package unit

import (
	"fmt"
	"testing"
	"time"

	"elida/internal/correlation"
)

func observeAll(e *correlation.Engine, session string, evs ...correlation.Event) []correlation.Match {
	var out []correlation.Match
	for _, ev := range evs {
		out = append(out, e.Observe(session, []correlation.Event{ev})...)
	}
	return out
}

func TestCorrelationSequence_InOrderMatches(t *testing.T) {
	e := newCorr(t, okLimits(), sequenceRule("chain", 10*time.Minute, "a", "b", "c"))
	m := observeAll(e, "s", cev("a1", "a", "", 0), cev("b1", "b", "", time.Minute), cev("c1", "c", "", 2*time.Minute))
	if len(m) != 1 || fmt.Sprint(m[0].EventIDs) != "[a1 b1 c1]" {
		t.Fatalf("want one match [a1 b1 c1], got %+v", m)
	}
	if fmt.Sprint(m[0].Categories) != "[a b c]" || m[0].Shape != correlation.ShapeSequence {
		t.Fatalf("categories/shape wrong: %+v", m[0])
	}
	if m[0].ID != correlation.MatchID("chain", m[0].EventIDs) {
		t.Fatal("match ID must be MatchID(rule, ordered ids)")
	}
}

func TestCorrelationSequence_OutOfOrderArrivalStillMatches(t *testing.T) {
	// The middle step's result (an async semantic decision) lands last.
	e := newCorr(t, okLimits(), sequenceRule("chain", 10*time.Minute, "a", "b", "c"))
	m := observeAll(e, "s", cev("c1", "c", "", 2*time.Minute), cev("a1", "a", "", 0), cev("b1", "b", "", time.Minute))
	if len(m) != 1 || fmt.Sprint(m[0].EventIDs) != "[a1 b1 c1]" {
		t.Fatalf("time order, not arrival order, decides the sequence: got %+v", m)
	}
}

func TestCorrelationSequence_WrongOrderDoesNotMatch(t *testing.T) {
	e := newCorr(t, okLimits(), sequenceRule("chain", 10*time.Minute, "a", "b", "c"))
	if m := observeAll(e, "s", cev("c1", "c", "", 0), cev("b1", "b", "", time.Minute), cev("a1", "a", "", 2*time.Minute)); len(m) != 0 {
		t.Fatalf("c, b, a is not a, b, c: got %+v", m)
	}
}

func TestCorrelationSequence_LateStepCompletesAnEarlierChain(t *testing.T) {
	e := newCorr(t, okLimits(), sequenceRule("chain", 10*time.Minute, "a", "b", "c"))
	// a@0 .. c@11m is too wide.
	if m := observeAll(e, "s", cev("a0", "a", "", 0), cev("b5", "b", "", 5*time.Minute), cev("c11", "c", "", 11*time.Minute)); len(m) != 0 {
		t.Fatalf("span 11m > 10m must not match, got %+v", m)
	}
	// a@6m and then b@7m arrive late; c@11m is already there.
	m := observeAll(e, "s", cev("a6", "a", "", 6*time.Minute), cev("b7", "b", "", 7*time.Minute))
	if len(m) != 1 || fmt.Sprint(m[0].EventIDs) != "[a6 b7 c11]" {
		t.Fatalf("want [a6 b7 c11] (smallest span through the new event), got %+v", m)
	}
}

func TestCorrelationSequence_EachEventUsedOnce(t *testing.T) {
	r := sequenceRule("chain", 10*time.Minute, "a", "b", "c")
	r.Cooldown = time.Nanosecond
	e := newCorr(t, okLimits(), r)
	m := observeAll(e, "s",
		cev("a1", "a", "", 0), cev("b1", "b", "", 1*time.Second), cev("c1", "c", "", 2*time.Second),
		cev("a2", "a", "", 3*time.Second), cev("b2", "b", "", 4*time.Second), cev("c2", "c", "", 5*time.Second),
	)
	if len(m) != 2 {
		t.Fatalf("want two disjoint matches, got %+v", m)
	}
	if fmt.Sprint(m[0].EventIDs) != "[a1 b1 c1]" || fmt.Sprint(m[1].EventIDs) != "[a2 b2 c2]" {
		t.Fatalf("matches must not share events: %+v", m)
	}
	// One more c cannot reuse a2/b2.
	if more := observeAll(e, "s", cev("c3", "c", "", 6*time.Second)); len(more) != 0 {
		t.Fatalf("consumed events must not contribute again, got %+v", more)
	}
}

func TestCorrelationSequence_RepeatedStepNeedsTwoEvents(t *testing.T) {
	e := newCorr(t, okLimits(), sequenceRule("twice_then_b", 10*time.Minute, "a", "a", "b"))
	if m := observeAll(e, "s", cev("a1", "a", "", 0), cev("b1", "b", "", time.Minute), cev("a2", "a", "", 2*time.Minute)); len(m) != 0 {
		t.Fatalf("a, b, a is not a, a, b: got %+v", m)
	}
	m := observeAll(e, "s", cev("b2", "b", "", 3*time.Minute))
	if len(m) != 1 || fmt.Sprint(m[0].EventIDs) != "[a1 a2 b2]" {
		t.Fatalf("want [a1 a2 b2], got %+v", m)
	}
}

func TestCorrelationSequence_CooldownSuppresses(t *testing.T) {
	e := newCorr(t, okLimits(), sequenceRule("chain", 10*time.Minute, "a", "b")) // cooldown = window
	m := observeAll(e, "s",
		cev("a1", "a", "", 0), cev("b1", "b", "", time.Second),
		cev("a2", "a", "", 2*time.Second), cev("b2", "b", "", 3*time.Second),
	)
	if len(m) != 1 {
		t.Fatalf("want one match inside the cooldown, got %+v", m)
	}
	if e.Stats().CooldownSuppressed == 0 {
		t.Fatal("the second chain must be counted as suppressed")
	}
}

func TestCorrelationSequence_ConsumptionIsPerRule(t *testing.T) {
	// D10: one event may contribute to one match of each rule.
	e := newCorr(t, okLimits(),
		repeatedRule("two_a", "a", 2, 10*time.Minute),
		sequenceRule("a_then_b", 10*time.Minute, "a", "b"),
	)
	m := observeAll(e, "s", cev("a1", "a", "", 0), cev("a2", "a", "", time.Second), cev("b1", "b", "", 2*time.Second))
	if len(m) != 2 {
		t.Fatalf("want one match per rule, got %+v", m)
	}
	byRule := map[string]string{}
	for _, x := range m {
		byRule[x.Rule] = fmt.Sprint(x.EventIDs)
	}
	if byRule["two_a"] != "[a1 a2]" || byRule["a_then_b"] != "[a2 b1]" {
		t.Fatalf("unexpected matches: %v", byRule)
	}
}

func TestCorrelationSequence_EqualTimestampsUseArrivalOrder(t *testing.T) {
	e := newCorr(t, okLimits(), sequenceRule("chain", time.Minute, "a", "b"))
	m := observeAll(e, "s", cev("a1", "a", "", 0), cev("b1", "b", "", 0))
	if len(m) != 1 {
		t.Fatalf("a then b at the same instant, in arrival order, is a sequence: got %+v", m)
	}
}

// Tests below go beyond the brief.

// Steps in different sessions never chain, whichever shard they land on.
func TestCorrelationSequence_SessionsAreIndependent(t *testing.T) {
	e := newCorr(t, okLimits(), sequenceRule("chain", 10*time.Minute, "a", "b"))
	if m := observeAll(e, "s1", cev("a1", "a", "", 0)); len(m) != 0 {
		t.Fatalf("one step is not a chain, got %+v", m)
	}
	if m := observeAll(e, "s2", cev("b1", "b", "", time.Second)); len(m) != 0 {
		t.Fatalf("a in s1 and b in s2 must not chain, got %+v", m)
	}
	if m := observeAll(e, "s1", cev("b2", "b", "", 2*time.Second)); len(m) != 1 || fmt.Sprint(m[0].EventIDs) != "[a1 b2]" {
		t.Fatalf("want [a1 b2] in s1, got %+v", m)
	}
}

var seqArrivalOrders = [][]string{
	{"a", "b", "c"}, {"a", "c", "b"}, {"b", "a", "c"},
	{"b", "c", "a"}, {"c", "a", "b"}, {"c", "b", "a"},
}

func seqEvents(order []string, at map[string]time.Duration) []correlation.Event {
	evs := make([]correlation.Event, 0, len(order))
	for _, c := range order {
		evs = append(evs, cev(c+"1", c, "", at[c]))
	}
	return evs
}

// Timestamps in reverse step order never form a sequence, whatever order
// the events arrive in, one per call or all in one call.
func TestCorrelationSequence_ReversedTimestampsNeverMatch(t *testing.T) {
	at := map[string]time.Duration{"a": 2 * time.Minute, "b": time.Minute, "c": 0}
	for _, order := range seqArrivalOrders {
		evs := seqEvents(order, at)
		e := newCorr(t, okLimits(), sequenceRule("chain", 10*time.Minute, "a", "b", "c"))
		if m := observeAll(e, "s", evs...); len(m) != 0 {
			t.Fatalf("arrival %v of c<b<a timestamps must never match, got %+v", order, m)
		}
		e = newCorr(t, okLimits(), sequenceRule("chain", 10*time.Minute, "a", "b", "c"))
		if m := e.Observe("s", evs); len(m) != 0 {
			t.Fatalf("batch arrival %v must never match, got %+v", order, m)
		}
	}
}

// Every arrival order of a chain in time order matches exactly once, with
// the steps reported in time order.
func TestCorrelationSequence_AnyArrivalOrderMatchesOnce(t *testing.T) {
	at := map[string]time.Duration{"a": 0, "b": time.Minute, "c": 2 * time.Minute}
	for _, order := range seqArrivalOrders {
		evs := seqEvents(order, at)
		e := newCorr(t, okLimits(), sequenceRule("chain", 10*time.Minute, "a", "b", "c"))
		m := observeAll(e, "s", evs...)
		if len(m) != 1 || fmt.Sprint(m[0].EventIDs) != "[a1 b1 c1]" || fmt.Sprint(m[0].Categories) != "[a b c]" {
			t.Fatalf("arrival %v: want one match [a1 b1 c1], got %+v", order, m)
		}
		if !m[0].FirstEvent.Equal(corrT0) || !m[0].LastEvent.Equal(corrT0.Add(2*time.Minute)) {
			t.Fatalf("arrival %v: first/last wrong: %+v", order, m[0])
		}
		e = newCorr(t, okLimits(), sequenceRule("chain", 10*time.Minute, "a", "b", "c"))
		if m := e.Observe("s", evs); len(m) != 1 || fmt.Sprint(m[0].EventIDs) != "[a1 b1 c1]" {
			t.Fatalf("batch arrival %v: want one match [a1 b1 c1], got %+v", order, m)
		}
	}
}

// The window is inclusive: first and last exactly Window apart match, one
// nanosecond more does not.
func TestCorrelationSequence_WindowIsInclusive(t *testing.T) {
	e := newCorr(t, okLimits(), sequenceRule("chain", time.Minute, "a", "b"))
	if m := observeAll(e, "s", cev("a1", "a", "", 0), cev("b1", "b", "", time.Minute)); len(m) != 1 {
		t.Fatalf("exactly Window apart must match, got %+v", m)
	}
	e = newCorr(t, okLimits(), sequenceRule("chain", time.Minute, "a", "b"))
	if m := observeAll(e, "s", cev("a1", "a", "", 0), cev("b1", "b", "", time.Minute+time.Nanosecond)); len(m) != 0 {
		t.Fatalf("Window + 1ns apart must not match, got %+v", m)
	}
}

// When the earliest-ending chain is inside the cooldown, a chain through a
// later last step that clears it still matches, as repeated windows do.
func TestCorrelationSequence_LaterLastStepClearsCooldown(t *testing.T) {
	r := sequenceRule("chain", 10*time.Minute, "a", "b")
	r.Cooldown = 5 * time.Minute
	e := newCorr(t, okLimits(), r)
	if m := observeAll(e, "s", cev("a1", "a", "", 0), cev("b1", "b", "", time.Second)); len(m) != 1 {
		t.Fatalf("first chain must match, got %+v", m)
	}
	// b2 (inside the cooldown) and b3 (after it) are there when a2 lands.
	if m := observeAll(e, "s", cev("b2", "b", "", 3*time.Minute), cev("b3", "b", "", 6*time.Minute)); len(m) != 0 {
		t.Fatalf("no unconsumed a before b2/b3 yet, got %+v", m)
	}
	m := observeAll(e, "s", cev("a2", "a", "", 2*time.Minute))
	if len(m) != 1 || fmt.Sprint(m[0].EventIDs) != "[a2 b3]" {
		t.Fatalf("want [a2 b3] (b2 is inside the cooldown), got %+v", m)
	}
}

// Events past MaxEventsPerObserve are never inspected, so they cannot
// complete a chain.
func TestCorrelationSequence_EventsPastTheCapAreNotSteps(t *testing.T) {
	e := newCorr(t, okLimits(), sequenceRule("chain", time.Hour, "a", "b"))
	evs := make([]correlation.Event, 0, correlation.MaxEventsPerObserve+1)
	for i := 0; i < correlation.MaxEventsPerObserve; i++ {
		evs = append(evs, cev(fmt.Sprintf("a%d", i), "a", "", time.Duration(i)*time.Millisecond))
	}
	evs = append(evs, cev("b_late", "b", "", time.Second))
	if m := e.Observe("s", evs); len(m) != 0 {
		t.Fatalf("the dropped b must not complete a chain, got %+v", m)
	}
	if e.Stats().EventsDropped != 1 {
		t.Fatalf("want one dropped event, got %+v", e.Stats())
	}
	if m := e.Observe("s", []correlation.Event{cev("b1", "b", "", time.Second)}); len(m) != 1 || m[0].EventIDs[1] != "b1" {
		t.Fatalf("a b in a later call completes the chain, got %+v", m)
	}
}

// Steady-state evaluation of a sequence rule allocates nothing per event
// when it produces no match: the shard scratch is reused.
func TestCorrelationSequence_EvaluationDoesNotAllocate(t *testing.T) {
	e := newCorr(t, correlation.Limits{MaxHistoryEvents: 64, MaxSessions: 16},
		sequenceRule("chain", time.Hour, "a", "b", "c", "d", "e", "f", "g", "h"))
	evs := make([][]correlation.Event, 512)
	for i := range evs {
		// Only a and b arrive, so the eight-step chain never completes, but
		// every event walks the history in both directions.
		cat := "a"
		if i%2 == 1 {
			cat = "b"
		}
		evs[i] = []correlation.Event{cev(fmt.Sprintf("id%04d", i), cat, "", time.Duration(i)*time.Millisecond)}
	}
	i := 0
	for ; i < 128; i++ { // fill the history to its cap
		e.Observe("s", evs[i])
	}
	allocs := testing.AllocsPerRun(256, func() {
		if m := e.Observe("s", evs[i]); len(m) != 0 {
			t.Fatalf("unexpected match %+v", m)
		}
		i++
	})
	if allocs != 0 {
		t.Fatalf("sequence evaluation allocates %v per event, want 0", allocs)
	}
}

// Task 3 review M2: the cooldown-suppressed path, through repickLast,
// allocates nothing per event either. After one match the rule is in
// cooldown; the history is full of later b events, and each measured event
// is an a arriving late (async) with an earlier timestamp. It fills step 0,
// the forward walk picks the earliest b, which is inside the cooldown, so
// evaluation re-picks the last step (repickLast), finds no b that clears
// the cooldown, and suppresses.
func TestCorrelationSequence_SuppressedAndRepickPathsDoNotAllocate(t *testing.T) {
	r := sequenceRule("chain", time.Hour, "a", "b")
	r.Cooldown = time.Hour
	e := newCorr(t, correlation.Limits{MaxHistoryEvents: 1024, MaxSessions: 16}, r)
	if m := e.Observe("s", []correlation.Event{cev("a0", "a", "", 0), cev("b0", "b", "", time.Millisecond)}); len(m) != 1 {
		t.Fatalf("fixture: the first chain must match, got %+v", m)
	}
	for i := 0; i < 64; i++ { // fill the history with later b events
		if m := e.Observe("s", []correlation.Event{cev(fmt.Sprintf("b%04d", i), "b", "", 10*time.Second+time.Duration(i)*time.Millisecond)}); len(m) != 0 {
			t.Fatalf("fixture: a b alone must not match, got %+v", m)
		}
	}
	evs := make([][]correlation.Event, 512)
	for i := range evs {
		evs[i] = []correlation.Event{cev(fmt.Sprintf("a%04d", i), "a", "", 5*time.Second+time.Duration(i)*time.Microsecond)}
	}
	i := 0
	for ; i < 16; i++ { // warm up the late-a path
		e.Observe("s", evs[i])
	}
	before := e.Stats().CooldownSuppressed
	allocs := testing.AllocsPerRun(256, func() {
		if m := e.Observe("s", evs[i]); len(m) != 0 {
			t.Fatalf("unexpected match inside the cooldown %+v", m)
		}
		i++
	})
	if got := e.Stats().CooldownSuppressed; got <= before {
		t.Fatalf("fixture: the measured events must take the suppressed path (CooldownSuppressed %d -> %d)", before, got)
	}
	if allocs != 0 {
		t.Fatalf("suppressed sequence evaluation allocates %v per event, want 0", allocs)
	}
}
