package telemetry

import (
	"context"
	"log/slog"
	"os"
	"regexp"
	"sync"
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
	ocsfEmitter   *OCSFEmitter
	redactor      contentRedactor
	redactorMu    sync.RWMutex
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

// SetOCSFEmitter attaches an OCSF emitter to the provider.
func (p *Provider) SetOCSFEmitter(e *OCSFEmitter) {
	p.ocsfEmitter = e
}

// SetRedactor attaches a redactor that will be applied to content-bearing
// span attributes and log record fields before they are exported.
func (p *Provider) SetRedactor(r contentRedactor) {
	p.redactorMu.Lock()
	p.redactor = r
	p.redactorMu.Unlock()
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
	rec.SetSeverity(otelSeverity(v.exportedSeverity()))
	body := "policy violation: " + v.RuleName
	if v.EvidenceOnly {
		body = "policy evidence (no risk contributed): " + v.RuleName
	}
	rec.SetBody(attribute.StringValue(body))
	rec.AddAttributes(
		// GenAI semconv
		attribute.String("gen_ai.conversation.id", sessionID),
		attribute.String("gen_ai.provider.name", providerName),
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.request.model", modelOrUnknown(model)),
		// ELIDA-specific
		attribute.String("elida.violation.rule", v.RuleName),
		attribute.String("elida.violation.severity", v.Severity),
		attribute.String("elida.violation.matched_text", truncateBody(p.redact(v.MatchedText), 200)),
		attribute.String("elida.violation.action", v.Action),
		attribute.String("elida.violation.description", v.Description),
		attribute.Bool("elida.violation.evidence_only", v.EvidenceOnly),
	)
	// Only a semantic or correlation violation carries an event ID and its
	// metadata, so a plain violation keeps exactly its key set.
	if v.Semantic != nil || v.Correlation != nil {
		// A blanked event ID is left out, as in the OCSF finding.
		if id := safeToken(v.EventID); id != "" {
			rec.AddAttributes(attribute.String("elida.violation.event_id", id))
		}
	}
	if v.Semantic != nil {
		rec.AddAttributes(semanticAttributes("elida.semantic.", *v.Semantic)...)
	}
	if v.Correlation != nil {
		rec.AddAttributes(correlationAttributes("elida.correlation.", *v.Correlation)...)
	}

	// Trace correlation
	setTraceContext(&rec, ctx)
	p.logger.Emit(ctx, rec)
}

// EmitSemanticDecisionLog emits one OTEL log record for one stored semantic
// decision. Strings pass safeToken; there is no content to redact. An
// evidence-only decision is logged at Info, as evidence-only violations are.
func (p *Provider) EmitSemanticDecisionLog(ctx context.Context, sessionID string, d SemanticDecision, model, providerName string) {
	if p.logger == nil {
		return
	}
	rule := safeToken(d.RuleName)
	var rec otellog.Record
	ts := d.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	rec.SetTimestamp(ts)
	sev := d.Severity
	body := "semantic decision: " + rule
	if d.EvidenceOnly {
		sev = "info"
		body += " (evidence only, no risk contributed)"
	}
	rec.SetSeverity(otelSeverity(sev))
	rec.SetBody(attribute.StringValue(body))
	rec.AddAttributes(
		attribute.String("gen_ai.conversation.id", sessionID),
		attribute.String("gen_ai.provider.name", providerName),
		attribute.String("gen_ai.request.model", modelOrUnknown(model)),
		attribute.String("elida.semantic.rule", rule),
		attribute.String("elida.semantic.event_category", safeToken(d.EventCategory)),
		attribute.String("elida.semantic.severity", safeToken(d.Severity)),
		attribute.String("elida.semantic.source_role", safeToken(d.SourceRole)),
		attribute.Int("elida.semantic.message_index", d.MessageIndex),
		attribute.Bool("elida.semantic.evidence_only", d.EvidenceOnly),
		attribute.Float64("elida.semantic.would_contribute_points", d.WouldContributePoints),
	)
	// A blanked event ID is left out, as on the violation exports.
	if id := safeToken(d.EventID); id != "" {
		rec.AddAttributes(attribute.String("elida.semantic.event_id", id))
	}
	rec.AddAttributes(semanticAttributes("elida.semantic.", d.Metadata)...)
	setTraceContext(&rec, ctx)
	p.logger.Emit(ctx, rec)
}

// EmitCorrelationShadowLog emits one Info log record for one shadow-mode
// correlation match. Strings pass safeToken and lists the export caps; no
// content exists to redact.
func (p *Provider) EmitCorrelationShadowLog(ctx context.Context, sessionID string, s CorrelationShadow, model, providerName string) {
	if p.logger == nil {
		return
	}
	var rec otellog.Record
	ts := s.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	rec.SetTimestamp(ts)
	rec.SetSeverity(otellog.SeverityInfo)
	rec.SetBody(attribute.StringValue("shadow correlation match: " + safeToken(s.Metadata.Rule) + " (calibration only, never a violation)"))
	rec.AddAttributes(
		attribute.String("gen_ai.conversation.id", sessionID),
		attribute.String("gen_ai.provider.name", providerName),
		attribute.String("gen_ai.request.model", modelOrUnknown(model)),
		attribute.Bool("elida.correlation.shadow", true),
		attribute.String("elida.correlation.severity", safeToken(s.Severity)),
	)
	rec.AddAttributes(correlationAttributes("elida.correlation.", s.Metadata)...)
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
	rec.SetBody(attribute.StringValue("session killed: " + reason))
	rec.AddAttributes(
		attribute.String("gen_ai.conversation.id", sessionID),
		attribute.String("gen_ai.provider.name", backend),
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.request.model", modelOrUnknown(model)),
		attribute.String("elida.session.state", "killed"),
		attribute.String("elida.session.kill_reason", reason),
		attribute.Int64("elida.duration.ms", durationMs),
		attribute.Int("elida.request.count", requestCount),
	)

	setTraceContext(&rec, ctx)
	p.logger.Emit(ctx, rec)
}

// EmitBlockLog emits a real-time block event OTEL log record
func (p *Provider) EmitBlockLog(ctx context.Context, sessionID, ruleName, matchedText, backend, model string) {
	if p.logger != nil {
		var rec otellog.Record
		rec.SetTimestamp(time.Now())
		rec.SetSeverity(otellog.SeverityWarn)
		rec.SetBody(attribute.StringValue("request blocked: " + ruleName))
		rec.AddAttributes(
			attribute.String("gen_ai.conversation.id", sessionID),
			attribute.String("gen_ai.provider.name", backend),
			attribute.String("gen_ai.operation.name", "chat"),
			attribute.String("gen_ai.request.model", modelOrUnknown(model)),
			attribute.String("elida.violation.rule", ruleName),
			attribute.String("elida.violation.matched_text", truncateBody(p.redact(matchedText), 200)),
			attribute.String("elida.violation.action", "block"),
		)

		setTraceContext(&rec, ctx)
		p.logger.Emit(ctx, rec)
	}

	// Emit OCSF Detection Finding for block event
	if p.ocsfEmitter != nil {
		bd := BuildBlockDetection(sessionID, ruleName, p.redact(matchedText), backend, model)
		p.ocsfEmitter.Emit(ctx, OCSFClassDetectionFinding, bd.SeverityID, bd)
	}
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

// unknownModel is the fallback stamped on gen_ai.request.model when the model
// is unknown (e.g. a request that omitted it, or a path that never captured it).
// Per the OTel GenAI semconv the attribute should be present; emitting an empty
// string pollutes dashboards with an empty-string series.
const unknownModel = "unknown"

// modelOrUnknown returns model, or unknownModel when model is empty.
func modelOrUnknown(model string) string {
	if model == "" {
		return unknownModel
	}
	return model
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
	rec.SetBody(attribute.StringValue(body))
	rec.AddAttributes(
		attribute.String("gen_ai.conversation.id", sessionID),
		attribute.String("gen_ai.provider.name", providerName),
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.request.model", modelOrUnknown(model)),
		attribute.String("elida.capture.request_body", truncateBody(p.redactBody(requestBody), maxSize)),
		attribute.String("elida.capture.response_body", truncateBody(p.redactBody(responseBody), maxSize)),
		attribute.Bool("elida.capture.flagged", flagged),
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
		attribute.String("gen_ai.request.model", modelOrUnknown(model)),
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
		attribute.String("gen_ai.request.model", modelOrUnknown(model)),
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
		rec.AddAttributes(attribute.String("trace_id", spanCtx.TraceID().String()))
	}
	if spanCtx.HasSpanID() {
		rec.AddAttributes(attribute.String("span_id", spanCtx.SpanID().String()))
	}
}

// redact applies the provider's redactor to s, or returns s unchanged if no
// redactor is set.
func (p *Provider) redact(s string) string {
	p.redactorMu.RLock()
	r := p.redactor
	p.redactorMu.RUnlock()
	if r == nil {
		return s
	}
	return r.Redact(s)
}

// redactBody applies the provider's JSON-aware body redaction to s, or
// returns s unchanged if no redactor is set. Use this (not redact) for
// full request/response bodies — see redaction.PatternRedactor.RedactBody.
func (p *Provider) redactBody(s string) string {
	p.redactorMu.RLock()
	r := p.redactor
	p.redactorMu.RUnlock()
	if r == nil {
		return s
	}
	return r.RedactBody(s)
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

	// SDR integrity attributes
	AttrSDRRootHash                = "elida.sdr.root_hash"
	AttrSDRAlgorithm               = "elida.sdr.algorithm"
	AttrSDRCanonicalizationVersion = "elida.sdr.canonicalization_version"
	AttrSDREventCount              = "elida.sdr.event_count"

	// Semantic decision attributes: how many stored semantic decisions the
	// session exported as logs (set only when OTEL logs are on), and how many
	// the policy engine dropped past its per-session cap.
	AttrSemanticDecisionCount   = "elida.semantic.decisions.count"
	AttrSemanticDecisionDropped = "elida.semantic.decisions.dropped"

	// AttrCorrelationShadowCount is how many shadow correlation matches the
	// session exported as logs (set only when OTEL logs are on).
	AttrCorrelationShadowCount = "elida.correlation.shadow.count"
)

// Violation represents a policy violation for telemetry export
type Violation struct {
	RuleName      string
	Description   string
	Severity      string
	MatchedText   string
	Action        string
	EventCategory string
	FrameworkRef  string
	SourceRole    string
	// EvidenceOnly marks a violation recorded as evidence for correlation
	// that contributed no risk (semantic detection in audit mode, and every
	// injection_elevated event). Exports carry the flag, report it at
	// informational severity, and leave it out of the session's max
	// severity, so a SIEM never reads audit evidence as a finding.
	EvidenceOnly bool
	// EventID is the policy engine's stable ID for the occurrence behind
	// this violation. It is exported for semantic and correlation
	// violations only (for a correlation match it is the match ID).
	EventID string
	// Semantic is the representative decision's metadata for a semantic
	// rule: scores, identifiers and offsets, never content. Every string in
	// it is exported through safeToken.
	Semantic *SemanticMetadata
	// Correlation is a temporal-correlation match's evidence: IDs, enums,
	// counts and times, never content. Every string is exported through
	// safeToken and every list through the MaxExportedCorrelation* caps.
	Correlation *CorrelationMetadata
}

// SemanticMetadata is a semantic decision's metadata for export, the
// telemetry mirror of storage.SemanticEvidence: scores, identifiers and
// offsets, never request content.
type SemanticMetadata struct {
	Signal           string
	Probability      float64
	AuxProbability   float64
	Vetoed           bool
	Model            string
	ModelVersion     string
	ModelChecksum    string
	ThresholdSet     string
	DecisionID       string
	Transform        string
	TransformDepth   int
	WindowStartByte  int
	WindowEndByte    int
	CoverageComplete bool
	ExecutionMode    string
	ProtectionScope  string
}

// semanticTokenRE matches the identifier-shaped values semantic metadata
// legitimately holds: decision IDs, model names and versions, checksums,
// transform chains ("base64_decode>url_decode"), threshold sets, modes and
// scopes. Free text (anything with a space, newline, quote or non-ASCII
// letter) does not match.
var semanticTokenRE = regexp.MustCompile(`^[A-Za-z0-9._:>+/=-]{1,128}$`)

// safeToken returns s if it is identifier-shaped, else "". It is a shape
// backstop for semantic metadata, not a proof that content cannot leave: it
// rejects free text (spaces, newlines and other control characters, quotes,
// non-ASCII) and anything over 128 bytes, but an identifier-shaped string
// passes whatever it encodes. The privacy argument is the sources: every
// exported field comes from the model manifest, config, hex IDs or the
// fixed preprocess transform names (the longest chain is 87 bytes), so the
// fields must keep being populated only from those.
func safeToken(s string) string {
	if semanticTokenRE.MatchString(s) {
		return s
	}
	return ""
}

// semanticAttrNames is the export allowlist, in export order. Span events
// prefix it with "semantic.", logs with "elida.semantic."; OCSF uses the
// bare names inside unmapped."elida.semantic".
var semanticAttrNames = []string{"signal", "probability", "aux_probability", "vetoed", "model", "model_version",
	"model_checksum", "threshold_set", "decision_id", "transform", "transform_depth", "window_start_byte",
	"window_end_byte", "coverage_complete", "execution_mode", "protection_scope"}

// semanticAttributes renders m as attributes named prefix+semanticAttrNames.
func semanticAttributes(prefix string, m SemanticMetadata) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String(prefix+"signal", safeToken(m.Signal)),
		attribute.Float64(prefix+"probability", m.Probability),
		attribute.Float64(prefix+"aux_probability", m.AuxProbability),
		attribute.Bool(prefix+"vetoed", m.Vetoed),
		attribute.String(prefix+"model", safeToken(m.Model)),
		attribute.String(prefix+"model_version", safeToken(m.ModelVersion)),
		attribute.String(prefix+"model_checksum", safeToken(m.ModelChecksum)),
		attribute.String(prefix+"threshold_set", safeToken(m.ThresholdSet)),
		attribute.String(prefix+"decision_id", safeToken(m.DecisionID)),
		attribute.String(prefix+"transform", safeToken(m.Transform)),
		attribute.Int(prefix+"transform_depth", m.TransformDepth),
		attribute.Int(prefix+"window_start_byte", m.WindowStartByte),
		attribute.Int(prefix+"window_end_byte", m.WindowEndByte),
		attribute.Bool(prefix+"coverage_complete", m.CoverageComplete),
		attribute.String(prefix+"execution_mode", safeToken(m.ExecutionMode)),
		attribute.String(prefix+"protection_scope", safeToken(m.ProtectionScope)),
	}
}

// MaxExportedCorrelationEventIDs and MaxExportedCorrelationCategories cap
// the list attributes of a correlation export (Plan C D10). The engine's
// own ceilings (32 repeated events, 8 sequence steps) make them exact today;
// they are hard caps in their own right so a future engine change cannot
// grow an attribute past them. event_count always reports the full count.
const (
	MaxExportedCorrelationEventIDs   = 32
	MaxExportedCorrelationCategories = 8
)

// correlationAttrNames is the correlation export allowlist, in export
// order. Span events prefix it with "correlation.", logs with
// "elida.correlation."; OCSF uses the bare names inside
// unmapped."elida.correlation".
var correlationAttrNames = []string{"match_id", "rule", "shape", "mode", "event_count", "categories", "first_event", "last_event", "event_ids"}

// safeTokens applies safeToken to each entry, drops the blanked ones and
// keeps the first limit survivors.
func safeTokens(in []string, limit int) []string {
	out := make([]string, 0, min(len(in), limit))
	for _, s := range in {
		if len(out) == limit {
			break
		}
		if t := safeToken(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// distinctTokens keeps the first appearance of each entry, then applies
// safeTokens with limit.
func distinctTokens(in []string, limit int) []string {
	seen := make(map[string]bool, len(in))
	uniq := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			uniq = append(uniq, s)
		}
	}
	return safeTokens(uniq, limit)
}

// safeTime renders t as RFC3339Nano in UTC, a shape safeToken accepts, or
// "" for the zero time.
func safeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// CorrelationMetadata is a correlation match's evidence for export, the
// telemetry mirror of storage.CorrelationEvidence plus the full event
// count: identifiers, enums, counts and times, never content. Lists are
// capped on export (MaxExportedCorrelation*), strings pass safeToken.
type CorrelationMetadata struct {
	MatchID    string
	Rule       string
	Shape      string
	Mode       string
	EventCount int
	Categories []string
	FirstEvent time.Time
	LastEvent  time.Time
	EventIDs   []string
}

// correlationAttributes renders m as attributes named prefix+correlationAttrNames.
func correlationAttributes(prefix string, m CorrelationMetadata) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String(prefix+"match_id", safeToken(m.MatchID)),
		attribute.String(prefix+"rule", safeToken(m.Rule)),
		attribute.String(prefix+"shape", safeToken(m.Shape)),
		attribute.String(prefix+"mode", safeToken(m.Mode)),
		attribute.Int(prefix+"event_count", m.EventCount),
		attribute.StringSlice(prefix+"categories", distinctTokens(m.Categories, MaxExportedCorrelationCategories)),
		attribute.String(prefix+"first_event", safeTime(m.FirstEvent)),
		attribute.String(prefix+"last_event", safeTime(m.LastEvent)),
		attribute.StringSlice(prefix+"event_ids", safeTokens(m.EventIDs, MaxExportedCorrelationEventIDs)),
	}
}

// SemanticDecision is one stored semantic decision for export: identifiers,
// scores and offsets, never content.
type SemanticDecision struct {
	EventID               string
	RuleName              string
	EventCategory         string
	Severity              string
	SourceRole            string
	MessageIndex          int
	EvidenceOnly          bool
	WouldContributePoints float64
	Timestamp             time.Time
	Metadata              SemanticMetadata
}

// CorrelationShadow is one shadow-mode correlation match for export:
// calibration data, never a finding. Identifiers and times only.
type CorrelationShadow struct {
	Timestamp time.Time
	Severity  string
	Metadata  CorrelationMetadata
}

// exportedSeverity is the severity a violation is exported at: its own,
// except that evidence-only violations are informational.
func (v Violation) exportedSeverity() string {
	if v.EvidenceOnly {
		return "info"
	}
	return v.Severity
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
	// SemanticDecisions holds every stored semantic decision of the ending
	// session instance (bounded by the policy engine's per-session cap);
	// each is exported as one OTEL log. SemanticDecisionsDropped counts the
	// decisions dropped past that cap.
	SemanticDecisions        []SemanticDecision
	SemanticDecisionsDropped int
	// CorrelationShadow holds the ending session instance's shadow
	// correlation matches (at most 50); each is exported as one OTEL log,
	// never as an OCSF finding.
	CorrelationShadow []CorrelationShadow
	Captures          []CapturedRequest
	CaptureCount      int
	Model             string // For GenAI semconv

	// WebSocket fields
	IsWebSocket  bool
	FrameCount   int64
	TextFrames   int64
	BinaryFrames int64

	// Token tracking
	TokensIn  int64
	TokensOut int64

	// SDR integrity metadata
	SDRRootHash                string
	SDRAlgorithm               string
	SDRCanonicalizationVersion string
	SDREventCount              int
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
	if !p.Enabled() && p.ocsfEmitter == nil {
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
		// Evidence-only violations contributed no risk, so they never raise
		// the exported max severity (the policy engine's own MaxSeverity
		// excludes them the same way).
		if sev := v.exportedSeverity(); severityOrder[sev] > severityOrder[maxSeverity] {
			maxSeverity = sev
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
	if record.SDRRootHash != "" {
		attrs = append(attrs,
			attribute.String(AttrSDRRootHash, record.SDRRootHash),
			attribute.String(AttrSDRAlgorithm, record.SDRAlgorithm),
			attribute.String(AttrSDRCanonicalizationVersion, record.SDRCanonicalizationVersion),
			attribute.Int(AttrSDREventCount, record.SDREventCount),
		)
	}
	// Counts only: a session without semantic decisions keeps its key set.
	// The count is of decision logs, so it is left out when OTEL logs are
	// off (nothing is exported then); the dropped count is kept.
	if len(record.SemanticDecisions) > 0 || record.SemanticDecisionsDropped > 0 {
		if p.logger != nil {
			attrs = append(attrs, attribute.Int(AttrSemanticDecisionCount, len(record.SemanticDecisions)))
		}
		attrs = append(attrs, attribute.Int(AttrSemanticDecisionDropped, record.SemanticDecisionsDropped))
	}
	if len(record.CorrelationShadow) > 0 && p.logger != nil {
		attrs = append(attrs, attribute.Int(AttrCorrelationShadowCount, len(record.CorrelationShadow)))
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
		attrs := []attribute.KeyValue{
			attribute.String("rule_name", v.RuleName),
			attribute.String("description", v.Description),
			attribute.String("severity", v.Severity),
			attribute.String("matched_text", p.redact(v.MatchedText)),
			attribute.String("action", v.Action),
			attribute.Bool("evidence_only", v.EvidenceOnly),
		}
		// Only a semantic or correlation violation carries an event ID and
		// its metadata, so a plain violation keeps exactly its key set.
		if v.Semantic != nil || v.Correlation != nil {
			// A blanked event ID is left out, as in the OCSF finding.
			if id := safeToken(v.EventID); id != "" {
				attrs = append(attrs, attribute.String("event_id", id))
			}
		}
		if v.Semantic != nil {
			attrs = append(attrs, semanticAttributes("semantic.", *v.Semantic)...)
		}
		if v.Correlation != nil {
			attrs = append(attrs, correlationAttributes("correlation.", *v.Correlation)...)
		}
		span.AddEvent("policy.violation", trace.WithAttributes(attrs...))
	}

	// Add captured request/response events
	for i, c := range record.Captures {
		span.AddEvent("captured.request",
			trace.WithAttributes(
				attribute.Int("capture.index", i),
				attribute.String("capture.timestamp", c.Timestamp),
				attribute.String("capture.method", c.Method),
				attribute.String("capture.path", c.Path),
				attribute.String("capture.request_body", p.redactBody(c.RequestBody)),
				attribute.String("capture.response_body", p.redactBody(c.ResponseBody)),
				attribute.Int("capture.status_code", c.StatusCode),
			),
		)
	}

	span.End()

	// Emit OTEL logs for each violation
	for _, v := range record.Violations {
		p.EmitViolationLog(ctx, record.SessionID, v, record.Model, record.Backend)
	}

	// One log per stored semantic decision (bounded by the policy engine's
	// per-session cap). No OCSF finding per decision: findings stay one per
	// rule, carrying the representative decision.
	for _, d := range record.SemanticDecisions {
		p.EmitSemanticDecisionLog(ctx, record.SessionID, d, record.Model, record.Backend)
	}

	// One log per shadow correlation match (at most 50 per session). No
	// OCSF finding: a shadow match is calibration data.
	for _, s := range record.CorrelationShadow {
		p.EmitCorrelationShadowLog(ctx, record.SessionID, s, record.Model, record.Backend)
	}

	// Emit OCSF Detection Finding events for policy violations
	if p.ocsfEmitter != nil {
		for _, v := range record.Violations {
			finding := BuildPolicyDetection(record.SessionID, v, record)
			p.ocsfEmitter.Emit(ctx, OCSFClassDetectionFinding, finding.SeverityID, finding)
		}
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
