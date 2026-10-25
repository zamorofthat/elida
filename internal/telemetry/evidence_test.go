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

// evidenceRecord holds one critical evidence-only violation (semantic audit
// mode) and one ordinary warning.
func evidenceRecord() SessionRecord {
	return SessionRecord{
		SessionID: "sess-evidence",
		State:     "completed",
		Backend:   "anthropic",
		Violations: []Violation{
			{RuleName: "semantic_injection", Severity: "critical", Action: "flag", EventCategory: "semantic_injection", EvidenceOnly: true},
			{RuleName: "baseline_marker", Severity: "warning", Action: "flag"},
		},
	}
}

func attrBool(rec otellog.Record, key string) (val, ok bool) {
	rec.WalkAttributes(func(kv attribute.KeyValue) bool {
		if string(kv.Key) == key {
			val, ok = kv.Value.AsBool(), true
			return false
		}
		return true
	})
	return val, ok
}

// Through the real export path: the session span's max severity ignores
// evidence, every violation event and log carries evidence_only, and an
// evidence log is informational.
func TestExportSessionRecord_EvidenceOnlyIsMarkedAndExcludedFromMaxSeverity(t *testing.T) {
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

	p.ExportSessionRecord(context.Background(), evidenceRecord())

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	var maxSev string
	for _, kv := range spans[0].Attributes {
		if string(kv.Key) == AttrViolationSeverity {
			maxSev = kv.Value.AsString()
		}
	}
	if maxSev != "warning" {
		t.Fatalf("%s = %q, want warning: a critical evidence-only violation must not raise it", AttrViolationSeverity, maxSev)
	}
	evidence := map[string]bool{}
	for _, ev := range spans[0].Events {
		if ev.Name != "policy.violation" {
			continue
		}
		var rule string
		var flag, has bool
		for _, kv := range ev.Attributes {
			switch string(kv.Key) {
			case "rule_name":
				rule = kv.Value.AsString()
			case "evidence_only":
				flag, has = kv.Value.AsBool(), true
			}
		}
		if !has {
			t.Fatalf("policy.violation event for %s has no evidence_only attribute", rule)
		}
		evidence[rule] = flag
	}
	if !evidence["semantic_injection"] || evidence["baseline_marker"] {
		t.Fatalf("span evidence_only = %v", evidence)
	}

	var logs int
	for _, rec := range fl.records {
		rule := attrString(rec, "elida.violation.rule")
		if rule == "" {
			continue
		}
		logs++
		flag, ok := attrBool(rec, "elida.violation.evidence_only")
		if !ok {
			t.Fatalf("violation log for %s has no elida.violation.evidence_only", rule)
		}
		switch rule {
		case "semantic_injection":
			if !flag || rec.Severity() != otellog.SeverityInfo {
				t.Fatalf("evidence log: evidence_only=%v severity=%v, want true/info", flag, rec.Severity())
			}
			if attrString(rec, "elida.violation.severity") != "critical" {
				t.Fatal("the rule's own severity stays visible as an attribute")
			}
		case "baseline_marker":
			if flag || rec.Severity() != otellog.SeverityWarn {
				t.Fatalf("ordinary log: evidence_only=%v severity=%v, want false/warn", flag, rec.Severity())
			}
		}
	}
	if logs != 2 {
		t.Fatalf("violation logs = %d, want 2", logs)
	}
}

func TestBuildPolicyDetection_EvidenceOnlyIsInformationalAndMarked(t *testing.T) {
	rec := evidenceRecord()

	ev := BuildPolicyDetection(rec.SessionID, rec.Violations[0], rec)
	if ev.SeverityID != OCSFSeverityInfo {
		t.Fatalf("evidence severity_id = %d, want %d (Informational)", ev.SeverityID, OCSFSeverityInfo)
	}
	if !ev.Unmapped.EvidenceOnly || !strings.Contains(ev.Message, "evidence") {
		t.Fatalf("evidence finding unmapped=%+v message=%q", ev.Unmapped, ev.Message)
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"elida.evidence_only":true`) {
		t.Fatalf("OCSF JSON lacks elida.evidence_only: %s", raw)
	}

	ord := BuildPolicyDetection(rec.SessionID, rec.Violations[1], rec)
	if ord.SeverityID != OCSFSeverityWarning || ord.Unmapped.EvidenceOnly {
		t.Fatalf("ordinary finding severity_id=%d evidence=%v", ord.SeverityID, ord.Unmapped.EvidenceOnly)
	}
	raw, _ = json.Marshal(ord)
	if strings.Contains(string(raw), "elida.evidence_only") {
		t.Fatalf("an ordinary finding must not carry the evidence flag: %s", raw)
	}
}
