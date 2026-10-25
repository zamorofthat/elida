package proxy

import (
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/correlation"
	"elida/internal/policy"
	"elida/internal/router"
	"elida/internal/session"
)

// Task 7 review N-2 (regression): a session that ended while its async
// response scan ran keeps its tool-RULE violations (flagged, as before the
// liveness check), while only the temporal-correlation step is skipped.
func TestAsyncScanResponse_EndedSessionKeepsToolRuleViolations(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	e := policy.NewEngine(policy.Config{
		Enabled: true, Mode: "enforce",
		Rules: []policy.Rule{{
			Name: "no_bash", Type: "tool_blocked", Patterns: []string{"bash"},
			Severity: "warning", Action: "flag",
		}},
		Correlation: policy.CorrelationConfig{
			Enabled: true,
			Rules: []correlation.Rule{{
				Name: "cred_twice", Shape: correlation.ShapeRepeated,
				Category: "credential_discovery", Count: 2, Window: time.Minute,
			}},
			Limits: correlation.Limits{MaxHistoryEvents: 64, MaxSessions: 64},
			TaxonomyConfig: correlation.TaxonomyConfig{ToolCategories: []correlation.ToolCategoryRule{
				{Category: "credential_discovery", Tools: []string{"bash"}},
			}},
		},
	})
	p, err := New(&config.Config{
		Backend: "http://127.0.0.1:1",
		Session: config.SessionConfig{Header: "X-Session-ID", GenerateIfMissing: true, Timeout: 5 * time.Minute},
	}, store, manager, WithPolicyEngine(e))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body := `{"choices":[{"message":{"tool_calls":[{"id":"call_a","type":"function","function":{"name":"bash","arguments":"ls"}}]}}]}`
	backend := &router.Backend{Name: "b"}

	ended := manager.GetOrCreate("sess-ended-rule", "b", "127.0.0.1")
	manager.Kill("sess-ended-rule")
	snap := ended.Snapshot()
	p.asyncScanResponse("sess-ended-rule", &snap, backend, []string{body}, 200)
	flushEngine(t, e)

	fs := e.GetFlaggedSession("sess-ended-rule")
	if fs == nil || len(fs.Violations) != 1 || fs.Violations[0].RuleName != "no_bash" {
		t.Fatalf("a late tool-rule violation of an ended session must still be recorded: %+v", fs)
	}
	st, _ := e.Stats()["correlation"].(policy.CorrelationStats)
	if st.ToolBatchesQueued != 0 || st.EventsObserved != 0 {
		t.Fatalf("an ended session's tool calls must skip temporal correlation: %+v", st)
	}
}
