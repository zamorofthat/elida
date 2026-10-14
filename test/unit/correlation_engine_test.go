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

// The tests below come from the Task 2 review fix round.

// D10: consumption is per rule, so one event counts toward each rule once.
func TestCorrelationRepeated_OneEventCountsTowardDifferentRules(t *testing.T) {
	e := newCorr(t, okLimits(),
		repeatedRule("pair", "a", 2, 10*time.Minute),
		repeatedRule("triple", "a", 3, 10*time.Minute))
	var all []correlation.Match
	for i := 0; i < 3; i++ {
		all = append(all, e.Observe("s", []correlation.Event{cev(fmt.Sprintf("e%d", i), "a", "", time.Duration(i)*time.Second)})...)
	}
	got := map[string]string{}
	for _, m := range all {
		got[m.Rule] = fmt.Sprint(m.EventIDs)
	}
	if len(all) != 2 || got["pair"] != "[e0 e1]" || got["triple"] != "[e0 e1 e2]" {
		t.Fatalf("want pair [e0 e1] and triple [e0 e1 e2] sharing e0 and e1, got %+v", all)
	}
}

// A late event can complete a window that ends after the cooldown even when
// the earliest-ending window containing it is inside the cooldown.
func TestCorrelationRepeated_LateEventCompletesWindowPastCooldown(t *testing.T) {
	burst := repeatedRule("burst", "a", 2, 10*time.Minute)
	burst.Cooldown = 5 * time.Minute
	long := repeatedRule("long", "z", 2, time.Hour) // raises the history horizon only
	e := newCorr(t, okLimits(), burst, long)
	obs := func(id string, at time.Duration) []correlation.Match {
		return e.Observe("s", []correlation.Event{cev(id, "a", "", at)})
	}
	obs("e1", 0)
	if m := obs("e2", time.Minute); len(m) != 1 { // cooldown runs to 6m
		t.Fatalf("e1 e2 must match, got %+v", m)
	}
	obs("x", 2*time.Minute)
	if m := obs("z", 13*time.Minute); len(m) != 0 { // [x z] spans 11m
		t.Fatalf("[x z] is wider than the window, got %+v", m)
	}
	// w arrives late: [x w] ends inside the cooldown, [w z] spans exactly
	// the window and ends after it.
	m := obs("w", 3*time.Minute)
	if len(m) != 1 || fmt.Sprint(m[0].EventIDs) != "[w z]" {
		t.Fatalf("want [w z], got %+v", m)
	}
	if s := e.Stats().CooldownSuppressed; s != 0 {
		t.Fatalf("a window cleared the cooldown, so nothing is counted as suppressed, got %d", s)
	}
}

// Window and cooldown boundaries are inclusive.
func TestCorrelationRepeated_BoundariesAreInclusive(t *testing.T) {
	e := newCorr(t, okLimits(), repeatedRule("pair", "a", 2, 10*time.Minute))
	e.Observe("at", []correlation.Event{cev("a1", "a", "", 0)})
	if m := e.Observe("at", []correlation.Event{cev("a2", "a", "", 10*time.Minute)}); len(m) != 1 {
		t.Fatalf("a span of exactly the window matches, got %+v", m)
	}
	e.Observe("past", []correlation.Event{cev("p1", "a", "", 0)})
	if m := e.Observe("past", []correlation.Event{cev("p2", "a", "", 10*time.Minute+time.Nanosecond)}); len(m) != 0 {
		t.Fatalf("a span one nanosecond over the window does not match, got %+v", m)
	}

	r := repeatedRule("cd", "a", 2, 10*time.Minute)
	r.Cooldown = 5 * time.Minute
	for _, c := range []struct {
		name  string
		last  time.Duration
		match bool
	}{{"edge", 6 * time.Minute, true}, {"inside", 6*time.Minute - time.Nanosecond, false}} {
		e := newCorr(t, okLimits(), r)
		e.Observe("s", []correlation.Event{cev("c1", "a", "", 0), cev("c2", "a", "", time.Minute)}) // cooldown to 6m
		e.Observe("s", []correlation.Event{cev("c3", "a", "", 5*time.Minute)})
		m := e.Observe("s", []correlation.Event{cev("c4", "a", "", c.last)})
		if (len(m) == 1) != c.match {
			t.Fatalf("%s: a candidate ending at %v, cooldown until 6m: match=%v, got %+v", c.name, c.last, c.match, m)
		}
	}
}

// count_by message: a consumed message cannot count again for the rule
// until the rule's window has elapsed since the match, even after its
// events leave the history.
func TestCorrelationRepeated_CountByMessageConsumedGroupWaitsForWindow(t *testing.T) {
	r := repeatedRule("slow_burn", "injection_elevated", 3, 15*time.Minute)
	r.CountBy = correlation.CountByMessage
	r.Cooldown = time.Nanosecond // isolate consumption from the cooldown
	e := newCorr(t, okLimits(), r)
	obs := func(id, group string, at time.Duration) []correlation.Match {
		return e.Observe("s", []correlation.Event{cev(id, "injection_elevated", group, at)})
	}
	obs("A1", "tool#1", 0)
	obs("B1", "tool#2", 30*time.Second)
	if m := obs("C1", "tool#3", time.Minute); len(m) != 1 { // tool#1..3 consumed until 1m + 15m
		t.Fatalf("[A1 B1 C1] must match, got %+v", m)
	}
	obs("D1", "tool#4", 10*time.Minute)
	obs("E1", "tool#5", 12*time.Minute)
	// A1 (at 0) is pruned from the history at 15m30s, but tool#1 is still
	// consumed: [D1 E1 A2] must not match.
	if m := obs("A2", "tool#1", 15*time.Minute+30*time.Second); len(m) != 0 {
		t.Fatalf("tool#1 already counted for slow_burn at 1m; it cannot count again before 16m, got %+v", m)
	}
	if m := obs("A3", "tool#1", 16*time.Minute); len(m) != 0 {
		t.Fatalf("the consumption boundary is inclusive (16m is still blocked), got %+v", m)
	}
	m := obs("A4", "tool#1", 16*time.Minute+time.Second)
	if len(m) != 1 || fmt.Sprint(m[0].EventIDs) != "[D1 E1 A4]" {
		t.Fatalf("after the window has elapsed tool#1 counts again: want [D1 E1 A4], got %+v", m)
	}
}

func TestCorrelationEngine_ObserveCapsEventsPerCall(t *testing.T) {
	e := newCorr(t, correlation.Limits{MaxHistoryEvents: correlation.MaxHistoryEventsCeiling, MaxSessions: 8},
		repeatedRule("burst", "a", 32, time.Hour))
	batch := make([]correlation.Event, correlation.MaxEventsPerObserve+44)
	for i := range batch {
		batch[i] = cev(fmt.Sprintf("e%d", i), "a", "", time.Duration(i)*time.Millisecond)
	}
	e.Observe("s", batch)
	st := e.Stats()
	if st.EventsObserved != correlation.MaxEventsPerObserve || st.EventsDropped != 44 || st.HistoryEvents != correlation.MaxEventsPerObserve {
		t.Fatalf("want %d observed and 44 dropped, got %+v", correlation.MaxEventsPerObserve, st)
	}
}

func TestCorrelationEngine_ShardsSplitTheSessionCap(t *testing.T) {
	if s := newCorr(t, okLimits(), repeatedRule("b", "a", 2, time.Hour)).Stats().Shards; s != correlation.MaxShards {
		t.Fatalf("the default session cap uses %d shards, got %d", correlation.MaxShards, s)
	}
	if s := newCorr(t, correlation.Limits{MaxHistoryEvents: 8, MaxSessions: 8}, repeatedRule("b", "a", 2, time.Hour)).Stats().Shards; s != 1 {
		t.Fatalf("a small session cap keeps one exact LRU, got %d shards", s)
	}
	e := newCorr(t, okLimits(), repeatedRule("b", "a", 2, time.Hour))
	const n = 5000
	for i := 0; i < n; i++ {
		e.Observe(fmt.Sprintf("s%d", i), []correlation.Event{cev(fmt.Sprintf("e%d", i), "a", "", 0)})
	}
	st := e.Stats()
	if st.Sessions > correlation.DefaultMaxSessions || st.Sessions+int(st.SessionsEvicted) != n || st.HistoryEvents != st.Sessions {
		t.Fatalf("sessions must stay within the cap and every eviction be counted: %+v", st)
	}
	for i := 0; i < n; i++ {
		e.Forget(fmt.Sprintf("s%d", i))
	}
	if st := e.Stats(); st.Sessions != 0 || st.HistoryEvents != 0 {
		t.Fatalf("Forget must empty every shard: %+v", st)
	}
}

func TestCorrelationEngine_ConcurrentAcrossShards(t *testing.T) {
	e := newCorr(t, okLimits(), repeatedRule("burst", "a", 3, time.Hour))
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			sid := fmt.Sprintf("s%d", g)
			for i := 0; i < 20; i++ {
				m := e.Observe(sid, []correlation.Event{cev(fmt.Sprintf("g%d-%d", g, i), "a", "", time.Duration(i)*time.Second)})
				mu.Lock()
				total += len(m)
				mu.Unlock()
				if i%7 == 0 {
					_ = e.Stats()
				}
			}
			e.Forget(sid)
		}(g)
	}
	wg.Wait()
	if total != 64 {
		t.Fatalf("want one match per session, got %d", total)
	}
	if st := e.Stats(); st.Sessions != 0 || st.HistoryEvents != 0 {
		t.Fatalf("every session was forgotten: %+v", st)
	}
}
