package unit

import (
	"strings"
	"sync"
	"testing"
	"time"

	"elida/internal/decision"
	"elida/internal/decision/decisiontest"
	"elida/internal/session"
)

// shadowDecisionIDs is the set of a session's own shadow DecisionIDs.
func shadowDecisionIDs(sess *session.Session) map[string]bool {
	ids := map[string]bool{}
	for _, d := range sess.GetSemanticShadow() {
		ids[d.DecisionID] = true
	}
	return ids
}

// reuseID returns a new session instance under sess's ID. Its start time is
// set apart explicitly so the two instances differ even on a coarse clock.
func reuseID(sess *session.Session) *session.Session {
	next := session.NewSession(sess.ID, "https://backend", "127.0.0.1:1")
	next.StartTime = sess.StartTime.Add(time.Second)
	return next
}

// Task 8 review M1: a request on a session that has already ended (state
// moved off Active, then Unbind) must not re-bind it or leave shadow
// correlation state that a later session reusing the ID would inherit.
func TestShadowCorrelation_RequestAfterUnbindDoesNotRebind(t *testing.T) {
	r, _, sc := shadowCorrRunner(t, "shadow", elevatedScores())
	ended := session.NewSession("sess-late", "https://backend", "127.0.0.1:1")
	assessTool(r, ended, 0, "First mildly odd note.")
	if sc.Stats().Sessions != 1 {
		t.Fatalf("precondition: one tracked session, got %+v", sc.Stats())
	}
	ended.SetState(session.Completed)
	r.Unbind(ended)

	assessTool(r, ended, 1, "A late mildly odd note.")
	if r.Bound(ended.ID) {
		t.Fatal("a request on an ended session must not re-bind it")
	}
	if st := sc.Stats(); st.Sessions != 0 || st.HistoryEvents != 0 {
		t.Fatalf("a request on an ended session must leave no shadow correlation state: %+v", st)
	}

	next := reuseID(ended)
	assessTool(r, next, 2, "Second session, first odd note.")
	assessTool(r, next, 3, "Second session, second odd note.")
	if recs := next.GetCorrelationShadow(); len(recs) != 0 {
		t.Fatalf("the reused ID inherited the ended session's evidence: %+v", recs)
	}
	assessTool(r, next, 4, "Second session, third odd note.")
	recs := next.GetCorrelationShadow()
	if len(recs) != 1 {
		t.Fatalf("three elevated messages of the new session must match once, got %+v", recs)
	}
	own := shadowDecisionIDs(next)
	for _, id := range recs[0].EventIDs {
		if !own[id] {
			t.Fatalf("contributing ID %q is not one of this session's decisions", id)
		}
	}
}

// Task 8 review M1: a request still in flight when the session ends must
// not Observe after Unbind (the binding is checked under the registry lock
// around Observe).
func TestShadowCorrelation_InFlightRequestEndingAfterUnbindDoesNotObserve(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	f := decisiontest.NewFake(elevatedScores())
	f.ScoreFunc = func(in decision.Input) map[decision.Signal]float64 {
		if strings.Contains(in.Content, "GATED") {
			once.Do(func() { close(entered) })
			<-release
		}
		return elevatedScores()
	}
	r, _, sc := shadowCorrRunnerWith(t, "shadow", f)
	sess := session.NewSession("sess-inflight", "https://backend", "127.0.0.1:1")

	done := make(chan struct{})
	go func() {
		defer close(done)
		assessTool(r, sess, 0, "GATED mildly odd note.")
	}()
	<-entered
	sess.SetState(session.Completed)
	r.Unbind(sess)
	close(release)
	<-done

	if st := sc.Stats(); st.Sessions != 0 || st.EventsObserved != 0 {
		t.Fatalf("a decision finishing after Unbind must not be observed: %+v", st)
	}
	if len(sess.GetSemanticShadow()) == 0 {
		t.Fatal("fixture: the in-flight decision must still be answered and recorded as shadow data")
	}
}

// Task 8 review M2: a new session instance binding an ID before the old
// instance's Unbind (the old one is retained after a timeout) starts with
// empty shadow correlation state.
func TestShadowCorrelation_ReusedIDDoesNotInheritShadowState(t *testing.T) {
	r, _, _ := shadowCorrRunner(t, "shadow", elevatedScores())
	old := session.NewSession("sess-reused", "https://backend", "127.0.0.1:1")
	assessTool(r, old, 0, "Old session, first odd note.")
	assessTool(r, old, 1, "Old session, second odd note.")
	old.SetState(session.TimedOut) // retained; its end callback has not run yet

	next := reuseID(old)
	assessTool(r, next, 2, "New session, first odd note.")
	if recs := next.GetCorrelationShadow(); len(recs) != 0 {
		t.Fatalf("the new session completed a match on the old session's evidence: %+v", recs)
	}
	r.Unbind(old) // the old instance's end: a no-op for the new binding
	if !r.Bound(next.ID) {
		t.Fatal("the old instance's Unbind must not drop the new binding")
	}
	assessTool(r, next, 3, "New session, second odd note.")
	assessTool(r, next, 4, "New session, third odd note.")
	recs := next.GetCorrelationShadow()
	if len(recs) != 1 {
		t.Fatalf("three elevated messages of the new session must match once, got %+v", recs)
	}
	own := shadowDecisionIDs(next)
	for _, id := range recs[0].EventIDs {
		if !own[id] {
			t.Fatalf("contributing ID %q is not one of this session's decisions", id)
		}
	}
}

// A session store that returns a fresh copy per lookup (the Redis store)
// hands the runner a new pointer for the SAME instance on every request:
// that is not a new session, and its shadow state is kept.
func TestShadowCorrelation_SameInstanceFreshPointerKeepsState(t *testing.T) {
	r, _, _ := shadowCorrRunner(t, "shadow", elevatedScores())
	first := session.NewSession("sess-copy", "https://backend", "127.0.0.1:1")
	assessTool(r, first, 0, "First mildly odd note.")
	assessTool(r, first, 1, "Second mildly odd note.")

	copyOf := session.NewSession(first.ID, "https://backend", "127.0.0.1:1")
	copyOf.StartTime = first.StartTime
	assessTool(r, copyOf, 2, "Third mildly odd note.")
	if recs := copyOf.GetCorrelationShadow(); len(recs) != 1 || len(recs[0].EventIDs) != 3 {
		t.Fatalf("a fresh pointer to the same session instance must keep its shadow evidence: %+v", recs)
	}
	r.Unbind(copyOf)
	if r.Bound(first.ID) {
		t.Fatal("Unbind through a fresh pointer to the same instance must end the binding")
	}
}
