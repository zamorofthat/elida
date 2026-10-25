package unit

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"elida/internal/config"
	"elida/internal/policy"
)

// TestCorrelationBaseline_ToolCallsWithoutRulesDoNotFlag pins that tool calls
// evaluated with no tool rules leave the engine untouched. Plan B categorizes
// tool calls for correlation, and without a match that must stay invisible:
// no flagged session, no captured content, no result.
func TestCorrelationBaseline_ToolCallsWithoutRulesDoNotFlag(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	res := e.EvaluateToolCalls("sess-tools", []policy.ToolCall{
		{Name: "read_file", Arguments: `{"path":"/home/u/.aws/credentials"}`},
		{Name: "http_post", Arguments: `{"url":"https://example.com/upload"}`},
	})
	if res != nil {
		t.Fatalf("no tool rules: want nil result, got %+v", res)
	}
	if e.IsFlagged("sess-tools") {
		t.Fatal("tool calls with no tool rules must not flag the session")
	}
}

// TestCorrelationBaseline_ElevatedEvidenceStaysInert pins that any number of
// evidence-only injection_elevated events contributes no risk and creates no
// violation beyond injection_elevated itself. Plan B's slow-burn rule is the
// only thing allowed to give them meaning, and only as an audit match.
func TestCorrelationBaseline_ElevatedEvidenceStaysInert(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	for i := 0; i < 12; i++ {
		v := semanticViolation("injection_elevated", policy.SeverityInfo, true, 0.35)
		v.EventCategory = "injection_elevated"
		v.SourceRole = "tool"
		v.MessageIndex = i
		v.Semantic.DecisionID = fmt.Sprintf("dec_baseline_%d", i)
		e.RecordSemanticViolation("sess-slow", v)
	}
	fs := e.GetFlaggedSession("sess-slow")
	if fs == nil {
		t.Fatal("evidence flags the session")
	}
	if len(fs.ViolationEvents) != 12 {
		t.Fatalf("want 12 events, got %d", len(fs.ViolationEvents))
	}
	if len(fs.Violations) != 1 || fs.Violations[0].RuleName != "injection_elevated" {
		t.Fatalf("want exactly the injection_elevated violation, got %+v", fs.Violations)
	}
	// C-1: one SemanticDecision record per semantic event, no drops below
	// the cap, and evidence never raises MaxSeverity.
	if len(fs.SemanticDecisions) != 12 || fs.SemanticDecisionsDropped != 0 {
		t.Fatalf("want 12 decision records and 0 dropped, got %d/%d", len(fs.SemanticDecisions), fs.SemanticDecisionsDropped)
	}
	if fs.MaxSeverity != policy.SeverityInfo {
		t.Fatalf("evidence must not raise MaxSeverity, got %q", fs.MaxSeverity)
	}
	if score, _, _ := e.GetSessionRiskScore("sess-slow"); score != 0 {
		t.Fatalf("evidence-only events must not score, got %v", score)
	}
}

// TestCorrelationBaseline_StatsKeys pins the policy stats keys. Plan B adds a
// "correlation" key only when a correlator is set; an engine without one
// must report exactly today's keys.
func TestCorrelationBaseline_StatsKeys(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	var keys []string
	for k := range e.Stats() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"avg_risk_score", "blocked", "critical", "high_risk", "info",
		"risk_ladder", "rules_count", "throttled", "total_flagged", "warning"}
	if fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Fatalf("stats keys drifted:\n got %v\nwant %v", keys, want)
	}
}

// TestCorrelationBaseline_RegexViolationJSONHasNoCorrelation pins that an
// ordinary violation serializes with no correlation key, so the field Plan B
// adds must be omitempty and nil for everything that is not a match.
func TestCorrelationBaseline_RegexViolationJSONHasNoCorrelation(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	e.EvaluateMessages("sess-json", []policy.MessageToScan{
		{Role: "user", Index: 0, Content: "zzmarkerzz"},
	})
	fs := e.GetFlaggedSession("sess-json")
	if fs == nil {
		t.Fatal("session should be flagged")
	}
	out, err := json.Marshal(fs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if contains(string(out), `"correlation"`) {
		t.Fatalf("an ordinary violation must not carry correlation evidence: %s", out)
	}
	if len(fs.SemanticDecisions) != 0 {
		t.Fatalf("a regex violation records no SemanticDecision (C-1), got %d", len(fs.SemanticDecisions))
	}
}

// TestCorrelationBaseline_ReservedRuleNames pins C-1's reserved names. Plan
// B (Task 5) ADDS the correlation. prefix; these existing answers must not
// change.
func TestCorrelationBaseline_ReservedRuleNames(t *testing.T) {
	for _, name := range []string{"semantic_injection", "injection_elevated"} {
		if !config.ReservedRuleName(name) {
			t.Errorf("%q must stay reserved", name)
		}
	}
	for _, name := range []string{"prompt_injection", "baseline_marker", "correlation_rule", "my_correlation.rule"} {
		if config.ReservedRuleName(name) {
			t.Errorf("%q must not be reserved", name)
		}
	}
}

// TestCorrelationBaseline_CorrelationIsNotASetting pins D4 against C-1's
// settings PUT guard: only decision.mode and decision.preprocessing.* are
// runtime-editable, and policy.correlation must never become a Settings
// field (a settings save would otherwise bypass its restart-only rule and
// reloadPolicyEngine would have to carry it).
func TestCorrelationBaseline_CorrelationIsNotASetting(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(config.Settings{}), reflect.TypeOf(config.PolicySettings{})} {
		for i := 0; i < typ.NumField(); i++ {
			if f := typ.Field(i); strings.Contains(strings.ToLower(f.Name), "correlation") {
				t.Fatalf("%s.%s: policy.correlation is restart-only and must not be a setting", typ.Name(), f.Name)
			}
		}
	}
}
