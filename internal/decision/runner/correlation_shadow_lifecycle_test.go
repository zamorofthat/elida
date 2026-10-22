package runner

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/correlation"
	"elida/internal/decision"
	"elida/internal/session"
)

// gatedShadowCorrelator is a ShadowCorrelator whose Observe returns one
// match for every event. Once armed, the next Observe computes its match
// and then blocks on release (after signaling entered), so the test can
// order a session's Unbind, or its replacement, between the correlator's
// work and the runner's acceptance of the result. Forget records its keys.
type gatedShadowCorrelator struct {
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}

	mu      sync.Mutex
	forgets []string
}

func newGatedShadowCorrelator() *gatedShadowCorrelator {
	return &gatedShadowCorrelator{entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *gatedShadowCorrelator) Observe(_ string, evs []correlation.Event) []correlation.Match {
	out := make([]correlation.Match, 0, len(evs))
	for _, ev := range evs {
		out = append(out, correlation.Match{
			ID: "cm_" + ev.ID, Rule: "lifecycle_rule", Shape: correlation.ShapeRepeated, Severity: "warning",
			Mode: correlation.ModeAudit, EventIDs: []string{ev.ID}, Categories: []string{ev.Category},
			FirstEvent: ev.Timestamp, LastEvent: ev.Timestamp,
		})
	}
	if g.armed.CompareAndSwap(true, false) {
		close(g.entered)
		<-g.release
	}
	return out
}

func (g *gatedShadowCorrelator) Forget(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.forgets = append(g.forgets, key)
}

func (g *gatedShadowCorrelator) forgotten(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, k := range g.forgets {
		if k == key {
			n++
		}
	}
	return n
}

// lifecycleRunner is a runner whose only configured behavior is the shadow
// correlator: the tests drive correlateShadow directly, so no scheduler or
// model is needed (Mode disabled is what New accepts without one).
func lifecycleRunner(t *testing.T) (*Runner, *gatedShadowCorrelator) {
	t.Helper()
	g := newGatedShadowCorrelator()
	r, err := New(Config{Mode: config.DecisionModeDisabled, Thresholds: testThresholds(), ShadowCorrelator: g})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r, g
}

// elevated is an elevated-band, non-vetoed verdict for message i: exactly
// what correlateShadow feeds to the correlator.
func elevated(i int) (decision.Input, Verdict) {
	return decision.Input{SourceRole: "tool", MessageIndex: i},
		Verdict{Signal: decision.SignalInjection, Probability: 0.4, Severity: "info", Answered: true, DecisionID: fmt.Sprintf("dec_%d", i)}
}

func lifecycleSession(id string) *session.Session {
	return session.NewSession(id, "https://backend", "127.0.0.1:1")
}

// waitFor waits for done, failing (and releasing the gate so the blocked
// goroutine can finish) on timeout. The timeout never orders anything: it
// only turns a deadlock into a test failure.
func waitFor(t *testing.T, done <-chan struct{}, g *gatedShadowCorrelator, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		close(g.release)
		t.Fatalf("%s did not complete", what)
	}
}

// Recording wins: a match accepted before Unbind is in the snapshot the
// session-end path takes after Unbind returns.
func TestCorrelationShadowLifecycle_MatchBeforeUnbindIsInSnapshot(t *testing.T) {
	r, g := lifecycleRunner(t)
	sess := lifecycleSession("sess-lc-before")
	r.Bind(sess)
	in, v := elevated(0)
	r.correlateShadow(sess, in, v)
	r.Unbind(sess)

	snap := sess.Snapshot()
	if len(snap.CorrelationShadow) != 1 || snap.CorrelationShadow[0].MatchID != "cm_dec_0" {
		t.Fatalf("snapshot after Unbind must hold the match recorded before it: %+v", snap.CorrelationShadow)
	}
	if n := r.ShadowMatchesDropped(); n != 0 {
		t.Fatalf("ShadowMatchesDropped = %d, want 0", n)
	}
	if n := g.forgotten(shadowKey(sess)); n == 0 {
		t.Fatal("Unbind must forget the instance's correlator key")
	}
}

// Unbind wins: the session is unbound while its Observe is still running.
// Unbind returns without waiting for Observe; the snapshot taken after it
// is empty, and the match that arrives afterwards is rejected, counted in
// ShadowMatchesDropped and its key forgotten, so the snapshot stays empty.
func TestCorrelationShadowLifecycle_UnbindDuringObserveRejectsAndCounts(t *testing.T) {
	r, g := lifecycleRunner(t)
	sess := lifecycleSession("sess-lc-unbind-mid")
	r.Bind(sess)

	g.armed.Store(true)
	done := make(chan struct{})
	go func() {
		defer close(done)
		in, v := elevated(0)
		r.correlateShadow(sess, in, v)
	}()
	<-g.entered // Observe has its match and is blocked before returning it

	sess.SetState(session.Completed)
	unbound := make(chan struct{})
	go func() {
		defer close(unbound)
		r.Unbind(sess)
	}()
	waitFor(t, unbound, g, "Unbind during a blocked Observe")
	if got := sess.Snapshot().CorrelationShadow; len(got) != 0 {
		t.Fatalf("snapshot after Unbind: %+v, want none", got)
	}
	select {
	case <-done:
		t.Fatal("fixture: the gated Observe must still be blocked")
	default:
	}
	forgotten := g.forgotten(shadowKey(sess)) // Bind's and Unbind's Forget

	close(g.release)
	<-done
	if got := sess.Snapshot().CorrelationShadow; len(got) != 0 {
		t.Fatalf("a match arriving after Unbind returned must not be recorded: %+v", got)
	}
	if n := r.ShadowMatchesDropped(); n != 1 {
		t.Fatalf("ShadowMatchesDropped = %d, want 1", n)
	}
	// The rejected match forgets the key again, so nothing the late Observe
	// wrote outlives the session.
	if n := g.forgotten(shadowKey(sess)); n != forgotten+1 {
		t.Fatalf("key forgotten %d times after the rejection, want %d", n, forgotten+1)
	}
	if r.Bound(sess.ID) {
		t.Fatal("the rejected match must not re-bind the session")
	}
}

// Replacement wins: a new instance of the same ID binds while the old
// instance's Observe is still running. The old instance's match is rejected
// and counted; neither instance records it.
func TestCorrelationShadowLifecycle_ReplacementDuringObserveRejectsAndCounts(t *testing.T) {
	r, g := lifecycleRunner(t)
	old := lifecycleSession("sess-lc-reuse-mid")
	r.Bind(old)

	g.armed.Store(true)
	done := make(chan struct{})
	go func() {
		defer close(done)
		in, v := elevated(0)
		r.correlateShadow(old, in, v)
	}()
	<-g.entered

	old.SetState(session.TimedOut) // retained; its Unbind has not run yet
	next := lifecycleSession(old.ID)
	next.StartTime = old.StartTime.Add(time.Second)
	bound := make(chan struct{})
	go func() {
		defer close(bound)
		r.Bind(next)
	}()
	waitFor(t, bound, g, "Bind of the replacing instance during a blocked Observe")
	forgotten := g.forgotten(shadowKey(old)) // Bind's own, and the replacement's

	close(g.release)
	<-done
	if got := old.Snapshot().CorrelationShadow; len(got) != 0 {
		t.Fatalf("the replaced instance must not record the late match: %+v", got)
	}
	if got := next.Snapshot().CorrelationShadow; len(got) != 0 {
		t.Fatalf("the new instance must not receive the old instance's match: %+v", got)
	}
	if n := r.ShadowMatchesDropped(); n != 1 {
		t.Fatalf("ShadowMatchesDropped = %d, want 1", n)
	}
	if n := g.forgotten(shadowKey(old)); n != forgotten+1 {
		t.Fatalf("old key forgotten %d times after the rejection, want %d", n, forgotten+1)
	}
	r.Unbind(old) // a no-op for the new binding
	if !r.Bound(next.ID) {
		t.Fatal("the old instance's Unbind must not release the new instance")
	}
	r.Unbind(next)
}

// The guarantee under real concurrency: once Observe has its match, the
// runner's acceptance and the session's Unbind race. Whichever runs first,
// nothing is recorded on a session after its Unbind returned, and every
// match is either in the snapshot or counted as dropped. The gate makes
// sure each iteration's Observe has already passed the pre-check; the start
// barrier then releases acceptance and Unbind together. No sleeps.
func TestCorrelationShadowLifecycle_NothingLandsAfterUnbindReturns(t *testing.T) {
	r, g := lifecycleRunner(t)
	const iterations = 500
	var recorded, dropped int64
	for i := 0; i < iterations; i++ {
		sess := lifecycleSession(fmt.Sprintf("sess-lc-race-%d", i))
		r.Bind(sess)
		droppedBefore := r.ShadowMatchesDropped()
		// Fresh gate channels: the previous iteration's Observe has returned
		// (wg.Wait below), and the goroutine started next observes them.
		g.entered, g.release = make(chan struct{}), make(chan struct{})
		g.armed.Store(true)
		observed := make(chan struct{})
		go func() {
			defer close(observed)
			in, v := elevated(i)
			r.correlateShadow(sess, in, v)
		}()
		<-g.entered // the pre-check passed; Observe holds the match

		start := make(chan struct{})
		var wg sync.WaitGroup
		var afterUnbind int
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			close(g.release)
		}()
		go func() {
			defer wg.Done()
			<-start
			r.Unbind(sess)
			afterUnbind = len(sess.Snapshot().CorrelationShadow)
		}()
		close(start)
		wg.Wait()
		<-observed
		final := len(sess.Snapshot().CorrelationShadow)
		if final != afterUnbind {
			t.Fatalf("iteration %d: %d match(es) landed after Unbind returned (snapshot had %d, now %d)", i, final-afterUnbind, afterUnbind, final)
		}
		d := r.ShadowMatchesDropped() - droppedBefore
		if int64(final)+d != 1 {
			t.Fatalf("iteration %d: recorded %d + dropped %d != 1", i, final, d)
		}
		recorded += int64(final)
		dropped += d
	}
	t.Logf("%d iterations: %d recorded before Unbind, %d rejected after it", iterations, recorded, dropped)
}

// The atomic step itself: recordShadowMatches records only while this
// exact instance owns the binding. After Unbind, or after a replacement
// instance binds, it reports false and records nothing; the replacement
// records its own matches.
func TestCorrelationShadowLifecycle_AcceptanceAfterUnbindIsRejected(t *testing.T) {
	r, _ := lifecycleRunner(t)
	sess := lifecycleSession("sess-lc-accept")
	now := time.Now()
	one := []correlation.Match{{ID: "cm_x", Rule: "lifecycle_rule", EventIDs: []string{"dec_x"}, Categories: []string{CategoryInjectionElevated}}}

	if r.recordShadowMatches(sess, now, one) {
		t.Fatal("an unbound session must not accept matches")
	}
	r.Bind(sess)
	if !r.recordShadowMatches(sess, now, one) {
		t.Fatal("a bound session must accept matches")
	}
	r.Unbind(sess)
	if r.recordShadowMatches(sess, now, one) {
		t.Fatal("after Unbind returned no match may be accepted")
	}
	if got := sess.Snapshot().CorrelationShadow; len(got) != 1 || got[0].MatchID != "cm_x" {
		t.Fatalf("snapshot: %+v, want only the match accepted while bound", got)
	}

	old := lifecycleSession("sess-lc-accept-reuse")
	r.Bind(old)
	old.SetState(session.TimedOut)
	next := lifecycleSession(old.ID)
	next.StartTime = old.StartTime.Add(time.Second)
	r.Bind(next)
	if r.recordShadowMatches(old, now, one) {
		t.Fatal("a replaced instance must not accept matches")
	}
	if !r.recordShadowMatches(next, now, one) {
		t.Fatal("the replacing instance must accept matches")
	}
	if got := old.Snapshot().CorrelationShadow; len(got) != 0 {
		t.Fatalf("replaced instance: %+v, want none", got)
	}
	if got := next.Snapshot().CorrelationShadow; len(got) != 1 {
		t.Fatalf("replacing instance: %+v, want one", got)
	}
	r.Unbind(old)
	r.Unbind(next)
}
