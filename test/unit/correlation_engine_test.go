package unit

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"elida/internal/correlation"
)

var corrT0 = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func cev(id, cat, group string, at time.Duration) correlation.Event {
	return correlation.Event{ID: id, Category: cat, Group: group, Timestamp: corrT0.Add(at)}
}

func newCorr(t *testing.T, limits correlation.Limits, rules ...correlation.Rule) *correlation.Engine {
	t.Helper()
	e, err := correlation.New(rules, limits)
	if err != nil {
		t.Fatalf("correlation.New: %v", err)
	}
	return e
}

func TestCorrelationEngine_NewRejectsInvalidRules(t *testing.T) {
	if _, err := correlation.New([]correlation.Rule{repeatedRule("x", "a", 1, time.Minute)}, okLimits()); err == nil {
		t.Fatal("New must refuse rules Validate rejects")
	}
}

func TestCorrelationEngine_NormalizesDefaults(t *testing.T) {
	e := newCorr(t, okLimits(), repeatedRule("r", "a", 3, 10*time.Minute))
	r := e.Rules()[0]
	if r.Cooldown != 10*time.Minute || r.Severity != "warning" || r.Mode != correlation.ModeAudit || r.CountBy != correlation.CountByEvent {
		t.Fatalf("defaults not applied: %+v", r)
	}
	if !e.Wants("a") || e.Wants("b") || e.Wants(correlation.CategoryMatch) || e.Wants("") {
		t.Fatal("Wants must report exactly the referenced categories")
	}
	var nilEngine *correlation.Engine
	if nilEngine.Wants("a") || nilEngine.Observe("s", []correlation.Event{cev("x", "a", "", 0)}) != nil {
		t.Fatal("a nil engine is inert")
	}
}

func TestCorrelationRepeated_MatchesAtCount(t *testing.T) {
	e := newCorr(t, okLimits(), repeatedRule("burst", "a", 3, 10*time.Minute))
	if m := e.Observe("s", []correlation.Event{cev("e1", "a", "", 0), cev("e2", "a", "", time.Minute)}); len(m) != 0 {
		t.Fatalf("two of three must not match, got %+v", m)
	}
	m := e.Observe("s", []correlation.Event{cev("e3", "a", "", 2*time.Minute)})
	if len(m) != 1 {
		t.Fatalf("third event must complete the match, got %+v", m)
	}
	got := m[0]
	if fmt.Sprint(got.EventIDs) != "[e1 e2 e3]" {
		t.Fatalf("EventIDs = %v, want time order [e1 e2 e3]", got.EventIDs)
	}
	if got.ID != correlation.MatchID("burst", got.EventIDs) {
		t.Fatalf("match ID %q is not MatchID(rule, ids)", got.ID)
	}
	if got.Rule != "burst" || got.Shape != correlation.ShapeRepeated || got.Mode != correlation.ModeAudit || got.Severity != "warning" {
		t.Fatalf("match metadata wrong: %+v", got)
	}
	if !got.FirstEvent.Equal(corrT0) || !got.LastEvent.Equal(corrT0.Add(2*time.Minute)) {
		t.Fatalf("first/last = %v/%v", got.FirstEvent, got.LastEvent)
	}
}

func TestCorrelationRepeated_OutsideWindowDoesNotMatch(t *testing.T) {
	e := newCorr(t, okLimits(), repeatedRule("burst", "a", 3, 10*time.Minute))
	e.Observe("s", []correlation.Event{cev("e1", "a", "", 0), cev("e2", "a", "", 6*time.Minute)})
	if m := e.Observe("s", []correlation.Event{cev("e3", "a", "", 12*time.Minute)}); len(m) != 0 {
		t.Fatalf("e1..e3 span 12m > 10m window, got %+v", m)
	}
	m := e.Observe("s", []correlation.Event{cev("e4", "a", "", 13*time.Minute)})
	if len(m) != 1 || fmt.Sprint(m[0].EventIDs) != "[e2 e3 e4]" {
		t.Fatalf("want [e2 e3 e4], got %+v", m)
	}
}

func TestCorrelationRepeated_EachEventUsedOnce(t *testing.T) {
	r := repeatedRule("burst", "a", 3, 10*time.Minute)
	r.Cooldown = time.Nanosecond
	e := newCorr(t, okLimits(), r)
	var all []correlation.Match
	for i := 0; i < 6; i++ {
		all = append(all, e.Observe("s", []correlation.Event{cev(fmt.Sprintf("e%d", i), "a", "", time.Duration(i)*time.Second)})...)
	}
	if len(all) != 2 {
		t.Fatalf("six events, count 3, tiny cooldown: want 2 matches, got %d (%+v)", len(all), all)
	}
	seen := map[string]bool{}
	for _, m := range all {
		for _, id := range m.EventIDs {
			if seen[id] {
				t.Fatalf("event %s contributed twice", id)
			}
			seen[id] = true
		}
	}
}

func TestCorrelationRepeated_CooldownSuppresses(t *testing.T) {
	e := newCorr(t, okLimits(), repeatedRule("burst", "a", 3, 10*time.Minute)) // cooldown = window
	var n int
	for i := 0; i < 6; i++ {
		n += len(e.Observe("s", []correlation.Event{cev(fmt.Sprintf("e%d", i), "a", "", time.Duration(i)*time.Second)}))
	}
	if n != 1 {
		t.Fatalf("within the cooldown only one match, got %d", n)
	}
	if e.Stats().CooldownSuppressed == 0 {
		t.Fatal("suppressed candidates must be counted")
	}
	// Past the cooldown, a fresh burst matches again.
	for i := 0; i < 3; i++ {
		n += len(e.Observe("s", []correlation.Event{cev(fmt.Sprintf("late%d", i), "a", "", 11*time.Minute+time.Duration(i)*time.Second)}))
	}
	if n != 2 {
		t.Fatalf("after the cooldown a second match, got %d total", n)
	}
}

func TestCorrelationRepeated_CountByMessageCountsDistinctMessages(t *testing.T) {
	r := repeatedRule("slow_burn", "injection_elevated", 3, 15*time.Minute)
	r.CountBy = correlation.CountByMessage
	e := newCorr(t, okLimits(), r)
	// One long message: five elevated windows, one group.
	var batch []correlation.Event
	for i := 0; i < 5; i++ {
		batch = append(batch, cev(fmt.Sprintf("m1w%d", i), "injection_elevated", "tool#1", time.Duration(i)*time.Millisecond))
	}
	if m := e.Observe("s", batch); len(m) != 0 {
		t.Fatalf("one message is not a slow burn, got %+v", m)
	}
	if m := e.Observe("s", []correlation.Event{cev("m2", "injection_elevated", "tool#2", time.Minute)}); len(m) != 0 {
		t.Fatalf("two messages are not enough, got %+v", m)
	}
	m := e.Observe("s", []correlation.Event{cev("m3", "injection_elevated", "user#3", 2*time.Minute)})
	if len(m) != 1 || len(m[0].EventIDs) != 3 {
		t.Fatalf("three distinct messages must match once with three IDs, got %+v", m)
	}
	if m[0].EventIDs[0] != "m1w0" {
		t.Fatalf("a group is represented by its earliest event, got %v", m[0].EventIDs)
	}
}

func TestCorrelationEngine_IgnoresUnwantedAndDuplicates(t *testing.T) {
	e := newCorr(t, okLimits(), repeatedRule("burst", "a", 2, time.Minute))
	e.Observe("s", []correlation.Event{cev("x", "unrelated", "", 0), cev("", "a", "", 0), cev("y", correlation.CategoryMatch, "", 0)})
	st := e.Stats()
	if st.EventsIgnored != 3 || st.Sessions != 0 {
		t.Fatalf("unwanted, ID-less and match events are ignored and create no state: %+v", st)
	}
	e.Observe("s", []correlation.Event{cev("e1", "a", "", 0)})
	if m := e.Observe("s", []correlation.Event{cev("e1", "a", "", time.Second)}); len(m) != 0 {
		t.Fatalf("a duplicate event ID must not count twice, got %+v", m)
	}
	if e.Stats().DuplicateEvents != 1 {
		t.Fatalf("duplicates must be counted: %+v", e.Stats())
	}
}

func TestCorrelationEngine_HistoryIsBounded(t *testing.T) {
	limits := correlation.Limits{MaxHistoryEvents: 4, MaxSessions: 8}
	e := newCorr(t, limits, repeatedRule("burst", "a", 3, time.Hour))
	for i := 0; i < 50; i++ {
		e.Observe("s", []correlation.Event{cev(fmt.Sprintf("e%d", i), "a", "", time.Duration(i)*time.Second)})
	}
	st := e.Stats()
	if st.HistoryEvents > 4 {
		t.Fatalf("history must stay at or below 4 events, got %d", st.HistoryEvents)
	}
	if st.HistoryEvicted == 0 {
		t.Fatal("evictions must be counted")
	}
}

func TestCorrelationEngine_HistoryIsPrunedByAge(t *testing.T) {
	e := newCorr(t, okLimits(), repeatedRule("burst", "a", 3, 10*time.Minute))
	e.Observe("s", []correlation.Event{cev("old1", "a", "", 0), cev("old2", "a", "", time.Second)})
	e.Observe("s", []correlation.Event{cev("new", "a", "", time.Hour)})
	if st := e.Stats(); st.HistoryEvents != 1 {
		t.Fatalf("events older than the largest window are pruned, history = %d", st.HistoryEvents)
	}
}

func TestCorrelationEngine_SessionsAreBounded(t *testing.T) {
	limits := correlation.Limits{MaxHistoryEvents: 8, MaxSessions: 3}
	e := newCorr(t, limits, repeatedRule("burst", "a", 3, time.Hour))
	e.Observe("victim", []correlation.Event{cev("v1", "a", "", 0), cev("v2", "a", "", time.Second)})
	for i := 0; i < 5; i++ {
		e.Observe(fmt.Sprintf("spam-%d", i), []correlation.Event{cev(fmt.Sprintf("s%d", i), "a", "", 0)})
	}
	st := e.Stats()
	if st.Sessions != 3 {
		t.Fatalf("sessions must stay at 3, got %d", st.Sessions)
	}
	if st.SessionsEvicted != 3 {
		t.Fatalf("want 3 evictions (victim + 2 spam), got %d", st.SessionsEvicted)
	}
	// The evicted session's history is gone: one more event is not a match.
	if m := e.Observe("victim", []correlation.Event{cev("v3", "a", "", 2*time.Second)}); len(m) != 0 {
		t.Fatalf("an evicted session must not match on lost history, got %+v", m)
	}
}

func TestCorrelationEngine_Forget(t *testing.T) {
	e := newCorr(t, okLimits(), repeatedRule("burst", "a", 2, time.Hour))
	e.Observe("s", []correlation.Event{cev("e1", "a", "", 0)})
	e.Forget("s")
	if e.Stats().Sessions != 0 {
		t.Fatal("Forget must drop the session")
	}
	if m := e.Observe("s", []correlation.Event{cev("e2", "a", "", time.Second)}); len(m) != 0 {
		t.Fatalf("a forgotten session starts empty, got %+v", m)
	}
}

func TestCorrelationEngine_ConcurrentSessions(t *testing.T) {
	e := newCorr(t, okLimits(), repeatedRule("burst", "a", 3, time.Hour))
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				m := e.Observe(fmt.Sprintf("s%d", g), []correlation.Event{cev(fmt.Sprintf("g%d-%d", g, i), "a", "", time.Duration(i)*time.Second)})
				mu.Lock()
				total += len(m)
				mu.Unlock()
				_ = e.Stats()
			}
		}(g)
	}
	wg.Wait()
	if total != 8 { // one match per session; the rest are inside the one-hour cooldown
		t.Fatalf("want 8 matches, got %d", total)
	}
}

// The tests below are additions to the brief: they pin out-of-order
// tolerance, LRU recency and the dedup-set bound.

func TestCorrelationRepeated_OutOfOrderArrival(t *testing.T) {
	e := newCorr(t, okLimits(), repeatedRule("burst", "a", 3, 10*time.Minute))
	e.Observe("s", []correlation.Event{cev("e3", "a", "", 2*time.Minute)})
	e.Observe("s", []correlation.Event{cev("e1", "a", "", 0)})
	m := e.Observe("s", []correlation.Event{cev("e2", "a", "", time.Minute)})
	if len(m) != 1 || fmt.Sprint(m[0].EventIDs) != "[e1 e2 e3]" {
		t.Fatalf("arrival order must not matter, want [e1 e2 e3], got %+v", m)
	}
	if !m[0].FirstEvent.Equal(corrT0) || !m[0].LastEvent.Equal(corrT0.Add(2*time.Minute)) {
		t.Fatalf("first/last = %v/%v", m[0].FirstEvent, m[0].LastEvent)
	}
}

func TestCorrelationEngine_SessionLRUKeepsRecentlyObserved(t *testing.T) {
	limits := correlation.Limits{MaxHistoryEvents: 8, MaxSessions: 2}
	e := newCorr(t, limits, repeatedRule("burst", "a", 2, time.Hour))
	e.Observe("keep", []correlation.Event{cev("k1", "a", "", 0)})
	e.Observe("other", []correlation.Event{cev("o1", "a", "", 0)})
	e.Observe("keep", []correlation.Event{cev("k0", "a", "", -2*time.Hour)}) // pruned on arrival, but touches the LRU
	e.Observe("new", []correlation.Event{cev("n1", "a", "", 0)})             // evicts "other", not "keep"
	if st := e.Stats(); st.SessionsEvicted != 1 || st.Sessions != 2 {
		t.Fatalf("want 1 eviction and 2 sessions, got %+v", st)
	}
	if m := e.Observe("keep", []correlation.Event{cev("k2", "a", "", time.Second)}); len(m) != 1 {
		t.Fatalf("the recently observed session must keep its history, got %+v", m)
	}
}

func TestCorrelationEngine_MatchDedupSetIsBounded(t *testing.T) {
	limits := correlation.Limits{MaxHistoryEvents: 2, MaxSessions: 8}
	r := repeatedRule("pair", "a", 2, time.Minute)
	r.Cooldown = time.Nanosecond
	e := newCorr(t, limits, r)
	pair := func(i int, at time.Duration) []correlation.Match {
		e.Observe("s", []correlation.Event{cev(fmt.Sprintf("p%da", i), "a", "", at)})
		return e.Observe("s", []correlation.Event{cev(fmt.Sprintf("p%db", i), "a", "", at+time.Second)})
	}
	n := correlation.MaxMatchIDsPerSession + 1
	for i := 0; i < n; i++ {
		if m := pair(i, time.Duration(2*i)*time.Second); len(m) != 1 {
			t.Fatalf("pair %d must match once, got %+v", i, m)
		}
	}
	// History holds two events, so every pair but the newest is pruned. Re-observing
	// the second newest is caught by the dedup set; the oldest has left it.
	later := time.Duration(4*n) * time.Second
	if m := pair(n-2, later); len(m) != 0 {
		t.Fatalf("a re-observed pair inside the dedup set must not match again, got %+v", m)
	}
	if e.Stats().DuplicateMatches != 1 {
		t.Fatalf("duplicate matches must be counted: %+v", e.Stats())
	}
	if m := pair(0, later+time.Minute); len(m) != 1 {
		t.Fatalf("the dedup set is bounded at %d: the oldest ID was evicted, got %+v", correlation.MaxMatchIDsPerSession, m)
	}
}
