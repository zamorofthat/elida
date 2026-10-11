package telemetry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// exportForTest runs ExportSessionRecord against an in-memory span
// exporter and the fake logger, and returns what was emitted.
func exportForTest(t *testing.T, rec SessionRecord) (tracetest.SpanStubs, *fakeLogger) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	fl := &fakeLogger{}
	p := &Provider{
		config:   Config{Enabled: true, CaptureContent: "none", MaxBodySize: 4096},
		provider: tp,
		tracer:   tp.Tracer("test"),
		logger:   fl,
	}
	p.ExportSessionRecord(context.Background(), rec)
	return exp.GetSpans(), fl
}

func eventKeys(ev sdktrace.Event) map[string]bool {
	keys := map[string]bool{}
	for _, kv := range ev.Attributes {
		keys[string(kv.Key)] = true
	}
	return keys
}

func logKeys(rec otellog.Record) map[string]bool {
	keys := map[string]bool{}
	rec.WalkAttributes(func(kv attribute.KeyValue) bool {
		keys[string(kv.Key)] = true
		return true
	})
	return keys
}

func sameKeys(t *testing.T, what string, got map[string]bool, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s keys = %v, want exactly %v", what, got, want)
	}
	for _, k := range want {
		if !got[k] {
			t.Fatalf("%s keys = %v, missing %q", what, got, k)
		}
	}
}

func plainRecord() SessionRecord {
	return SessionRecord{
		SessionID: "sess-plain",
		State:     "completed",
		Backend:   "anthropic",
		Violations: []Violation{
			{RuleName: "baseline_marker", Description: "marker", Severity: "warning", Action: "flag", MatchedText: "zz"},
		},
	}
}

// KEEP. A violation without semantic metadata exports exactly these span
// event attributes, before and after Plan C-1.
func TestBaselineC1_PlainViolationSpanEventKeys(t *testing.T) {
	spans, _ := exportForTest(t, plainRecord())
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	var n int
	for _, ev := range spans[0].Events {
		if ev.Name != "policy.violation" {
			continue
		}
		n++
		sameKeys(t, "policy.violation", eventKeys(ev),
			[]string{"rule_name", "description", "severity", "matched_text", "action", "evidence_only"})
	}
	if n != 1 {
		t.Fatalf("policy.violation events = %d, want 1", n)
	}
}

// KEEP. ...and exactly these violation log attributes.
func TestBaselineC1_PlainViolationLogKeys(t *testing.T) {
	_, fl := exportForTest(t, plainRecord())
	var n int
	for _, rec := range fl.records {
		if attrString(rec, "elida.violation.rule") == "" {
			continue
		}
		n++
		sameKeys(t, "violation log", logKeys(rec), []string{
			"gen_ai.conversation.id", "gen_ai.provider.name", "gen_ai.operation.name", "gen_ai.request.model",
			"elida.violation.rule", "elida.violation.severity", "elida.violation.matched_text",
			"elida.violation.action", "elida.violation.description", "elida.violation.evidence_only",
		})
	}
	if n != 1 {
		t.Fatalf("violation logs = %d, want 1", n)
	}
}

// KEEP. A record without semantic decisions puts no semantic attribute on
// the session span and emits no semantic decision log.
func TestBaselineC1_NoSemanticAttributesWithoutDecisions(t *testing.T) {
	spans, fl := exportForTest(t, plainRecord())
	for _, kv := range spans[0].Attributes {
		if strings.HasPrefix(string(kv.Key), "elida.semantic.") {
			t.Fatalf("unexpected span attribute %q", kv.Key)
		}
	}
	for _, rec := range fl.records {
		for k := range logKeys(rec) {
			if strings.HasPrefix(k, "elida.semantic.") {
				t.Fatalf("unexpected log attribute %q", k)
			}
		}
	}
}

// KEEP. The OCSF finding for a plain violation carries no semantic block.
func TestBaselineC1_PlainOCSFFindingHasNoSemanticBlock(t *testing.T) {
	rec := plainRecord()
	raw, err := json.Marshal(BuildPolicyDetection(rec.SessionID, rec.Violations[0], rec))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "elida.semantic") || strings.Contains(string(raw), "elida.event_id") {
		t.Fatalf("a plain finding must not carry semantic fields: %s", raw)
	}
}
