package unit

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"elida/internal/control"
	"elida/internal/correlation"
	"elida/internal/policy"
	"elida/internal/session"
)

// Stricter companions of the Task 0 baselines (correlation_baseline_test.go
// is kept unedited). Task 0 review L1-L3.

// L1: the control API responses themselves, not just the policy struct,
// carry no "correlation" key for an ordinary regex violation, so a key
// added only in a handler would trip this.
func TestCorrelationBaselineStrict_APIResponsesHaveNoCorrelationKey(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	manager.GetOrCreate("sess-json-api", "https://backend", "127.0.0.1:1")
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	e.EvaluateMessages("sess-json-api", []policy.MessageToScan{
		{Role: "user", Index: 0, Content: "zzmarkerzz"},
	})
	h := control.New(store, manager, control.WithPolicy(e))
	for _, tc := range []struct {
		path           string
		carriesFinding bool
	}{
		{"/control/sessions/sess-json-api", false}, // risk only, no violation list
		{"/control/flagged/sess-json-api", true},
		{"/control/flagged", true},
	} {
		path := tc.path
		w := doJSON(t, h, http.MethodGet, path, nil)
		if w.Code != http.StatusOK || (tc.carriesFinding && !contains(w.Body.String(), "baseline_marker")) {
			t.Fatalf("%s: want 200 (carrying the regex violation where listed), got %d %s", path, w.Code, w.Body.String())
		}
		if contains(w.Body.String(), `"correlation"`) {
			t.Fatalf("%s: an ordinary violation must not carry a correlation key: %s", path, w.Body.String())
		}
	}
}

// L2: evidence of the highest severity still never raises MaxSeverity (the
// Task 0 pin used Info evidence, the floor, so it could not show this).
func TestCorrelationBaselineStrict_CriticalEvidenceDoesNotRaiseMaxSeverity(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	for i := 0; i < 3; i++ {
		v := semanticViolation("injection_elevated", policy.SeverityCritical, true, 0.35)
		v.EventCategory = "injection_elevated"
		v.SourceRole = "user"
		v.MessageIndex = i
		v.Semantic.DecisionID = fmt.Sprintf("dec_crit_%d", i)
		e.RecordSemanticViolation("sess-crit-evidence", v)
	}
	fs := e.GetFlaggedSession("sess-crit-evidence")
	if fs == nil || len(fs.ViolationEvents) != 3 {
		t.Fatalf("fixture: the evidence must be recorded: %+v", fs)
	}
	if fs.MaxSeverity != policy.SeverityInfo {
		t.Fatalf("critical evidence-only events must not raise MaxSeverity, got %q", fs.MaxSeverity)
	}
	if score, _, _ := e.GetSessionRiskScore("sess-crit-evidence"); score != 0 {
		t.Fatalf("evidence-only events must not score, got %v", score)
	}
}

// L3: tool calls that a correlation taxonomy categorizes, without a match
// and without tool rules, leave no flagged session and no captured content,
// while the calls do reach the correlator.
func TestCorrelationBaselineStrict_CategorizedToolCallsCaptureNothing(t *testing.T) {
	e := policy.NewEngine(policy.Config{
		Enabled: true, Mode: "enforce", Rules: baselineRules(),
		Correlation: policy.CorrelationConfig{
			Enabled: true,
			Rules: []correlation.Rule{{
				Name: "cred_thrice", Shape: correlation.ShapeRepeated,
				Category: "credential_discovery", Count: 3, Window: time.Minute,
			}},
			Limits: correlation.Limits{MaxHistoryEvents: 64, MaxSessions: 64},
			TaxonomyConfig: correlation.TaxonomyConfig{ToolCategories: []correlation.ToolCategoryRule{
				{Category: "credential_discovery", Tools: []string{"read_file"}, ArgumentPatterns: []string{`\.aws/credentials`}},
			}},
		},
	})
	res := e.EvaluateToolCalls("sess-tools-strict", []policy.ToolCall{
		{Name: "read_file", Arguments: `{"path":"/home/u/.aws/credentials"}`},
		{Name: "http_post", Arguments: `{"url":"https://example.com/upload"}`},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.FlushToolCalls(ctx); err != nil {
		t.Fatal(err)
	}
	if res != nil {
		t.Fatalf("no tool rules: want nil result, got %+v", res)
	}
	if e.IsFlagged("sess-tools-strict") || e.GetFlaggedSession("sess-tools-strict") != nil {
		t.Fatal("categorized tool calls without a match must not flag the session")
	}
	if all := e.GetFlaggedSessions(); len(all) != 0 {
		t.Fatalf("no flagged session, so no captured content anywhere: %+v", all)
	}
	st, _ := e.Stats()["correlation"].(policy.CorrelationStats)
	if st.EventsObserved != 1 || st.Matches != 0 {
		t.Fatalf("fixture: the credential read must reach the correlator without matching: %+v", st)
	}
}
