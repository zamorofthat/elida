package telemetry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/embedded"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"elida/internal/redaction"
)

// fakeLogger is a minimal otellog.Logger that records every emitted Record,
// so tests can inspect attribute values without standing up an OTLP/stdout
// exporter.
type fakeLogger struct {
	embedded.Logger
	records []otellog.Record
}

func (f *fakeLogger) Emit(_ context.Context, r otellog.Record) {
	f.records = append(f.records, r)
}

func (f *fakeLogger) Enabled(_ context.Context, _ otellog.EnabledParameters) bool {
	return true
}

// attrString returns the string value of the named attribute on rec, or ""
// if absent.
func attrString(rec otellog.Record, key string) string {
	var out string
	rec.WalkAttributes(func(kv otellog.KeyValue) bool {
		if kv.Key == key {
			out = kv.Value.AsString()
			return false
		}
		return true
	})
	return out
}

// Feedback #10, fourth call site: emitContentRecord (used by
// EmitCapturedContentLog / EmitFlaggedContentLog) sends full request/response
// bodies to OTEL logs. It must use JSON-aware redaction (RedactBody) so a
// bare Luhn-valid numeric field isn't rewritten unquoted, corrupting the
// JSON on the SIEM/log feed — the same failure mode already fixed at the
// proxy capture, OCSF emitter, and cmd redactRecord call sites.
func TestEmitContentRecordUsesJSONAwareRedaction(t *testing.T) {
	fl := &fakeLogger{}
	p := &Provider{
		config: Config{Enabled: true, CaptureContent: "all", MaxBodySize: 4096},
		logger: fl,
	}
	p.SetRedactor(redaction.NewPatternRedactor())

	// n_params is a bare (unquoted) 16-digit numeric field that happens to
	// be Luhn-valid (the standard Visa test number) — raw regex redaction
	// mistakes it for a credit card and replaces it with an unquoted
	// [REDACTED_CC], producing invalid JSON.
	body := `{"n_params":4532015112830366,"created":1753305600,"choices":[{"message":{"content":"ssn 123-45-6789"}}]}`

	p.EmitCapturedContentLog(context.Background(), "sess-1", body, body, "claude-3", "anthropic")

	if len(fl.records) != 1 {
		t.Fatalf("expected 1 emitted log record, got %d", len(fl.records))
	}
	rec := fl.records[0]

	for _, attr := range []string{"elida.capture.request_body", "elida.capture.response_body"} {
		got := attrString(rec, attr)
		if got == "" {
			t.Fatalf("%s attribute missing or empty", attr)
		}

		var parsed map[string]any
		if err := json.Unmarshal([]byte(got), &parsed); err != nil {
			t.Fatalf("%s is not valid JSON: %v\nbody: %s", attr, err, got)
		}
		if parsed["created"] != float64(1753305600) {
			t.Errorf("%s: created mangled: %v (body: %s)", attr, parsed["created"], got)
		}
		if parsed["n_params"] != float64(4532015112830366) {
			t.Errorf("%s: n_params mangled: %v (body: %s)", attr, parsed["n_params"], got)
		}
		if !strings.Contains(got, "[REDACTED") {
			t.Errorf("%s: expected redaction marker, got: %s", attr, got)
		}
	}
}

// Same feedback-#10 exposure, different sink: ExportSessionRecord adds
// captured request/response bodies as trace span event attributes
// (independently of, and ungated by, the CaptureContent-gated OTEL log
// path exercised above). It must also use RedactBody.
func TestExportSessionRecordCapturedEventsUseJSONAwareRedaction(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	defer func() { _ = tp.Shutdown(context.Background()) }()

	fl := &fakeLogger{}
	p := &Provider{
		config:   Config{Enabled: true, CaptureContent: "none", MaxBodySize: 4096},
		provider: tp,
		tracer:   tp.Tracer("test"),
		logger:   fl,
	}
	p.SetRedactor(redaction.NewPatternRedactor())

	body := `{"n_params":4532015112830366,"created":1753305600,"choices":[{"message":{"content":"ssn 123-45-6789"}}]}`

	p.ExportSessionRecord(context.Background(), SessionRecord{
		SessionID: "sess-1",
		State:     "completed",
		Captures: []CapturedRequest{
			{RequestBody: body, ResponseBody: body},
		},
	})

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 exported span, got %d", len(spans))
	}

	var event *tracetest.SpanStub
	for i := range spans {
		for _, ev := range spans[i].Events {
			if ev.Name == "captured.request" {
				event = &spans[i]
			}
		}
	}
	if event == nil {
		t.Fatal("expected a captured.request span event")
	}

	var got map[string]string
	for _, ev := range event.Events {
		if ev.Name != "captured.request" {
			continue
		}
		got = map[string]string{}
		for _, kv := range ev.Attributes {
			if kv.Key == "capture.request_body" || kv.Key == "capture.response_body" {
				got[string(kv.Key)] = kv.Value.AsString()
			}
		}
	}
	if len(got) != 2 {
		t.Fatalf("expected capture.request_body and capture.response_body attributes, got %v", got)
	}

	for attr, val := range got {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(val), &parsed); err != nil {
			t.Fatalf("%s is not valid JSON: %v\nbody: %s", attr, err, val)
		}
		if parsed["created"] != float64(1753305600) {
			t.Errorf("%s: created mangled: %v (body: %s)", attr, parsed["created"], val)
		}
		if parsed["n_params"] != float64(4532015112830366) {
			t.Errorf("%s: n_params mangled: %v (body: %s)", attr, parsed["n_params"], val)
		}
		if !strings.Contains(val, "[REDACTED") {
			t.Errorf("%s: expected redaction marker, got: %s", attr, val)
		}
	}
}
