package unit

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"elida/internal/correlation"
	"elida/internal/decision/decisiontest"
	"elida/internal/decision/runner"
	"elida/internal/session"
)

// gatedCorrelator wraps the shipped correlator. Once armed, the first
// Observe whose key starts with prefix computes its matches and then blocks
// until release is closed, so a session can end (or be replaced) between
// the correlator's work and the runner's re-check. With gateBefore set it
// blocks before the correlator runs instead, so a session end's Forget
// lands before the late Observe writes its event.
type gatedCorrelator struct {
	inner      *correlation.Engine
	prefix     string
	gateBefore bool
	armed      atomic.Bool
	entered    chan struct{}
	release    chan struct{}
}

func newGatedCorrelator(inner *correlation.Engine, prefix string) *gatedCorrelator {
	return &gatedCorrelator{inner: inner, prefix: prefix, entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *gatedCorrelator) Observe(key string, ev []correlation.Event) []correlation.Match {
	gate := strings.HasPrefix(key, g.prefix) && g.armed.CompareAndSwap(true, false)
	if gate && g.gateBefore {
		close(g.entered)
		<-g.release
		return g.inner.Observe(key, ev)
	}
	matches := g.inner.Observe(key, ev)
	if gate {
		close(g.entered)
		<-g.release
	}
	return matches
}

func (g *gatedCorrelator) Forget(key string) { g.inner.Forget(key) }

func gatedShadowRunner(t *testing.T, prefix string) (*runner.Runner, *correlation.Engine, *gatedCorrelator) {
	t.Helper()
	var g *gatedCorrelator
	r, _, eng := shadowCorrRunnerWrapped(t, "shadow", decisiontest.NewFake(elevatedScores()),
		func(e *correlation.Engine) runner.ShadowCorrelator {
			g = newGatedCorrelator(e, prefix)
			return g
		})
	return r, eng, g
}

// waitOrFail waits for done. On timeout it releases the gate (so the
// blocked goroutines can finish) and fails.
func waitOrFail(t *testing.T, done <-chan struct{}, g *gatedCorrelator, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		close(g.release)
		t.Fatalf("%s blocked behind a shadow Observe", what)
	}
}

// Task 9 review I-1: a slow shadow Observe on one session must not stall
// unrelated sessions' AssessRequest or Unbind. Before the fix Observe ran
// under the runner-wide boundMu, so every one of these waited for the
// gated Observe (the reviewer measured ~800 ms each behind an 800 ms
// Observe). Here they must all complete while the gated Observe is still
// blocked.
func TestShadowCorrelation_SlowObserveDoesNotStallOtherSessions(t *testing.T) {
	r, _, g := gatedShadowRunner(t, "sess-slow@")
	base := session.NewSession("sess-base", "https://backend", "127.0.0.1:1")
	assessTool(r, base, 0, "Baseline mildly odd note.")
	if !r.Bound(base.ID) {
		t.Fatal("fixture: the baseline session must be bound")
	}

	slow := session.NewSession("sess-slow", "https://backend", "127.0.0.1:1")
	g.armed.Store(true)
	slowDone := make(chan struct{})
	go func() {
		defer close(slowDone)
		assessTool(r, slow, 0, "Slow mildly odd note.")
	}()
	<-g.entered

	var wg sync.WaitGroup
	lat := make([]time.Duration, 8)
	for i := range lat {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := session.NewSession(fmt.Sprintf("sess-other-%d", i), "https://backend", "127.0.0.1:1")
			start := time.Now()
			assessTool(r, s, 0, "Other mildly odd note.")
			lat[i] = time.Since(start)
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.Unbind(base)
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	waitOrFail(t, done, g, "an unrelated AssessRequest or Unbind")
	for i, l := range lat {
		t.Logf("unrelated session %d AssessRequest during the gated Observe: %v", i, l)
	}
	if r.Bound(base.ID) {
		t.Fatal("the unrelated Unbind must have completed")
	}
	select {
	case <-slowDone:
		t.Fatal("fixture: the gated Observe must still be blocked")
	default:
	}
	close(g.release)
	<-slowDone
	if !r.Bound(slow.ID) {
		t.Fatal("the slow session stays bound")
	}
}

// Task 9 review I-1 (with Task 8 M1): Observe runs outside the lock, so a
// session can end while its Observe runs. The match that Observe produces
// is dropped (counted), nothing is recorded on the ended session, and no
// correlator state survives the session.
func TestShadowCorrelation_UnbindDuringObserveDropsMatchAndState(t *testing.T) {
	r, eng, g := gatedShadowRunner(t, "sess-end-mid@")
	sess := session.NewSession("sess-end-mid", "https://backend", "127.0.0.1:1")
	assessTool(r, sess, 0, "First mildly odd note.")
	assessTool(r, sess, 1, "Second mildly odd note.")

	g.armed.Store(true)
	reqDone := make(chan struct{})
	go func() {
		defer close(reqDone)
		assessTool(r, sess, 2, "Third mildly odd note.") // completes the match
	}()
	<-g.entered
	sess.SetState(session.Completed)
	unbound := make(chan struct{})
	go func() {
		defer close(unbound)
		r.Unbind(sess)
	}()
	waitOrFail(t, unbound, g, "Unbind of the observing session")
	close(g.release)
	<-reqDone

	if recs := sess.GetCorrelationShadow(); len(recs) != 0 {
		t.Fatalf("a match computed after the session ended must not be recorded: %+v", recs)
	}
	if n := r.ShadowMatchesDropped(); n != 1 {
		t.Fatalf("ShadowMatchesDropped = %d, want 1", n)
	}
	if st := eng.Stats(); st.Sessions != 0 || st.HistoryEvents != 0 {
		t.Fatalf("no shadow correlation state may outlive the session: %+v", st)
	}
}

// Task 9 review I-1 (with Task 8 M2): an old instance's Observe still
// running when a new instance of the reused ID binds never writes into the
// new instance's evidence, and the old instance's match is dropped.
func TestShadowCorrelation_OldInstanceObserveDuringReuseStaysSeparate(t *testing.T) {
	r, eng, g := gatedShadowRunner(t, "sess-reuse-mid@")
	old := session.NewSession("sess-reuse-mid", "https://backend", "127.0.0.1:1")
	assessTool(r, old, 0, "Old first mildly odd note.")
	assessTool(r, old, 1, "Old second mildly odd note.")

	g.armed.Store(true)
	oldDone := make(chan struct{})
	go func() {
		defer close(oldDone)
		assessTool(r, old, 2, "Old third mildly odd note.") // completes the old match
	}()
	<-g.entered
	old.SetState(session.TimedOut) // retained; its Unbind has not run yet
	next := reuseID(old)
	nextDone := make(chan struct{})
	go func() {
		defer close(nextDone)
		assessTool(r, next, 3, "New first mildly odd note.")
		assessTool(r, next, 4, "New second mildly odd note.")
	}()
	waitOrFail(t, nextDone, g, "the new instance's AssessRequest")
	close(g.release)
	<-oldDone

	if recs := old.GetCorrelationShadow(); len(recs) != 0 {
		t.Fatalf("the replaced instance's match must be dropped: %+v", recs)
	}
	if n := r.ShadowMatchesDropped(); n != 1 {
		t.Fatalf("ShadowMatchesDropped = %d, want 1", n)
	}
	if recs := next.GetCorrelationShadow(); len(recs) != 0 {
		t.Fatalf("the new instance matched on the old instance's evidence: %+v", recs)
	}
	assessTool(r, next, 5, "New third mildly odd note.")
	recs := next.GetCorrelationShadow()
	if len(recs) != 1 {
		t.Fatalf("three elevated messages of the new instance must match once, got %+v", recs)
	}
	own := shadowDecisionIDs(next)
	for _, id := range recs[0].EventIDs {
		if !own[id] {
			t.Fatalf("contributing ID %q is not one of this instance's decisions", id)
		}
	}
	r.Unbind(old) // the old instance's end: a no-op for the new binding
	r.Unbind(next)
	if st := eng.Stats(); st.Sessions != 0 {
		t.Fatalf("no shadow state may remain after both instances ended: %+v", st)
	}
}

// Task 9 review I-1 (with Task 8 M1): when the session ends while its
// Observe is still waiting to run, Unbind's Forget comes first and the late
// Observe then writes the event. The runner's re-check after Observe must
// forget that instance again, so no state outlives the session.
func TestShadowCorrelation_UnbindBeforeLateObserveWritesLeavesNoState(t *testing.T) {
	r, eng, g := gatedShadowRunner(t, "sess-end-early@")
	g.gateBefore = true
	sess := session.NewSession("sess-end-early", "https://backend", "127.0.0.1:1")
	assessTool(r, sess, 0, "First mildly odd note.")

	g.armed.Store(true)
	reqDone := make(chan struct{})
	go func() {
		defer close(reqDone)
		assessTool(r, sess, 1, "Second mildly odd note.")
	}()
	<-g.entered
	sess.SetState(session.Completed)
	unbound := make(chan struct{})
	go func() {
		defer close(unbound)
		r.Unbind(sess)
	}()
	waitOrFail(t, unbound, g, "Unbind of the observing session")
	if st := eng.Stats(); st.Sessions != 0 {
		t.Fatalf("fixture: Unbind must have forgotten the session before the late Observe: %+v", st)
	}
	close(g.release)
	<-reqDone

	if st := eng.Stats(); st.Sessions != 0 || st.HistoryEvents != 0 {
		t.Fatalf("a late Observe after session end must leave no shadow correlation state: %+v", st)
	}
}
