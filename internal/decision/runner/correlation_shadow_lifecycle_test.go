package runner

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/correlation"
	"elida/internal/decision"
	"elida/internal/decision/preprocess"
	"elida/internal/session"
)

// gatedShadowCorrelator is a ShadowCorrelator whose Observe returns one
// match for every event. Once armed, the next Observe computes its match
// and then blocks on release (after signaling entered), so the test can
// order a session's Unbind, or its replacement, between the correlator's
// work and the runner's acceptance of the result. Its Forget gate models a
// correlator shard operation that is blocked after the runner has invoked it.
type gatedShadowCorrelator struct {
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}

	forgetMu      sync.Mutex
	forgetArmed   bool
	forgetEntered chan string
	forgetRelease chan struct{}

	mu       sync.Mutex
	forgets  []string
	observed map[string]int
}

func newGatedShadowCorrelator() *gatedShadowCorrelator {
	return &gatedShadowCorrelator{
		entered: make(chan struct{}), release: make(chan struct{}), observed: make(map[string]int),
	}
}

func (g *gatedShadowCorrelator) Observe(key string, evs []correlation.Event) []correlation.Match {
	g.mu.Lock()
	g.observed[key]++
	g.mu.Unlock()
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
	g.forgets = append(g.forgets, key)
	g.mu.Unlock()

	g.forgetMu.Lock()
	var entered chan string
	var release chan struct{}
	if g.forgetArmed {
		g.forgetArmed = false
		entered, release = g.forgetEntered, g.forgetRelease
	}
	g.forgetMu.Unlock()
	if entered != nil {
		entered <- key
		<-release
	}

	g.mu.Lock()
	delete(g.observed, key)
	g.mu.Unlock()
}

// forgottenFor counts cleanups for any binding generation of a session
// instance. Before the lifecycle-generation fix, keys are the bare base;
// after it they carry a generation suffix.
func (g *gatedShadowCorrelator) forgottenFor(base string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, k := range g.forgets {
		if k == base || strings.HasPrefix(k, base+"#") {
			n++
		}
	}
	return n
}

func (g *gatedShadowCorrelator) armForget() <-chan string {
	g.forgetMu.Lock()
	defer g.forgetMu.Unlock()
	g.forgetArmed = true
	g.forgetEntered = make(chan string, 1)
	g.forgetRelease = make(chan struct{})
	return g.forgetEntered
}

func (g *gatedShadowCorrelator) releaseForget() {
	g.forgetMu.Lock()
	release := g.forgetRelease
	g.forgetRelease = nil
	g.forgetMu.Unlock()
	if release != nil {
		close(release)
	}
}

func (g *gatedShadowCorrelator) observedKeys() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	keys := make([]string, 0, len(g.observed))
	for key := range g.observed {
		keys = append(keys, key)
	}
	return keys
}

func (g *gatedShadowCorrelator) observedCount(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.observed[key]
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

// lifecycleNoopScheduler lets the availability test take the real
// AssessRequest path without creating a decision or another correlator call.
type lifecycleNoopScheduler struct{}

func (lifecycleNoopScheduler) Assess(context.Context, decision.Input, []decision.Signal) (decision.Assessment, error) {
	return decision.Assessment{}, nil
}

func lifecycleAssessRunner(t *testing.T) (*Runner, *gatedShadowCorrelator) {
	t.Helper()
	g := newGatedShadowCorrelator()
	r, err := New(Config{
		Mode:             config.DecisionModeShadow,
		Thresholds:       testThresholds(),
		Budget:           preprocess.Budget{MaxInputBytes: 1024, MaxAnalysisBytes: 1024, MaxRepresentations: 1, MaxDecodeDepth: 0, MaxExpansionRatio: 1},
		Model:            ModelIdentity{ThresholdSet: "test"},
		Signals:          []decision.Signal{decision.SignalInjection},
		Scheduler:        lifecycleNoopScheduler{},
		ShadowCorrelator: g,
	})
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

func waitForDone(t *testing.T, done <-chan struct{}, unblock func(), what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		unblock()
		t.Fatalf("%s did not complete", what)
	}
}

func waitForForget(t *testing.T, entered <-chan string, g *gatedShadowCorrelator, what string) string {
	t.Helper()
	select {
	case key := <-entered:
		return key
	case <-time.After(5 * time.Second):
		g.releaseForget()
		t.Fatalf("%s did not enter Forget", what)
		return ""
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
	if n := g.forgottenFor(shadowKey(sess)); n == 0 {
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
	forgotten := g.forgottenFor(shadowKey(sess)) // Unbind's Forget

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
	if n := g.forgottenFor(shadowKey(sess)); n != forgotten+1 {
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
	forgotten := g.forgottenFor(shadowKey(old)) // replacement's Forget

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
	if n := g.forgottenFor(shadowKey(old)); n != forgotten+1 {
		t.Fatalf("old key forgotten %d times after the rejection, want %d", n, forgotten+1)
	}
	r.Unbind(old) // a no-op for the new binding
	if !r.Bound(next.ID) {
		t.Fatal("the old instance's Unbind must not release the new instance")
	}
	r.Unbind(next)
}

// A slow Forget for A may wait behind the correlator's shard lock, but it
// must not retain boundMu while it does. Bind, lookup and the real
// AssessRequest path for B therefore complete while A's cleanup is blocked.
func TestPlanCFix_BlockedForgetLeavesUnrelatedRunnerWorkAvailable(t *testing.T) {
	r, g := lifecycleAssessRunner(t)
	a := lifecycleSession("sess-lc-forget-a")
	r.Bind(a)
	forgetEntered := g.armForget()

	unbound := make(chan struct{})
	go func() {
		defer close(unbound)
		r.Unbind(a)
	}()
	oldKey := waitForForget(t, forgetEntered, g, "Unbind A")
	if oldKey == "" {
		t.Fatal("Forget must receive A's binding key")
	}
	t.Cleanup(func() {
		g.releaseForget()
		select {
		case <-unbound:
		case <-time.After(5 * time.Second):
			t.Error("Unbind A did not finish after Forget was released")
		}
	})

	b := lifecycleSession("sess-lc-forget-b")
	bound := make(chan struct{})
	go func() {
		defer close(bound)
		r.Bind(b)
	}()
	waitForDone(t, bound, g.releaseForget, "Bind B while Forget A is blocked")

	lookedUp := make(chan *session.Session, 1)
	go func() { lookedUp <- r.lookupSession(b.ID) }()
	select {
	case got := <-lookedUp:
		if got == nil || !sameInstance(got, b) {
			t.Fatalf("lookup B = %p, want B %p", got, b)
		}
	case <-time.After(5 * time.Second):
		g.releaseForget()
		t.Fatal("lookup B blocked behind Forget A")
	}

	assessed := make(chan struct{})
	go func() {
		defer close(assessed)
		r.AssessRequest(context.Background(), b, "req-b", []Message{{Role: "user", Index: 0, Content: "ordinary input"}})
	}()
	waitForDone(t, assessed, g.releaseForget, "AssessRequest B while Forget A is blocked")
	g.releaseForget()
	waitForDone(t, unbound, g.releaseForget, "Unbind A after Forget is released")
}

// Replacement atomically changes the runner binding before the old key is
// forgotten. The delayed old cleanup must therefore leave state observed by
// the new A instance intact; a later cleanup of the new key would erase it.
func TestPlanCFix_DelayedReplacementForgetPreservesNewState(t *testing.T) {
	r, g := lifecycleRunner(t)
	old := lifecycleSession("sess-lc-forget-replace")
	r.Bind(old)
	forgetEntered := g.armForget()

	next := lifecycleSession(old.ID)
	next.StartTime = old.StartTime.Add(time.Second)
	bound := make(chan struct{})
	go func() {
		defer close(bound)
		r.Bind(next)
	}()
	oldKey := waitForForget(t, forgetEntered, g, "Bind replacement A")
	t.Cleanup(func() {
		g.releaseForget()
		select {
		case <-bound:
		case <-time.After(5 * time.Second):
			t.Error("replacement Bind did not finish after Forget was released")
		}
	})

	lookedUp := make(chan *session.Session, 1)
	go func() { lookedUp <- r.lookupSession(next.ID) }()
	var got *session.Session
	select {
	case got = <-lookedUp:
	case <-time.After(5 * time.Second):
		g.releaseForget()
		t.Fatal("replacement lookup blocked behind delayed Forget")
	}
	if got == nil || !sameInstance(got, next) {
		t.Fatalf("lookup replacement = %p, want %p", got, next)
	}
	in, v := elevated(1)
	r.correlateShadow(next, in, v)
	keys := g.observedKeys()
	if len(keys) != 1 {
		t.Fatalf("observed keys = %v, want one new binding key", keys)
	}
	newKey := keys[0]
	if newKey == oldKey {
		t.Fatalf("new binding reused delayed cleanup key %q", newKey)
	}

	g.releaseForget()
	waitForDone(t, bound, g.releaseForget, "replacement Bind after Forget is released")
	if got := g.observedCount(newKey); got != 1 {
		t.Fatalf("new binding state after delayed old Forget = %d, want 1", got)
	}
	r.Unbind(next)
}

// Unbind can race a subsequent Bind of an active session instance. A distinct
// binding generation makes the delayed cleanup target only the retired
// binding, never the state the new binding has already observed.
func TestPlanCFix_DelayedUnbindForgetPreservesReboundState(t *testing.T) {
	r, g := lifecycleRunner(t)
	sess := lifecycleSession("sess-lc-forget-rebind")
	r.Bind(sess)
	forgetEntered := g.armForget()

	unbound := make(chan struct{})
	go func() {
		defer close(unbound)
		r.Unbind(sess)
	}()
	oldKey := waitForForget(t, forgetEntered, g, "Unbind A")
	t.Cleanup(func() {
		g.releaseForget()
		select {
		case <-unbound:
		case <-time.After(5 * time.Second):
			t.Error("Unbind did not finish after Forget was released")
		}
	})

	bound := make(chan struct{})
	go func() {
		defer close(bound)
		r.Bind(sess)
	}()
	waitForDone(t, bound, g.releaseForget, "rebind A while its old Forget is blocked")
	in, v := elevated(2)
	r.correlateShadow(sess, in, v)
	keys := g.observedKeys()
	if len(keys) != 1 {
		t.Fatalf("observed keys = %v, want one rebound binding key", keys)
	}
	newKey := keys[0]
	if newKey == oldKey {
		t.Fatalf("rebound binding reused delayed cleanup key %q", newKey)
	}

	g.releaseForget()
	waitForDone(t, unbound, g.releaseForget, "Unbind after Forget is released")
	if got := g.observedCount(newKey); got != 1 {
		t.Fatalf("rebound state after delayed old Forget = %d, want 1", got)
	}
	r.Unbind(sess)
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

	if r.recordShadowMatches(sess, "", now, one) {
		t.Fatal("an unbound session must not accept matches")
	}
	r.Bind(sess)
	key, ok := r.shadowBindingKey(sess)
	if !ok {
		t.Fatal("bound session lacks a shadow binding key")
	}
	if !r.recordShadowMatches(sess, key, now, one) {
		t.Fatal("a bound session must accept matches")
	}
	r.Unbind(sess)
	if r.recordShadowMatches(sess, key, now, one) {
		t.Fatal("after Unbind returned no match may be accepted")
	}
	if got := sess.Snapshot().CorrelationShadow; len(got) != 1 || got[0].MatchID != "cm_x" {
		t.Fatalf("snapshot: %+v, want only the match accepted while bound", got)
	}

	old := lifecycleSession("sess-lc-accept-reuse")
	r.Bind(old)
	oldKey, ok := r.shadowBindingKey(old)
	if !ok {
		t.Fatal("old binding lacks a shadow key")
	}
	old.SetState(session.TimedOut)
	next := lifecycleSession(old.ID)
	next.StartTime = old.StartTime.Add(time.Second)
	r.Bind(next)
	nextKey, ok := r.shadowBindingKey(next)
	if !ok {
		t.Fatal("replacement binding lacks a shadow key")
	}
	if r.recordShadowMatches(old, oldKey, now, one) {
		t.Fatal("a replaced instance must not accept matches")
	}
	if !r.recordShadowMatches(next, nextKey, now, one) {
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
