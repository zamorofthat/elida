package runner

import (
	"sync/atomic"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/decision"
	"elida/internal/decision/preprocess"
	"elida/internal/decision/scheduler"
	"elida/internal/session"
)

func persisterRunner(t *testing.T, mode string, persist func(*session.Session)) *Runner {
	t.Helper()
	r, err := New(Config{
		Mode:             mode,
		Thresholds:       testThresholds(),
		Budget:           preprocess.Budget{MaxInputBytes: 1024, MaxAnalysisBytes: 1024, MaxRepresentations: 1, MaxDecodeDepth: 0, MaxExpansionRatio: 1},
		Model:            ModelIdentity{ThresholdSet: "test"},
		Signals:          []decision.Signal{decision.SignalInjection},
		Scheduler:        lifecycleNoopScheduler{},
		SessionPersister: persist,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func asyncDelivery(id string) (scheduler.Request, decision.Input) {
	return scheduler.Request{SessionID: id, RequestID: "req-1"},
		decision.Input{SourceRole: "tool", MessageIndex: 0, Content: "payload"}
}

// answeredAssessment is one answered injection window: handle claims its
// DecisionID and records exactly one shadow entry for it.
func answeredAssessment() decision.Assessment {
	return decision.Assessment{Decisions: []decision.Decision{{
		Signal: decision.SignalInjection, Probability: 0.1, Answered: true,
		Window: decision.Window{StartByte: 0, EndByte: 7},
	}}}
}

func countingPersister(calls *atomic.Int32, got *atomic.Pointer[session.Session]) func(*session.Session) {
	return func(s *session.Session) {
		calls.Add(1)
		if got != nil {
			got.Store(s)
		}
	}
}

func TestOnAsync_PersistsOnceWhenTheDeliveryRecordedAShadowEntry(t *testing.T) {
	var calls atomic.Int32
	var got atomic.Pointer[session.Session]
	r := persisterRunner(t, config.DecisionModeShadow, countingPersister(&calls, &got))
	sess := lifecycleSession("sess-persist")
	r.Bind(sess)

	req, in := asyncDelivery("sess-persist")
	r.OnAsync(req, in, answeredAssessment())

	if len(sess.GetSemanticShadow()) != 1 {
		t.Fatalf("fixture: want one recorded shadow entry, have %d", len(sess.GetSemanticShadow()))
	}
	if calls.Load() != 1 || got.Load() != sess {
		t.Fatalf("persister calls = %d, session match = %v; want exactly one call with the bound session", calls.Load(), got.Load() == sess)
	}
}

// The write budget is bounded by what was recorded, not by scored windows:
// a delivery that recorded nothing must not cost a Redis round trip.
func TestOnAsync_DoesNotPersistADeliveryThatRecordedNothing(t *testing.T) {
	var calls atomic.Int32
	r := persisterRunner(t, config.DecisionModeShadow, countingPersister(&calls, nil))
	sess := lifecycleSession("sess-quiet")
	r.Bind(sess)
	req, in := asyncDelivery("sess-quiet")

	r.OnAsync(req, in, decision.Assessment{}) // empty/unanswered
	unanswered := decision.Assessment{Decisions: []decision.Decision{{
		Signal: decision.SignalInjection, Probability: 0.9, Answered: false, Window: decision.Window{StartByte: 0, EndByte: 7},
	}}}
	r.OnAsync(req, in, unanswered)
	if calls.Load() != 0 || len(sess.GetSemanticShadow()) != 0 {
		t.Fatalf("unanswered deliveries: %d persists, %d shadow entries; want 0 and 0", calls.Load(), len(sess.GetSemanticShadow()))
	}

	r.OnAsync(req, in, answeredAssessment()) // records and persists
	r.OnAsync(req, in, answeredAssessment()) // same DecisionID: already claimed, records nothing
	if calls.Load() != 1 || len(sess.GetSemanticShadow()) != 1 {
		t.Fatalf("a redelivered (claimed) decision must not persist again: %d persists, %d shadow entries; want 1 and 1", calls.Load(), len(sess.GetSemanticShadow()))
	}
}

func TestOnAsync_DoesNotPersistWhenTheResultHasNowhereToLand(t *testing.T) {
	var calls atomic.Int32
	r := persisterRunner(t, config.DecisionModeShadow, countingPersister(&calls, nil))

	req, in := asyncDelivery("sess-unbound") // never bound
	r.OnAsync(req, in, answeredAssessment())
	if calls.Load() != 0 || r.AsyncDroppedNoSession() != 1 {
		t.Fatalf("an unbound session's dropped result must not persist: %d calls, %d dropped", calls.Load(), r.AsyncDroppedNoSession())
	}

	sess := lifecycleSession("sess-ended")
	r.Bind(sess)
	r.Unbind(sess)
	req, in = asyncDelivery("sess-ended")
	r.OnAsync(req, in, answeredAssessment())
	if calls.Load() != 0 {
		t.Fatalf("an ended session must not be persisted by a late result: %d calls", calls.Load())
	}
}

func TestOnAsync_DoesNotPersistInDisabledMode(t *testing.T) {
	var calls atomic.Int32
	r := persisterRunner(t, config.DecisionModeDisabled, countingPersister(&calls, nil))
	sess := lifecycleSession("sess-disabled")
	r.Bind(sess)
	req, in := asyncDelivery("sess-disabled")
	r.OnAsync(req, in, answeredAssessment())
	if calls.Load() != 0 || len(sess.GetSemanticShadow()) != 0 || r.AsyncDroppedDisabled() != 1 {
		t.Fatalf("disabled mode records and persists nothing: %d persists, %d entries, %d dropped-disabled", calls.Load(), len(sess.GetSemanticShadow()), r.AsyncDroppedDisabled())
	}
}

// With no persister the delivery records exactly what it records with one;
// only the write-back is absent.
func TestOnAsync_WithoutAPersisterRecordsTheSameState(t *testing.T) {
	var calls atomic.Int32
	with := persisterRunner(t, config.DecisionModeShadow, countingPersister(&calls, nil))
	without := persisterRunner(t, config.DecisionModeShadow, nil)

	a, b := lifecycleSession("sess-with"), lifecycleSession("sess-without")
	with.Bind(a)
	without.Bind(b)
	reqA, in := asyncDelivery("sess-with")
	reqB, _ := asyncDelivery("sess-without")
	with.OnAsync(reqA, in, answeredAssessment())
	without.OnAsync(reqB, in, answeredAssessment())

	if len(a.GetSemanticShadow()) != 1 || len(b.GetSemanticShadow()) != 1 {
		t.Fatalf("shadow entries: with=%d without=%d, want 1 and 1", len(a.GetSemanticShadow()), len(b.GetSemanticShadow()))
	}
	if without.AsyncDroppedNoSession() != 0 || with.AsyncDroppedNoSession() != 0 {
		t.Fatal("neither delivery may be dropped")
	}
	if calls.Load() != 1 {
		t.Fatalf("persister calls = %d, want 1 for the runner that has one", calls.Load())
	}
}

// Persist must leave the live session Active even when the stored lifecycle
// moved on, so Runner.Bind (which refuses non-Active sessions) still accepts
// the session on its next request and async results keep landing.
func TestBind_StillAcceptsASessionAfterPersistSawAMovedOnStoredLifecycle(t *testing.T) {
	store := session.NewCopyStore()
	m := session.NewManager(store, time.Minute)
	var calls atomic.Int32
	r := persisterRunner(t, config.DecisionModeShadow, func(s *session.Session) {
		calls.Add(1)
		m.Persist(s)
	})

	sess := m.GetOrCreate("sess-bind", "http://b", "127.0.0.1")
	m.Complete("sess-bind") // another replica ended it: stored lifecycle is Completed
	m.Persist(sess)         // the request-end write-back

	if !sess.IsActive() {
		t.Fatalf("Persist flipped the live session to %v", sess.GetState())
	}
	r.Bind(sess)
	req, in := asyncDelivery("sess-bind")
	r.OnAsync(req, in, answeredAssessment())
	if r.AsyncDroppedNoSession() != 0 || len(sess.GetSemanticShadow()) != 1 || calls.Load() != 1 {
		t.Fatalf("Bind refused the session: dropped=%d shadow=%d persists=%d", r.AsyncDroppedNoSession(), len(sess.GetSemanticShadow()), calls.Load())
	}
}
