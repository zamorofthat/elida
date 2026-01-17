package telemetry

import (
	"context"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Config holds telemetry configuration
type Config struct {
	Enabled     bool   `yaml:"enabled"`
	Exporter    string `yaml:"exporter"`    // "otlp", "stdout", or "none"
	Endpoint    string `yaml:"endpoint"`    // OTLP endpoint (e.g., "localhost:4317")
	ServiceName string `yaml:"service_name"`
	Insecure    bool   `yaml:"insecure"` // Use insecure connection for OTLP
}

// Provider manages OpenTelemetry tracing
type Provider struct {
	config   Config
	tracer   trace.Tracer
	provider *sdktrace.TracerProvider
}

// NewProvider creates a new telemetry provider
func NewProvider(cfg Config) (*Provider, error) {
	if !cfg.Enabled {
		return &Provider{
			config: cfg,
			tracer: otel.Tracer("elida"),
		}, nil
	}

	if cfg.ServiceName == "" {
		cfg.ServiceName = "elida"
	}

	slog.Info("creating exporter", "type", cfg.Exporter)

	// Create exporter based on config
	var exporter sdktrace.SpanExporter
	var err error
	switch cfg.Exporter {
	case "otlp":
		slog.Debug("creating OTLP exporter")
		exporter, err = createOTLPExporter(cfg)
		if err != nil {
			return nil, err
		}
		slog.Info("OTLP exporter initialized", "endpoint", cfg.Endpoint)
	case "stdout":
		slog.Debug("creating stdout exporter")
		exporter, err = stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			slog.Error("stdout exporter creation failed", "error", err)
			return nil, err
		}
		slog.Info("stdout trace exporter initialized")
	default:
		// No exporter - tracing disabled
		return &Provider{
			config: cfg,
			tracer: otel.Tracer("elida"),
		}, nil
	}

	// Create simple trace provider without resource (avoids schema version conflicts)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter), // Use sync exporter for simplicity
	)

	// Set as global provider
	otel.SetTracerProvider(tp)

	return &Provider{
		config:   cfg,
		tracer:   tp.Tracer("elida"),
		provider: tp,
	}, nil
}

// createOTLPExporter creates an OTLP gRPC exporter
func createOTLPExporter(cfg Config) (sdktrace.SpanExporter, error) {
	ctx := context.Background()

	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(cfg.Endpoint),
	}

	if cfg.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}

	return otlptracegrpc.New(ctx, opts...)
}

// Tracer returns the tracer for creating spans
func (p *Provider) Tracer() trace.Tracer {
	return p.tracer
}

// Shutdown gracefully shuts down the trace provider
func (p *Provider) Shutdown(ctx context.Context) error {
	if p.provider != nil {
		return p.provider.Shutdown(ctx)
	}
	return nil
}

// Enabled returns whether telemetry is enabled
func (p *Provider) Enabled() bool {
	return p.config.Enabled && p.provider != nil
}

// Session span attributes
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
)

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
	// Create a new span specifically for the session record
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
