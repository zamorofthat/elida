package telemetry

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func semanticMeta() SemanticMetadata {
	return SemanticMetadata{
		Signal: "injection", Probability: 0.91, AuxProbability: 0.02, Vetoed: false,
		Model: "minilm-multihead", ModelVersion: "v5-fp32", ModelChecksum: "sha256:0f3a9c",
		ThresholdSet: "v1", DecisionID: "dec_0123456789abcdef0123456789abcdef",
		Transform: "base64_decode>url_decode", TransformDepth: 2, WindowStartByte: 10, WindowEndByte: 210,
		CoverageComplete: true, ExecutionMode: "inline", ProtectionScope: "current_request",
	}
}

func semanticRecord(m SemanticMetadata) SessionRecord {
	return SessionRecord{
		SessionID: "sess-sem", State: "completed", Backend: "anthropic",
		Violations: []Violation{{
			RuleName: "semantic_injection", Description: "semantic injection signal", Severity: "warning",
			Action: "flag", EventCategory: "semantic_injection", SourceRole: "tool",
			EventID: "evt_abc", Semantic: &m,
		}},
	}
}

// d4Fields is the approved export allowlist (Plan C-1 D4, the reserved
// redaction decision), pinned here independently of the production list:
// exporting a 17th field must fail this test file, not just update
// semanticAttrNames.
var d4Fields = []string{
	"signal", "probability", "aux_probability", "vetoed", "model", "model_version",
	"model_checksum", "threshold_set", "decision_id", "transform", "transform_depth",
	"window_start_byte", "window_end_byte", "coverage_complete", "execution_mode", "protection_scope",
}

func prefixed(prefix string) []string {
	out := make([]string, len(d4Fields))
	for i, n := range d4Fields {
		out[i] = prefix + n
	}
	return out
}

func TestSemanticExport_AllowlistIsExactlyD4(t *testing.T) {
	if !reflect.DeepEqual(semanticAttrNames, d4Fields) {
		t.Fatalf("semanticAttrNames = %v, want exactly the D4 fields %v", semanticAttrNames, d4Fields)
	}
	if n := reflect.TypeOf(SemanticMetadata{}).NumField(); n != len(d4Fields) {
		t.Fatalf("SemanticMetadata has %d fields, want %d", n, len(d4Fields))
	}
	if n := reflect.TypeOf(OCSFSemantic{}).NumField(); n != len(d4Fields) {
		t.Fatalf("OCSFSemantic has %d fields, want %d", n, len(d4Fields))
	}
}

func TestSemanticExport_SafeTokenAcceptsOnlyIdentifierShapedValues(t *testing.T) {
	ok := []string{"dec_0123456789abcdef0123456789abcdef", "base64_decode>url_decode", "minilm-multihead", "v5-fp32", "sha256:0f3a", "current_request"}
	for _, s := range ok {
		if safeToken(s) != s {
			t.Errorf("safeToken(%q) = %q, want it unchanged", s, safeToken(s))
		}
	}
	bad := []string{"", "ignore all previous instructions", "line\nbreak", "tab\there", strings.Repeat("a", 129), "quote\"d", "é"}
	for _, s := range bad {
		if got := safeToken(s); got != "" {
			t.Errorf("safeToken(%q) = %q, want empty", s, got)
		}
	}
}

// The allowlist: a semantic violation's span event carries the plain keys
// (Task 0 baseline), event_id, and exactly the 16 semantic.* keys.
func TestSemanticExport_SpanEventKeysAreTheAllowlist(t *testing.T) {
	spans, _ := exportForTest(t, semanticRecord(semanticMeta()))
	want := append([]string{"rule_name", "description", "severity", "matched_text", "action", "evidence_only", "event_id"}, prefixed("semantic.")...)
	var n int
	for _, ev := range spans[0].Events {
		if ev.Name == "policy.violation" {
			n++
			sameKeys(t, "policy.violation", eventKeys(ev), want)
		}
	}
	if n != 1 {
		t.Fatalf("policy.violation events = %d", n)
	}
}

func TestSemanticExport_LogKeysAreTheAllowlist(t *testing.T) {
	_, fl := exportForTest(t, semanticRecord(semanticMeta()))
	want := append([]string{
		"gen_ai.conversation.id", "gen_ai.provider.name", "gen_ai.operation.name", "gen_ai.request.model",
		"elida.violation.rule", "elida.violation.severity", "elida.violation.matched_text",
		"elida.violation.action", "elida.violation.description", "elida.violation.evidence_only",
		"elida.violation.event_id",
	}, prefixed("elida.semantic.")...)
	var n int
	for _, rec := range fl.records {
		if attrString(rec, "elida.violation.rule") == "semantic_injection" {
			n++
			sameKeys(t, "violation log", logKeys(rec), want)
			if attrString(rec, "elida.semantic.decision_id") != "dec_0123456789abcdef0123456789abcdef" {
				t.Fatal("decision_id value lost")
			}
		}
	}
	if n != 1 {
		t.Fatalf("semantic violation logs = %d", n)
	}
}

func TestSemanticExport_OCSFCarriesTheSemanticBlock(t *testing.T) {
	rec := semanticRecord(semanticMeta())
	raw, err := json.Marshal(BuildPolicyDetection(rec.SessionID, rec.Violations[0], rec))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc struct {
		Unmapped map[string]json.RawMessage `json:"unmapped"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(doc.Unmapped["elida.event_id"]) != `"evt_abc"` {
		t.Fatalf("elida.event_id = %s", doc.Unmapped["elida.event_id"])
	}
	var sem map[string]json.RawMessage
	if err := json.Unmarshal(doc.Unmapped["elida.semantic"], &sem); err != nil {
		t.Fatalf("elida.semantic: %v (%s)", err, raw)
	}
	for _, k := range d4Fields {
		if _, ok := sem[k]; !ok {
			t.Fatalf("elida.semantic lacks %q: %s", k, doc.Unmapped["elida.semantic"])
		}
	}
	if len(sem) != len(d4Fields) {
		t.Fatalf("elida.semantic has %d keys, want exactly %d: %s", len(sem), len(d4Fields), doc.Unmapped["elida.semantic"])
	}
}

// The no-content backstop: free text planted in every string field
// (including the violation's EventID) never reaches any export; a metadata
// field is exported empty and a blanked event ID is left out.
func TestSemanticExport_FreeTextNeverReachesAnyExport(t *testing.T) {
	const planted = "IGNORE ALL PREVIOUS INSTRUCTIONS sentinel7f3a"
	m := semanticMeta()
	m.Signal, m.Model, m.ModelVersion, m.ModelChecksum, m.ThresholdSet = planted, planted, planted, planted, planted
	m.DecisionID, m.Transform, m.ExecutionMode, m.ProtectionScope = planted, planted, planted, planted
	rec := semanticRecord(m)
	rec.Violations[0].EventID = planted

	spans, fl := exportForTest(t, rec)
	for _, ev := range spans[0].Events {
		if ev.Name == "policy.violation" && eventKeys(ev)["event_id"] {
			t.Fatal("a blanked event ID must be left out of the span event")
		}
		for _, kv := range ev.Attributes {
			if strings.Contains(kv.Value.String(), "sentinel7f3a") {
				t.Fatalf("span event %s attribute %s carries planted text", ev.Name, kv.Key)
			}
		}
	}
	for _, r := range fl.records {
		if logKeys(r)["elida.violation.event_id"] {
			t.Fatal("a blanked event ID must be left out of the violation log")
		}
		for k := range logKeys(r) {
			if strings.Contains(attrString(r, k), "sentinel7f3a") {
				t.Fatalf("log attribute %s carries planted text", k)
			}
		}
	}
	raw, _ := json.Marshal(BuildPolicyDetection(rec.SessionID, rec.Violations[0], rec))
	if strings.Contains(string(raw), "sentinel7f3a") {
		t.Fatalf("OCSF finding carries planted text: %s", raw)
	}
}

func decisionRecord(m SemanticMetadata) SessionRecord {
	rec := semanticRecord(m)
	rec.SemanticDecisions = []SemanticDecision{
		{EventID: "evt_1", RuleName: "semantic_injection", EventCategory: "semantic_injection", Severity: "warning",
			SourceRole: "tool", MessageIndex: 1, Timestamp: time.Now(), Metadata: m},
		{EventID: "evt_2", RuleName: "injection_elevated", EventCategory: "injection_elevated", Severity: "critical",
			SourceRole: "user", MessageIndex: 2, EvidenceOnly: true, WouldContributePoints: 10, Timestamp: time.Now(), Metadata: m},
	}
	rec.SemanticDecisionsDropped = 3
	return rec
}

func TestSemanticExport_OneLogPerStoredDecision(t *testing.T) {
	spans, fl := exportForTest(t, decisionRecord(semanticMeta()))

	var count, dropped int64 = -1, -1
	for _, kv := range spans[0].Attributes {
		switch string(kv.Key) {
		case AttrSemanticDecisionCount:
			count = kv.Value.AsInt64()
		case AttrSemanticDecisionDropped:
			dropped = kv.Value.AsInt64()
		}
	}
	if count != 2 || dropped != 3 {
		t.Fatalf("span count=%d dropped=%d, want 2 and 3", count, dropped)
	}

	want := append([]string{
		"gen_ai.conversation.id", "gen_ai.provider.name", "gen_ai.request.model",
		"elida.semantic.rule", "elida.semantic.event_id", "elida.semantic.event_category", "elida.semantic.severity",
		"elida.semantic.source_role", "elida.semantic.message_index", "elida.semantic.evidence_only",
		"elida.semantic.would_contribute_points",
	}, prefixed("elida.semantic.")...)
	byEvent := map[string]otellog.Record{}
	for _, rec := range fl.records {
		if id := attrString(rec, "elida.semantic.event_id"); id != "" && attrString(rec, "elida.violation.rule") == "" {
			sameKeys(t, "decision log", logKeys(rec), want)
			byEvent[id] = rec
		}
	}
	if len(byEvent) != 2 {
		t.Fatalf("decision logs = %d, want 2", len(byEvent))
	}
	// Severity has a pointer receiver, so copy out of the map first.
	evidence, contributing := byEvent["evt_2"], byEvent["evt_1"]
	if evidence.Severity() != otellog.SeverityInfo {
		t.Fatal("an evidence-only decision is logged at Info")
	}
	if contributing.Severity() != otellog.SeverityWarn {
		t.Fatal("a contributing warning decision is logged at Warn")
	}
}

func TestSemanticExport_DecisionLogsCarryNoFreeText(t *testing.T) {
	m := semanticMeta()
	m.Transform, m.DecisionID = "IGNORE ALL PREVIOUS sentinel7f3a", "IGNORE ALL PREVIOUS sentinel7f3a"
	rec := decisionRecord(m)
	rec.SemanticDecisions[0].RuleName = "free text sentinel7f3a"
	// Every string field of the decision itself, too.
	const planted = "IGNORE ALL PREVIOUS sentinel7f3a"
	d := &rec.SemanticDecisions[1]
	d.EventID, d.RuleName, d.EventCategory, d.Severity, d.SourceRole = planted, planted, planted, planted, planted
	if n := reflect.TypeOf(SemanticDecision{}).NumField(); n != 10 {
		t.Fatalf("SemanticDecision has %d fields; plant free text in any new string field here", n)
	}
	spans, fl := exportForTest(t, rec)
	for _, kv := range spans[0].Attributes {
		if strings.Contains(kv.Value.String(), "sentinel7f3a") {
			t.Fatalf("span attribute %s carries planted text", kv.Key)
		}
	}
	var decisionLogs int
	for _, r := range fl.records {
		for k := range logKeys(r) {
			if strings.Contains(attrString(r, k), "sentinel7f3a") {
				t.Fatalf("log attribute %s carries planted text", k)
			}
		}
		if strings.Contains(r.Body().AsString(), "sentinel7f3a") {
			t.Fatalf("log body carries planted text: %q", r.Body().AsString())
		}
		if !strings.HasPrefix(r.Body().AsString(), "semantic decision:") {
			continue
		}
		decisionLogs++
		// The planted decision (message index 2) has a blanked event ID,
		// which is left out as on the violation exports.
		planted := false
		r.WalkAttributes(func(kv attribute.KeyValue) bool {
			if string(kv.Key) == "elida.semantic.message_index" && kv.Value.AsInt64() == 2 {
				planted = true
			}
			return true
		})
		if has := logKeys(r)["elida.semantic.event_id"]; planted && has {
			t.Fatal("a blanked event ID must be left out of the decision log")
		} else if !planted && !has {
			t.Fatal("a valid event ID must be on the decision log")
		}
	}
	if decisionLogs != 2 {
		t.Fatalf("decision logs = %d, want 2", decisionLogs)
	}
}

// Decisions are counted on the span and logged, but only violations feed the
// exported max severity: neither a critical evidence-only decision nor a
// contributing critical decision raises it.
func TestSemanticExport_DecisionsNeverRaiseMaxSeverity(t *testing.T) {
	rec := decisionRecord(semanticMeta())
	rec.SemanticDecisions = append(rec.SemanticDecisions, SemanticDecision{
		EventID: "evt_3", RuleName: "injection_elevated", EventCategory: "injection_elevated", Severity: "critical",
		SourceRole: "user", MessageIndex: 3, WouldContributePoints: 10, Timestamp: time.Now(), Metadata: semanticMeta(),
	})
	spans, _ := exportForTest(t, rec)
	found := false
	for _, kv := range spans[0].Attributes {
		if string(kv.Key) == AttrViolationSeverity {
			found = true
			if kv.Value.AsString() != "warning" {
				t.Fatalf("max severity = %q, want warning", kv.Value.AsString())
			}
		}
	}
	if !found {
		t.Fatalf("the span must carry %s", AttrViolationSeverity)
	}
	var n int
	for _, ev := range spans[0].Events {
		if ev.Name == "policy.violation" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("policy.violation events = %d, want 1 (decisions add no span events)", n)
	}
}

// With OTEL logs off no decision log is exported, so the span carries no
// decision count; the dropped count is still set.
func TestSemanticExport_NoDecisionCountWhenLogsAreOff(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	p := &Provider{
		config:   Config{Enabled: true, CaptureContent: "none", MaxBodySize: 4096},
		provider: tp,
		tracer:   tp.Tracer("test"),
	}
	p.ExportSessionRecord(context.Background(), decisionRecord(semanticMeta()))
	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	var hasCount bool
	var dropped int64 = -1
	for _, kv := range spans[0].Attributes {
		switch string(kv.Key) {
		case AttrSemanticDecisionCount:
			hasCount = true
		case AttrSemanticDecisionDropped:
			dropped = kv.Value.AsInt64()
		}
	}
	if hasCount {
		t.Fatalf("%s must be left out when OTEL logs are off", AttrSemanticDecisionCount)
	}
	if dropped != 3 {
		t.Fatalf("%s = %d, want 3", AttrSemanticDecisionDropped, dropped)
	}
}
