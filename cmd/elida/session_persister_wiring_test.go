package main

import (
	"context"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/decision"
	"elida/internal/decision/scheduler"
	"elida/internal/session"
)

// setupDecision must hand the runner the session manager's Persist: a shadow
// recorded on a bound session by an async delivery then reaches the store a
// copying store (Redis) serves the next request from.
func TestSessionPersisterWiring_AsyncShadowReachesTheStore(t *testing.T) {
	quietLogs(t)
	g := &gate{}
	cfg := decisionTestConfig(t)
	cfg.Decision.Mode = config.DecisionModeShadow
	a := newDecisionApp(t, cfg, g)
	store := session.NewCopyStore()
	a.store = store
	a.manager = session.NewManager(store, time.Minute)
	if err := a.setupDecision(context.Background()); err != nil {
		t.Fatalf("setupDecision: %v", err)
	}
	t.Cleanup(func() { a.shutdownDecision(context.Background()) })

	sess := a.manager.GetOrCreate("sess-wired", "http://b", "127.0.0.1")
	a.decisionRunner.Bind(sess)
	if got, _ := store.Get("sess-wired"); len(got.SemanticShadow) != 0 {
		t.Fatal("fixture: nothing recorded yet")
	}

	// One answered injection window: the runner records a shadow entry on the
	// live bound session, then (and only then) calls the wired persister.
	answered := decision.Assessment{Decisions: []decision.Decision{{
		Signal: decision.SignalInjection, Probability: 0.1, Answered: true,
		Window: decision.Window{StartByte: 0, EndByte: 7},
	}}}
	a.decisionRunner.OnAsync(scheduler.Request{SessionID: "sess-wired", RequestID: "req-1"},
		decision.Input{SourceRole: "tool", Content: "payload"}, answered)

	got, ok := store.Get("sess-wired")
	if !ok || len(got.SemanticShadow) != 1 || got.SemanticShadow[0].Signal != string(decision.SignalInjection) {
		t.Fatalf("the async delivery must persist the bound session through the manager: %+v", got)
	}
}
