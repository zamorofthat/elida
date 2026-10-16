package proxy

import (
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/policy"
	"elida/internal/router"
	"elida/internal/session"
)

// Final review M-1: the detached async scan of an OLD instance of a reused
// ID runs while a NEW instance under the same ID is active. Liveness is
// keyed by instance (start time), not by ID, so the old instance's tool
// calls take the uncorrelated branch: nothing is queued on the new
// instance's tool state, and the new instance's own first call completes no
// match with the old instance's evidence.
func TestAsyncScanResponse_OldInstanceOfReusedIDIsNotCorrelated(t *testing.T) {
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
	const id = "sess-reuse"

	old := manager.GetOrCreate(id, "b", "127.0.0.1")
	oldSnap := old.Snapshot()
	old.SetState(session.Completed) // ended without a kill: the ID may be reused
	e.ReleaseFlaggedSession(id)
	time.Sleep(time.Millisecond) // the two instances must not share a start time
	fresh := manager.GetOrCreate(id, "b", "127.0.0.1")
	if fresh == old || !fresh.IsActive() || fresh.StartTime.Equal(oldSnap.StartTime) {
		t.Fatal("fixture: want a new active instance with its own start time")
	}

	// The old instance's detached scan runs now, with the new instance live.
	p.asyncScanResponse(id, &oldSnap, backend, []string{body}, 200)
	flushEngine(t, e)
	st, _ := e.Stats()["correlation"].(policy.CorrelationStats)
	if st.ToolBatchesQueued != 0 || st.EventsObserved != 0 {
		t.Fatalf("the old instance's late scan must not be correlated into the new instance: %+v", st)
	}

	// The new instance's own first credential call: one event, no match.
	freshSnap := fresh.Snapshot()
	p.asyncScanResponse(id, &freshSnap, backend, []string{body}, 200)
	flushEngine(t, e)
	st, _ = e.Stats()["correlation"].(policy.CorrelationStats)
	if st.ToolBatchesQueued != 1 || st.EventsObserved != 1 {
		t.Fatalf("the new instance's own call must be correlated: %+v", st)
	}
	if st.Matches != 0 || e.GetFlaggedSession(id) != nil {
		t.Fatalf("the new instance matched on the old instance's evidence: matches=%d flagged=%v", st.Matches, e.GetFlaggedSession(id) != nil)
	}
}
