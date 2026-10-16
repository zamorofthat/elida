package proxy

import (
	"context"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/correlation"
	"elida/internal/policy"
	"elida/internal/router"
	"elida/internal/session"
)

// The tool-call ID is what lets correlation identify one provider call, so
// the converter must carry it.
func TestProxyToolCallsToPolicyToolCalls_CarriesID(t *testing.T) {
	got := proxyToolCallsToPolicyToolCalls([]ToolCallInfo{
		{Name: "read_file", Type: "function", ID: "call_1", Arguments: `{"path":"x"}`},
	})
	if len(got) != 1 || got[0].ID != "call_1" || got[0].Name != "read_file" || got[0].Arguments != `{"path":"x"}` {
		t.Fatalf("converter dropped a field: %+v", got)
	}
}

func credTwiceEngine(t *testing.T) *policy.Engine {
	t.Helper()
	return policy.NewEngine(policy.Config{
		Enabled: true, Mode: "enforce",
		Correlation: policy.CorrelationConfig{
			Enabled: true,
			Rules: []correlation.Rule{{
				Name: "cred_twice", Shape: correlation.ShapeRepeated,
				Category: "credential_discovery", Count: 2, Window: time.Minute,
			}},
			Limits: correlation.Limits{MaxHistoryEvents: 64, MaxSessions: 64},
			TaxonomyConfig: correlation.TaxonomyConfig{ToolCategories: []correlation.ToolCategoryRule{
				{Category: "credential_discovery", Tools: []string{"read_file", "bash"}, ArgumentPatterns: []string{`\.aws/credentials`, `id_rsa`}},
			}},
		},
	})
}

func flushEngine(t *testing.T, e *policy.Engine) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.FlushToolCalls(ctx); err != nil {
		t.Fatal(err)
	}
}

// Review I-2: the detached async scan does not feed tool calls for a
// session that is no longer active (it may already be persisted and
// released); an active session's calls still reach the lane.
func TestAsyncScanResponse_SkipsToolCallsOfEndedSessions(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	e := credTwiceEngine(t)
	p, err := New(&config.Config{
		Backend: "http://127.0.0.1:1",
		Session: config.SessionConfig{Header: "X-Session-ID", GenerateIfMissing: true, Timeout: 5 * time.Minute},
	}, store, manager, WithPolicyEngine(e))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body := `{"choices":[{"message":{"tool_calls":[{"id":"call_a","type":"function","function":{"name":"bash","arguments":"cat id_rsa"}}]}}]}`
	backend := &router.Backend{Name: "b"}

	live := manager.GetOrCreate("sess-live", "b", "127.0.0.1")
	snap := live.Snapshot()
	p.asyncScanResponse("sess-live", &snap, backend, []string{body}, 200)

	ended := manager.GetOrCreate("sess-ended", "b", "127.0.0.1")
	manager.Kill("sess-ended")
	snap2 := ended.Snapshot()
	p.asyncScanResponse("sess-ended", &snap2, backend, []string{body}, 200)
	p.asyncScanResponse("sess-unknown", &snap2, backend, []string{body}, 200)
	flushEngine(t, e)

	st, _ := e.Stats()["correlation"].(policy.CorrelationStats)
	if st.ToolBatchesQueued != 1 || st.EventsObserved != 1 {
		t.Fatalf("only the live session's tool calls may be queued: %+v", st)
	}
}
