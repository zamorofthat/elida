package telemetry

import (
	"context"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/credentials"
)

// Config holds telemetry configuration
type Config struct {
	Enabled        bool   `yaml:"enabled"`
	Exporter       string `yaml:"exporter"` // "otlp", "stdout", or "none"
	Endpoint       string `yaml:"endpoint"` // OTLP endpoint (e.g., "localhost:4317")
	ServiceName    string `yaml:"service_name"`
	Insecure       bool   `yaml:"insecure"`        // Use insecure connection for OTLP
	CaptureContent string `yaml:"capture_content"` // "none" (default), "flagged", or "all"
	MaxBodySize    int    `yaml:"max_body_size"`   // Truncation limit for bodies (default 4096)
}

// Provider manages OpenTelemetry tracing, logging, and metrics
type Provider struct {
	config        Config
	tracer        trace.Tracer
	provider      *sdktrace.TracerProvider
	logProvider   *sdklog.LoggerProvider
	logger        otellog.Logger
	meterProvider *sdkmetric.MeterProvider
	meter         metric.Meter
	// GenAI metrics instruments
	tokenUsage        metric.Int64Histogram
	operationDuration metric.Float64Histogram
}

// NewProvider creates a new telemetry provider with traces, logs, and metrics
func NewProvider(cfg Config) (*Provider, error) {
	if !cfg.Enabled {
		return noopWithConfig(cfg), nil
	}

	if cfg.ServiceName == "" {
		cfg.ServiceName = "elida"
	}
	if cfg.MaxBodySize == 0 {
		cfg.MaxBodySize = 4096
	}

	slog.Info("creating telemetry exporters", "type", cfg.Exporter)

	// Create resource with service name
	res := resource.NewSchemaless(
		semconv.ServiceName(cfg.ServiceName),
	)

	p := &Provider{config: cfg}

	switch cfg.Exporter {
	case "otlp":
		if err := p.initOTLP(cfg, res); err != nil {
			return nil, err
		}
	case "stdout":
		if err := p.initStdout(cfg, res); err != nil {
			return nil, err
		}
	default:
		return noopWithConfig(cfg), nil
	}

	return p, nil
}

// initOTLP initializes OTLP gRPC exporters for traces, logs, and metrics
func (p *Provider) initOTLP(cfg Config, res *resource.Resource) error {
	ctx := context.Background()

	// Common gRPC options
	var grpcCreds credentials.TransportCredentials
	if !cfg.Insecure {
		grpcCreds = credentials.NewClientTLSFromCert(nil, "")
	}

	// --- Trace exporter ---
	traceOpts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(cfg.Endpoint),
	}
	if cfg.Insecure {
		traceOpts = append(traceOpts, otlptracegrpc.WithInsecure())
	} else {
		traceOpts = append(traceOpts, otlptracegrpc.WithTLSCredentials(grpcCreds))
	}
	traceExp, err := otlptracegrpc.New(ctx, traceOpts...)
	if err != nil {
		return err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(traceExp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	p.provider = tp
	p.tracer = tp.Tracer("elida")

	// --- Log exporter ---
	logOpts := []otlploggrpc.Option{
		otlploggrpc.WithEndpoint(cfg.Endpoint),
	}
	if cfg.Insecure {
		logOpts = append(logOpts, otlploggrpc.WithInsecure())
	} else {
		logOpts = append(logOpts, otlploggrpc.WithTLSCredentials(grpcCreds))
	}
	logExp, err := otlploggrpc.New(ctx, logOpts...)
	if err != nil {
		slog.Warn("failed to create OTLP log exporter, logs disabled", "error", err)
	} else {
		lp := sdklog.NewLoggerProvider(
			sdklog.WithProcessor(sdklog.NewSimpleProcessor(logExp)),
			sdklog.WithResource(res),
		)
		p.logProvider = lp
		p.logger = lp.Logger("elida")
		slog.Info("OTLP log exporter initialized", "endpoint", cfg.Endpoint)
	}

	// --- Metric exporter ---
	metricOpts := []otlpmetricgrpc.Option{
		otlpmetricgrpc.WithEndpoint(cfg.Endpoint),
	}
	if cfg.Insecure {
		metricOpts = append(metricOpts, otlpmetricgrpc.WithInsecure())
	} else {
		metricOpts = append(metricOpts, otlpmetricgrpc.WithTLSCredentials(grpcCreds))
	}
	metricExp, err := otlpmetricgrpc.New(ctx, metricOpts...)
	if err != nil {
		slog.Warn("failed to create OTLP metric exporter, metrics disabled", "error", err)
	} else {
		mp := sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp, sdkmetric.WithInterval(30*time.Second))),
			sdkmetric.WithResource(res),
		)
		p.meterProvider = mp
		p.meter = mp.Meter("elida")
		p.initMetricInstruments()
		slog.Info("OTLP metric exporter initialized", "endpoint", cfg.Endpoint)
	}

	slog.Info("OTLP exporters initialized", "endpoint", cfg.Endpoint)
	return nil
}

// initStdout initializes stdout exporters for traces, logs, and metrics
func (p *Provider) initStdout(_ Config, res *resource.Resource) error {
	// --- Trace exporter ---
	traceExp, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		return err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(traceExp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	p.provider = tp
	p.tracer = tp.Tracer("elida")

	// --- Log exporter ---
	logExp, err := stdoutlog.New()
	if err != nil {
		slog.Warn("failed to create stdout log exporter", "error", err)
	} else {
		lp := sdklog.NewLoggerProvider(
			sdklog.WithProcessor(sdklog.NewSimpleProcessor(logExp)),
			sdklog.WithResource(res),
		)
		p.logProvider = lp
		p.logger = lp.Logger("elida")
		slog.Info("stdout log exporter initialized")
	}

	// --- Metric exporter ---
	metricExp, err := stdoutmetric.New()
	if err != nil {
		slog.Warn("failed to create stdout metric exporter", "error", err)
	} else {
		mp := sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp, sdkmetric.WithInterval(30*time.Second))),
			sdkmetric.WithResource(res),
		)
		p.meterProvider = mp
		p.meter = mp.Meter("elida")
		p.initMetricInstruments()
		slog.Info("stdout metric exporter initialized")
	}

	return nil
}

// initMetricInstruments creates the GenAI metric instruments
func (p *Provider) initMetricInstruments() {
	var err error

	p.tokenUsage, err = p.meter.Int64Histogram(
		"gen_ai.client.token.usage",
		metric.WithDescription("Number of tokens used in GenAI client requests"),
		metric.WithUnit("{token}"),
	)
	if err != nil {
		slog.Warn("failed to create token usage metric", "error", err)
	}

	p.operationDuration, err = p.meter.Float64Histogram(
		"gen_ai.client.operation.duration",
		metric.WithDescription("Duration of GenAI client operations"),
		metric.WithUnit("s"),
	)
	if err != nil {
		slog.Warn("failed to create operation duration metric", "error", err)
	}
}

func noopWithConfig(cfg Config) *Provider {
	return &Provider{
		config: cfg,
		tracer: otel.Tracer("elida"),
	}
}

// Tracer returns the tracer for creating spans
func (p *Provider) Tracer() trace.Tracer {
	return p.tracer
}

// Shutdown gracefully shuts down all providers
func (p *Provider) Shutdown(ctx context.Context) error {
	var errs []error
	if p.provider != nil {
		if err := p.provider.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if p.logProvider != nil {
		if err := p.logProvider.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if p.meterProvider != nil {
		if err := p.meterProvider.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// Enabled returns whether telemetry is enabled
func (p *Provider) Enabled() bool {
	return p.config.Enabled && p.provider != nil
}

// LogsEnabled returns whether log export is available
func (p *Provider) LogsEnabled() bool {
	return p.logger != nil
}

// MetricsEnabled returns whether metric export is available
func (p *Provider) MetricsEnabled() bool {
	return p.meter != nil && p.meterProvider != nil
}

// --- Log Severity Mapping ---

// otelSeverity maps violation/event severity to OTEL log severity
func otelSeverity(severity string) otellog.Severity {
	switch severity {
	case "info":
		return otellog.SeverityInfo
	case "warning":
		return otellog.SeverityWarn
	case "critical":
		return otellog.SeverityError
	default:
		return otellog.SeverityInfo
	}
}

// --- Log Emit Functions ---

// EmitViolationLog emits a per-violation OTEL log record
func (p *Provider) EmitViolationLog(ctx context.Context, sessionID string, v Violation, model, providerName string) {
	if p.logger == nil {
		return
	}

	var rec otellog.Record
	rec.SetTimestamp(time.Now())
	rec.SetSeverity(otelSeverity(v.Severity))
	rec.SetBody(otellog.StringValue("policy violation: " + v.RuleName))
	rec.AddAttributes(
		// GenAI semconv
		otellog.String("gen_ai.conversation.id", sessionID),
		otellog.String("gen_ai.provider.name", providerName),
		otellog.String("gen_ai.operation.name", "chat"),
		otellog.String("gen_ai.request.model", model),
		// ELIDA-specific
		otellog.String("elida.violation.rule", v.RuleName),
		otellog.String("elida.violation.severity", v.Severity),
		otellog.String("elida.violation.matched_text", truncateBody(v.MatchedText, 200)),
		otellog.String("elida.violation.action", v.Action),
		otellog.String("elida.violation.description", v.Description),
	)

	// Trace correlation
	setTraceContext(&rec, ctx)
	p.logger.Emit(ctx, rec)
}

// EmitSessionKilledLog emits a session kill/terminate OTEL log record
func (p *Provider) EmitSessionKilledLog(ctx context.Context, sessionID, reason, backend, model string, durationMs int64, requestCount int) {
	if p.logger == nil {
		return
	}

	var rec otellog.Record
	rec.SetTimestamp(time.Now())
	rec.SetSeverity(otellog.SeverityError)
	rec.SetBody(otellog.StringValue("session killed: " + reason))
	rec.AddAttributes(
		otellog.String("gen_ai.conversation.id", sessionID),
		otellog.String("gen_ai.provider.name", backend),
		otellog.String("gen_ai.operation.name", "chat"),
		otellog.String("gen_ai.request.model", model),
		otellog.String("elida.session.state", "killed"),
		otellog.String("elida.session.kill_reason", reason),
		otellog.Int64("elida.duration.ms", durationMs),
		otellog.Int("elida.request.count", requestCount),
	)

	setTraceContext(&rec, ctx)
	p.logger.Emit(ctx, rec)
}

// EmitBlockLog emits a real-time block event OTEL log record
func (p *Provider) EmitBlockLog(ctx context.Context, sessionID, ruleName, matchedText, backend, model string) {
	if p.logger == nil {
		return
	}

	var rec otellog.Record
	rec.SetTimestamp(time.Now())
	rec.SetSeverity(otellog.SeverityWarn)
	rec.SetBody(otellog.StringValue("request blocked: " + ruleName))
	rec.AddAttributes(
		otellog.String("gen_ai.conversation.id", sessionID),
		otellog.String("gen_ai.provider.name", backend),
		otellog.String("gen_ai.operation.name", "chat"),
		otellog.String("gen_ai.request.model", model),
		otellog.String("elida.violation.rule", ruleName),
		otellog.String("elida.violation.matched_text", truncateBody(matchedText, 200)),
		otellog.String("elida.violation.action", "block"),
	)

	setTraceContext(&rec, ctx)
	p.logger.Emit(ctx, rec)
}

// EmitCapturedContentLog emits captured request/response bodies as an OTEL log record.
// Only emits when capture_content mode is "all".
func (p *Provider) EmitCapturedContentLog(ctx context.Context, sessionID, requestBody, responseBody, model, providerName string) {
	if p.logger == nil || p.config.CaptureContent != "all" {
		return
	}
	p.emitContentRecord(ctx, sessionID, requestBody, responseBody, model, providerName, false)
}

// EmitFlaggedContentLog emits captured request/response bodies for policy-flagged sessions.
// Emits when capture_content mode is "flagged" or "all".
func (p *Provider) EmitFlaggedContentLog(ctx context.Context, sessionID, requestBody, responseBody, model, providerName string) {
	if p.logger == nil {
		return
	}
	mode := p.config.CaptureContent
	if mode != "flagged" && mode != "all" {
		return
	}
	p.emitContentRecord(ctx, sessionID, requestBody, responseBody, model, providerName, true)
}

// ShouldCaptureAll returns true if capture_content mode is "all"
func (p *Provider) ShouldCaptureAll() bool {
	return p.config.CaptureContent == "all"
}

// ShouldCaptureFlagged returns true if capture_content mode is "flagged" or "all"
func (p *Provider) ShouldCaptureFlagged() bool {
	mode := p.config.CaptureContent
	return mode == "flagged" || mode == "all"
}

func (p *Provider) emitContentRecord(ctx context.Context, sessionID, requestBody, responseBody, model, providerName string, flagged bool) {
	maxSize := p.config.MaxBodySize
	if maxSize == 0 {
		maxSize = 4096
	}

	severity := otellog.SeverityInfo
	body := "captured content"
	if flagged {
		severity = otellog.SeverityWarn
		body = "flagged content"
	}

	var rec otellog.Record
	rec.SetTimestamp(time.Now())
	rec.SetSeverity(severity)
	rec.SetBody(otellog.StringValue(body))
	rec.AddAttributes(
		otellog.String("gen_ai.conversation.id", sessionID),
		otellog.String("gen_ai.provider.name", providerName),
		otellog.String("gen_ai.operation.name", "chat"),
		otellog.String("gen_ai.request.model", model),
		otellog.String("elida.capture.request_body", truncateBody(requestBody, maxSize)),
		otellog.String("elida.capture.response_body", truncateBody(responseBody, maxSize)),
		otellog.Bool("elida.capture.flagged", flagged),
	)

	setTraceContext(&rec, ctx)
	p.logger.Emit(ctx, rec)
}

// --- Metric Recording Functions ---

// RecordTokenUsage records gen_ai.client.token.usage histogram
func (p *Provider) RecordTokenUsage(ctx context.Context, inputTokens, outputTokens int64, model, providerName string) {
	if p.tokenUsage == nil {
		return
	}

	commonAttrs := []attribute.KeyValue{
		attribute.String("gen_ai.request.model", model),
		attribute.String("gen_ai.provider.name", providerName),
		attribute.String("gen_ai.operation.name", "chat"),
	}

	if inputTokens > 0 {
		attrs := append(commonAttrs, attribute.String("gen_ai.token.type", "input"))
		p.tokenUsage.Record(ctx, inputTokens, metric.WithAttributes(attrs...))
	}
	if outputTokens > 0 {
		attrs := append(commonAttrs, attribute.String("gen_ai.token.type", "output"))
		p.tokenUsage.Record(ctx, outputTokens, metric.WithAttributes(attrs...))
	}
}

// RecordOperationDuration records gen_ai.client.operation.duration histogram
func (p *Provider) RecordOperationDuration(ctx context.Context, durationSec float64, model, providerName string, hasError bool) {
	if p.operationDuration == nil {
		return
	}

	attrs := []attribute.KeyValue{
		attribute.String("gen_ai.request.model", model),
		attribute.String("gen_ai.provider.name", providerName),
		attribute.String("gen_ai.operation.name", "chat"),
	}
	if hasError {
		attrs = append(attrs, attribute.String("error.type", "policy_violation"))
	}

	p.operationDuration.Record(ctx, durationSec, metric.WithAttributes(attrs...))
}

// --- Helper Functions ---

// setTraceContext adds trace/span IDs as attributes for correlation
func setTraceContext(rec *otellog.Record, ctx context.Context) {
	spanCtx := trace.SpanFromContext(ctx).SpanContext()
	if spanCtx.HasTraceID() {
		rec.AddAttributes(otellog.String("trace_id", spanCtx.TraceID().String()))
	}
	if spanCtx.HasSpanID() {
		rec.AddAttributes(otellog.String("span_id", spanCtx.SpanID().String()))
	}
}

// truncateBody truncates a string to maxLen bytes
func truncateBody(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...[truncated]"
}

// --- Session span attributes ---

const (
	AttrSessionID     = "elida.session.id"
	AttrSessionState  = "elida.session.state"
	AttrBackend       = "elida.backend"
	AttrClientAddr    = "elida.client.addr"
	AttrBytesIn       = "elida.bytes.in"
	AttrBytesOut      = "elida.bytes.out"
	AttrRequestCount  = "elida.request.count"
	AttrDurationMs    = "elida.duration.ms"
	AttrRequestMethod = "http.request.method"
	AttrRequestPath   = "url.path"
	AttrResponseCode  = "http.response.status_code"
	AttrStreaming     = "elida.streaming"

	// Violation attributes
	AttrViolationCount    = "elida.violations.count"
	AttrViolationRules    = "elida.violations.rules"
	AttrViolationSeverity = "elida.violations.max_severity"
	AttrViolationActions  = "elida.violations.actions"
	AttrCaptureCount      = "elida.captures.count"

	// WebSocket attributes
	AttrIsWebSocket  = "elida.websocket"
	AttrFrameCount   = "elida.websocket.frame_count"
	AttrTextFrames   = "elida.websocket.text_frames"
	AttrBinaryFrames = "elida.websocket.binary_frames"
)

// Violation represents a policy violation for telemetry export
type Violation struct {
	RuleName    string
	Description string
	Severity    string
	MatchedText string
	Action      string
}

// CapturedRequest represents a captured request/response for telemetry export
type CapturedRequest struct {
	Timestamp    string
	Method       string
	Path         string
	RequestBody  string
	ResponseBody string
	StatusCode   int
}

// SessionRecord contains all data for telemetry export
type SessionRecord struct {
	SessionID    string
	State        string
	Backend      string
	ClientAddr   string
	DurationMs   int64
	RequestCount int
	BytesIn      int64
	BytesOut     int64
	Violations   []Violation
	Captures     []CapturedRequest
	CaptureCount int
	Model        string // For GenAI semconv

	// WebSocket fields
	IsWebSocket  bool
	FrameCount   int64
	TextFrames   int64
	BinaryFrames int64

	// Token tracking
	TokensIn  int64
	TokensOut int64
}

// StartRequestSpan starts a span for an HTTP request
func (p *Provider) StartRequestSpan(ctx context.Context, sessionID, method, path string, streaming bool) (context.Context, trace.Span) {
	ctx, span := p.tracer.Start(ctx, "proxy.request",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String(AttrSessionID, sessionID),
			attribute.String(AttrRequestMethod, method),
			attribute.String(AttrRequestPath, path),
			attribute.Bool(AttrStreaming, streaming),
		),
	)
	return ctx, span
}

// EndRequestSpan ends a request span with additional attributes
func (p *Provider) EndRequestSpan(span trace.Span, statusCode int, bytesIn, bytesOut int64, err error) {
	span.SetAttributes(
		attribute.Int(AttrResponseCode, statusCode),
		attribute.Int64(AttrBytesIn, bytesIn),
		attribute.Int64(AttrBytesOut, bytesOut),
	)
	if err != nil {
		span.RecordError(err)
	}
	span.End()
}

// RecordSessionCreated records a session creation event
func (p *Provider) RecordSessionCreated(ctx context.Context, sessionID, backend, clientAddr string) {
	span := trace.SpanFromContext(ctx)
	span.AddEvent("session.created",
		trace.WithAttributes(
			attribute.String(AttrSessionID, sessionID),
			attribute.String(AttrBackend, backend),
			attribute.String(AttrClientAddr, clientAddr),
		),
	)
}

// RecordSessionEnded records a session end event (session record for audit)
func (p *Provider) RecordSessionEnded(ctx context.Context, sessionID, state, backend, clientAddr string, durationMs int64, requestCount int, bytesIn, bytesOut int64) {
	_, span := p.tracer.Start(ctx, "session.record",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String(AttrSessionID, sessionID),
			attribute.String(AttrSessionState, state),
			attribute.String(AttrBackend, backend),
			attribute.String(AttrClientAddr, clientAddr),
			attribute.Int64(AttrDurationMs, durationMs),
			attribute.Int(AttrRequestCount, requestCount),
			attribute.Int64(AttrBytesIn, bytesIn),
			attribute.Int64(AttrBytesOut, bytesOut),
		),
	)
	span.End()

	slog.Info("session record exported",
		"session_id", sessionID,
		"state", state,
		"duration_ms", durationMs,
		"requests", requestCount,
		"bytes_in", bytesIn,
		"bytes_out", bytesOut,
	)
}

// ExportSessionRecord exports a complete session record with violations to telemetry
func (p *Provider) ExportSessionRecord(ctx context.Context, record SessionRecord) {
	if !p.Enabled() {
		return
	}

	// Build violation rule names and actions for attributes
	var ruleNames []string
	var actions []string
	maxSeverity := "info"
	severityOrder := map[string]int{"info": 0, "warning": 1, "critical": 2}

	for _, v := range record.Violations {
		ruleNames = append(ruleNames, v.RuleName)
		actions = append(actions, v.Action)
		if severityOrder[v.Severity] > severityOrder[maxSeverity] {
			maxSeverity = v.Severity
		}
	}

	// Build attributes list
	attrs := []attribute.KeyValue{
		attribute.String(AttrSessionID, record.SessionID),
		attribute.String(AttrSessionState, record.State),
		attribute.String(AttrBackend, record.Backend),
		attribute.String(AttrClientAddr, record.ClientAddr),
		attribute.Int64(AttrDurationMs, record.DurationMs),
		attribute.Int(AttrRequestCount, record.RequestCount),
		attribute.Int64(AttrBytesIn, record.BytesIn),
		attribute.Int64(AttrBytesOut, record.BytesOut),
		attribute.Int(AttrViolationCount, len(record.Violations)),
		attribute.StringSlice(AttrViolationRules, ruleNames),
		attribute.String(AttrViolationSeverity, maxSeverity),
		attribute.StringSlice(AttrViolationActions, actions),
		attribute.Int(AttrCaptureCount, record.CaptureCount),
	}

	// Add WebSocket attributes if this is a WebSocket session
	if record.IsWebSocket {
		attrs = append(attrs,
			attribute.Bool(AttrIsWebSocket, true),
			attribute.Int64(AttrFrameCount, record.FrameCount),
			attribute.Int64(AttrTextFrames, record.TextFrames),
			attribute.Int64(AttrBinaryFrames, record.BinaryFrames),
		)
	}

	// Create session record span with all attributes
	_, span := p.tracer.Start(ctx, "session.record",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attrs...),
	)

	// Add individual violation events for detailed tracking
	for _, v := range record.Violations {
		span.AddEvent("policy.violation",
			trace.WithAttributes(
				attribute.String("rule_name", v.RuleName),
				attribute.String("description", v.Description),
				attribute.String("severity", v.Severity),
				attribute.String("matched_text", v.MatchedText),
				attribute.String("action", v.Action),
			),
		)
	}

	// Add captured request/response events
	for i, c := range record.Captures {
		span.AddEvent("captured.request",
			trace.WithAttributes(
				attribute.Int("capture.index", i),
				attribute.String("capture.timestamp", c.Timestamp),
				attribute.String("capture.method", c.Method),
				attribute.String("capture.path", c.Path),
				attribute.String("capture.request_body", c.RequestBody),
				attribute.String("capture.response_body", c.ResponseBody),
				attribute.Int("capture.status_code", c.StatusCode),
			),
		)
	}

	span.End()

	// Emit OTEL logs for each violation
	for _, v := range record.Violations {
		p.EmitViolationLog(ctx, record.SessionID, v, record.Model, record.Backend)
	}

	// Emit session killed log if applicable
	if record.State == "killed" || record.State == "terminated" {
		p.EmitSessionKilledLog(ctx, record.SessionID, record.State, record.Backend, record.Model, record.DurationMs, record.RequestCount)
	}

	// Emit captured content logs based on capture_content mode:
	//   "none"    — no content emitted
	//   "flagged" — only emit content for sessions with violations
	//   "all"     — emit all captured content
	isFlagged := len(record.Violations) > 0
	for _, c := range record.Captures {
		if isFlagged {
			p.EmitFlaggedContentLog(ctx, record.SessionID, c.RequestBody, c.ResponseBody, record.Model, record.Backend)
		} else {
			p.EmitCapturedContentLog(ctx, record.SessionID, c.RequestBody, c.ResponseBody, record.Model, record.Backend)
		}
	}

	// Record token usage metrics
	if record.TokensIn > 0 || record.TokensOut > 0 {
		p.RecordTokenUsage(ctx, record.TokensIn, record.TokensOut, record.Model, record.Backend)
	}

	// Record operation duration metric
	if record.DurationMs > 0 {
		p.RecordOperationDuration(ctx, float64(record.DurationMs)/1000.0, record.Model, record.Backend, record.State == "killed")
	}

	slog.Debug("session record exported to telemetry",
		"session_id", record.SessionID,
		"state", record.State,
		"violations", len(record.Violations),
		"captures", record.CaptureCount,
	)
}

// RecordSessionKilled records a session kill event
func (p *Provider) RecordSessionKilled(ctx context.Context, sessionID string) {
	span := trace.SpanFromContext(ctx)
	span.AddEvent("session.killed",
		trace.WithAttributes(
			attribute.String(AttrSessionID, sessionID),
		),
	)
}

// DefaultConfig returns a default telemetry configuration
func DefaultConfig() Config {
	return Config{
		Enabled:     false,
		Exporter:    "none",
		ServiceName: "elida",
		MaxBodySize: 4096,
	}
}

// ConfigFromEnv creates config from environment variables
func ConfigFromEnv() Config {
	cfg := DefaultConfig()

	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		cfg.Enabled = true
		cfg.Exporter = "otlp"
		cfg.Endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
		cfg.Insecure = os.Getenv("OTEL_EXPORTER_OTLP_INSECURE") == "true"
	}

	if os.Getenv("ELIDA_TELEMETRY_ENABLED") == "true" {
		cfg.Enabled = true
	}
	if os.Getenv("ELIDA_TELEMETRY_EXPORTER") != "" {
		cfg.Exporter = os.Getenv("ELIDA_TELEMETRY_EXPORTER")
	}
	if os.Getenv("ELIDA_TELEMETRY_ENDPOINT") != "" {
		cfg.Endpoint = os.Getenv("ELIDA_TELEMETRY_ENDPOINT")
	}

	return cfg
}

// NoopProvider returns a provider that does nothing (for testing)
func NoopProvider() *Provider {
	return &Provider{
		config: Config{Enabled: false},
		tracer: otel.Tracer("elida-noop"),
	}
}

// SpanFromContext extracts a span from context
func SpanFromContext(ctx context.Context) trace.Span {
	return trace.SpanFromContext(ctx)
}

// ContextWithTimeout creates a context with timeout for shutdown
func ContextWithTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}
