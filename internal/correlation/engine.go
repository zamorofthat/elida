package correlation

import (
	"container/list"
	"errors"
	"sort"
	"sync"
	"time"
)

// Stats are cumulative engine counters plus current sizes. No content.
type Stats struct {
	Rules              int    `json:"rules"`
	Sessions           int    `json:"sessions"`
	HistoryEvents      int    `json:"history_events"`
	EventsObserved     uint64 `json:"events_observed"`
	EventsIgnored      uint64 `json:"events_ignored"`
	DuplicateEvents    uint64 `json:"duplicate_events"`
	HistoryEvicted     uint64 `json:"history_evicted"`
	SessionsEvicted    uint64 `json:"sessions_evicted"`
	Matches            uint64 `json:"matches"`
	CooldownSuppressed uint64 `json:"cooldown_suppressed"`
	DuplicateMatches   uint64 `json:"duplicate_matches"`
}

// Engine evaluates correlation rules over bounded per-session histories.
// It is safe for concurrent use. Rules are fixed at New. Evaluation runs
// synchronously inside Observe; there is no background goroutine.
//
// Every piece of state is bounded:
//   - sessions: at most Limits.MaxSessions, least recently observed evicted
//     first (counted in SessionsEvicted);
//   - history per session: at most Limits.MaxHistoryEvents entries, and
//     none older than the newest event minus the largest rule window
//     (counted in HistoryEvicted);
//   - emitted match IDs per session: at most MaxMatchIDsPerSession, oldest
//     evicted first;
//   - last-match times per session: one per rule (at most MaxRules).
type Engine struct {
	rules     []Rule          // normalized, immutable
	wants     map[string]bool // categories any rule references, immutable
	maxWindow time.Duration
	limits    Limits

	mu       sync.Mutex
	sessions map[string]*list.Element // value: *sessionState
	lru      *list.List               // front = most recently observed
	stats    Stats
}

type entry struct {
	ev       Event
	seq      uint64
	consumed uint64 // bit i: already contributed to a match of rules[i]
}

type sessionState struct {
	id        string
	history   []entry // sorted by (Timestamp, seq)
	seq       uint64
	newest    time.Time
	lastMatch []time.Time // per rule: LastEvent of its latest match
	matchIDs  map[string]struct{}
	matchRing []string
	matchNext int
}

// New validates rules and limits and returns an engine. Zero values are
// normalized: CountBy event (repeated rules), Cooldown = Window, Severity
// warning, Mode audit.
func New(rules []Rule, limits Limits) (*Engine, error) {
	if errs := Validate(rules, limits); len(errs) > 0 {
		joined := make([]error, len(errs))
		for i := range errs {
			joined[i] = errs[i]
		}
		return nil, errors.Join(joined...)
	}
	e := &Engine{
		rules:    make([]Rule, len(rules)),
		wants:    make(map[string]bool),
		limits:   limits,
		sessions: make(map[string]*list.Element),
		lru:      list.New(),
	}
	for i, r := range rules {
		r.Sequence = append([]string(nil), r.Sequence...)
		if r.Shape == ShapeRepeated && r.CountBy == "" {
			r.CountBy = CountByEvent
		}
		if r.Cooldown == 0 {
			r.Cooldown = r.Window
		}
		if r.Severity == "" {
			r.Severity = "warning"
		}
		if r.Mode == "" {
			r.Mode = ModeAudit
		}
		if r.Shape == ShapeRepeated {
			e.wants[r.Category] = true
		} else {
			for _, c := range r.Sequence {
				e.wants[c] = true
			}
		}
		if r.Window > e.maxWindow {
			e.maxWindow = r.Window
		}
		e.rules[i] = r
	}
	return e, nil
}

// Wants reports whether any rule references category. Callers use it to
// skip building events nothing will read. It never wants CategoryMatch.
func (e *Engine) Wants(category string) bool {
	if e == nil {
		return false
	}
	return e.wants[category]
}

// Rules returns copies of the normalized rules.
func (e *Engine) Rules() []Rule {
	if e == nil {
		return nil
	}
	out := make([]Rule, len(e.rules))
	for i, r := range e.rules {
		r.Sequence = append([]string(nil), r.Sequence...)
		out[i] = r
	}
	return out
}

// Observe adds events to the session's history and returns the matches they
// complete. Every returned match includes at least one of these events.
// Events with no ID, a category no rule references, or an ID already in
// the history are ignored and counted.
func (e *Engine) Observe(sessionID string, events []Event) []Match {
	if e == nil || len(events) == 0 {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	var st *sessionState
	var fresh []uint64
	for _, ev := range events {
		if ev.ID == "" || !e.wants[ev.Category] {
			e.stats.EventsIgnored++
			continue
		}
		if st == nil {
			st = e.stateLocked(sessionID)
		}
		if st.has(ev.ID) {
			e.stats.DuplicateEvents++
			continue
		}
		e.stats.EventsObserved++
		fresh = append(fresh, st.insert(ev))
	}
	if st == nil || len(fresh) == 0 {
		return nil
	}
	e.stats.HistoryEvicted += uint64(st.prune(e.maxWindow, e.limits.MaxHistoryEvents))

	var matches []Match
	for _, seq := range fresh {
		at := st.indexOf(seq)
		if at < 0 {
			continue // pruned on arrival: older than any window
		}
		for ri := range e.rules {
			if m, ok := e.evaluateLocked(st, ri, at); ok {
				matches = append(matches, m)
			}
		}
	}
	return matches
}

// Forget drops a session's state. The session-end path calls it.
func (e *Engine) Forget(sessionID string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if el, ok := e.sessions[sessionID]; ok {
		e.lru.Remove(el)
		delete(e.sessions, sessionID)
	}
}

// Stats returns a snapshot of the counters and current sizes.
func (e *Engine) Stats() Stats {
	if e == nil {
		return Stats{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.stats
	s.Rules = len(e.rules)
	s.Sessions = len(e.sessions)
	for _, el := range e.sessions {
		s.HistoryEvents += len(stateOf(el).history)
	}
	return s
}

// stateLocked returns the session's state, creating it and evicting the
// least recently observed session past MaxSessions. The caller holds mu.
func (e *Engine) stateLocked(id string) *sessionState {
	if el, ok := e.sessions[id]; ok {
		e.lru.MoveToFront(el)
		return stateOf(el)
	}
	st := &sessionState{
		id:        id,
		lastMatch: make([]time.Time, len(e.rules)),
		matchIDs:  make(map[string]struct{}),
	}
	e.sessions[id] = e.lru.PushFront(st)
	for len(e.sessions) > e.limits.MaxSessions {
		back := e.lru.Back()
		old := stateOf(back)
		e.lru.Remove(back)
		delete(e.sessions, old.id)
		e.stats.SessionsEvicted++
	}
	return st
}

// stateOf returns the session state an LRU element holds. Every element is
// pushed by stateLocked with a *sessionState, so the assertion cannot fail.
func stateOf(el *list.Element) *sessionState {
	st, _ := el.Value.(*sessionState)
	return st
}

// evaluateLocked tries rule ri with history[at] as a required contributor.
func (e *Engine) evaluateLocked(st *sessionState, ri, at int) (Match, bool) {
	r := e.rules[ri]
	bit := uint64(1) << uint(ri)
	if st.history[at].consumed&bit != 0 {
		return Match{}, false
	}
	var picked []int
	switch r.Shape {
	case ShapeRepeated:
		if st.history[at].ev.Category == r.Category {
			picked = st.repeated(r, bit, at)
		}
	case ShapeSequence:
		picked = st.sequence(r, bit, at)
	}
	if len(picked) == 0 {
		return Match{}, false
	}

	first := st.history[picked[0]].ev.Timestamp
	last := st.history[picked[len(picked)-1]].ev.Timestamp
	if prev := st.lastMatch[ri]; !prev.IsZero() && last.Before(prev.Add(r.Cooldown)) {
		e.stats.CooldownSuppressed++
		return Match{}, false
	}

	m := Match{
		Rule:       r.Name,
		Shape:      r.Shape,
		Severity:   r.Severity,
		Mode:       r.Mode,
		EventIDs:   make([]string, len(picked)),
		Categories: make([]string, len(picked)),
		FirstEvent: first,
		LastEvent:  last,
	}
	for i, idx := range picked {
		m.EventIDs[i] = st.history[idx].ev.ID
		m.Categories[i] = st.history[idx].ev.Category
	}
	// History never holds two entries with one ID (Observe drops
	// duplicates), so MatchID receives distinct IDs.
	m.ID = MatchID(r.Name, m.EventIDs)
	st.consume(r, bit, picked)
	if _, dup := st.matchIDs[m.ID]; dup {
		e.stats.DuplicateMatches++
		return Match{}, false
	}
	st.rememberMatch(m.ID)
	st.lastMatch[ri] = last
	e.stats.Matches++
	return m, true
}

func (s *sessionState) has(id string) bool {
	for i := range s.history {
		if s.history[i].ev.ID == id {
			return true
		}
	}
	return false
}

// insert places ev after every entry with Timestamp <= ev.Timestamp, so the
// history stays ordered by (Timestamp, arrival). It returns the entry's seq.
func (s *sessionState) insert(ev Event) uint64 {
	s.seq++
	i := sort.Search(len(s.history), func(i int) bool {
		return s.history[i].ev.Timestamp.After(ev.Timestamp)
	})
	s.history = append(s.history, entry{})
	copy(s.history[i+1:], s.history[i:])
	s.history[i] = entry{ev: ev, seq: s.seq}
	if ev.Timestamp.After(s.newest) {
		s.newest = ev.Timestamp
	}
	return s.seq
}

// prune drops entries older than newest-maxWindow, then the oldest entries
// past maxEvents. It returns how many were dropped.
func (s *sessionState) prune(maxWindow time.Duration, maxEvents int) int {
	floor := s.newest.Add(-maxWindow)
	cut := 0
	for cut < len(s.history) && s.history[cut].ev.Timestamp.Before(floor) {
		cut++
	}
	if over := len(s.history) - cut - maxEvents; over > 0 {
		cut += over
	}
	if cut == 0 {
		return 0
	}
	s.history = append(s.history[:0], s.history[cut:]...)
	return cut
}

func (s *sessionState) indexOf(seq uint64) int {
	for i := range s.history {
		if s.history[i].seq == seq {
			return i
		}
	}
	return -1
}

// groupKey is the counting key under CountByMessage: the event's Group, or
// its own ID when it has none.
func groupKey(ev Event) string {
	if ev.Group == "" {
		return "id\x00" + ev.ID
	}
	return "g\x00" + ev.Group
}

// repeated returns the history indexes of a repeated-rule match that
// includes history[at], or nil. Eligible events are the rule's category,
// not yet consumed by it; under CountByMessage one per group (its earliest)
// and none from a group the rule already consumed. Of the windows of Count
// consecutive eligible events that contain at, it takes the one that ends
// earliest and fits in Window.
func (s *sessionState) repeated(r Rule, bit uint64, at int) []int {
	byMessage := r.CountBy == CountByMessage
	var used map[string]bool
	if byMessage {
		used = make(map[string]bool)
		for i := range s.history {
			h := s.history[i]
			if h.ev.Category == r.Category && h.consumed&bit != 0 {
				used[groupKey(h.ev)] = true
			}
		}
	}

	var elig []int
	seen := make(map[string]bool)
	pos := -1
	for i := range s.history {
		h := s.history[i]
		if h.ev.Category != r.Category || h.consumed&bit != 0 {
			continue
		}
		if byMessage {
			g := groupKey(h.ev)
			if used[g] || seen[g] {
				continue
			}
			seen[g] = true
		}
		if i == at {
			pos = len(elig)
		}
		elig = append(elig, i)
	}
	if pos < 0 {
		return nil // at adds no new group: its group is already represented
	}
	for end := pos; end < len(elig) && end < pos+r.Count; end++ {
		start := end - r.Count + 1
		if start < 0 {
			continue
		}
		span := s.history[elig[end]].ev.Timestamp.Sub(s.history[elig[start]].ev.Timestamp)
		if span <= r.Window {
			return append([]int(nil), elig[start:end+1]...)
		}
	}
	return nil
}

// consume marks the picked entries as used by the rule. Under
// CountByMessage every event of each picked group is consumed, so one
// message can never count toward two matches of the rule.
func (s *sessionState) consume(r Rule, bit uint64, picked []int) {
	if r.Shape == ShapeRepeated && r.CountBy == CountByMessage {
		groups := make(map[string]bool, len(picked))
		for _, i := range picked {
			groups[groupKey(s.history[i].ev)] = true
		}
		for i := range s.history {
			if s.history[i].ev.Category == r.Category && groups[groupKey(s.history[i].ev)] {
				s.history[i].consumed |= bit
			}
		}
		return
	}
	for _, i := range picked {
		s.history[i].consumed |= bit
	}
}

// rememberMatch records an emitted match ID, evicting the oldest past
// MaxMatchIDsPerSession.
func (s *sessionState) rememberMatch(id string) {
	if len(s.matchRing) < MaxMatchIDsPerSession {
		s.matchRing = append(s.matchRing, id)
	} else {
		delete(s.matchIDs, s.matchRing[s.matchNext])
		s.matchRing[s.matchNext] = id
		s.matchNext = (s.matchNext + 1) % MaxMatchIDsPerSession
	}
	s.matchIDs[id] = struct{}{}
}

// sequence is implemented in sequence.go (Task 3). Until then a sequence
// rule never matches.
func (s *sessionState) sequence(r Rule, bit uint64, at int) []int { return nil }
