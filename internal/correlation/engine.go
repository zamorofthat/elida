package correlation

import (
	"container/list"
	"errors"
	"hash/maphash"
	"sort"
	"sync"
	"time"
)

// Stats are cumulative engine counters plus current sizes. No content.
type Stats struct {
	Rules         int `json:"rules"`
	Shards        int `json:"shards"`
	Sessions      int `json:"sessions"`
	HistoryEvents int `json:"history_events"`
	// EventsObserved counts events added to a history.
	EventsObserved uint64 `json:"events_observed"`
	// EventsIgnored counts events with no ID or a category no rule wants.
	EventsIgnored   uint64 `json:"events_ignored"`
	DuplicateEvents uint64 `json:"duplicate_events"`
	// EventsDropped counts events past MaxEventsPerObserve in one Observe
	// call. They are never inspected.
	EventsDropped      uint64 `json:"events_dropped"`
	HistoryEvicted     uint64 `json:"history_evicted"`
	SessionsEvicted    uint64 `json:"sessions_evicted"`
	Matches            uint64 `json:"matches"`
	CooldownSuppressed uint64 `json:"cooldown_suppressed"`
	DuplicateMatches   uint64 `json:"duplicate_matches"`
}

// MaxShards is the number of session shards the engine uses at the default
// session cap. Sessions are assigned to a shard by a keyed hash of the
// session ID (hash/maphash with a per-engine random seed, so a client that
// chooses its session IDs cannot aim them at one shard). Each shard has its
// own mutex, its own LRU and an equal share of Limits.MaxSessions, so
// Observe calls for sessions on different shards never wait for each
// other.
//
// A shard keeps at least minSessionsPerShard sessions: below
// MaxShards x minSessionsPerShard sessions the shard count halves until each
// shard holds that many (one shard below 32 sessions), so a small cap keeps
// an exact, global LRU.
//
// The LRU is per shard: when a shard is full, its least recently observed
// session is evicted even if another shard has room. The total never
// exceeds Limits.MaxSessions.
const MaxShards = 64

const minSessionsPerShard = 16

// MaxEventsPerObserve caps the events one Observe call accepts. Events past
// it are dropped without inspection and counted in Stats.EventsDropped. It
// bounds the work done under a shard lock and how far one call can grow a
// history before it is pruned (MaxHistoryEvents + MaxEventsPerObserve
// entries).
const MaxEventsPerObserve = 256

// shardCount returns the number of shards for a session cap.
func shardCount(maxSessions int) int {
	n := MaxShards
	for n > 1 && maxSessions/n < minSessionsPerShard {
		n /= 2
	}
	return n
}

// Engine evaluates correlation rules over bounded per-session histories.
// It is safe for concurrent use. Rules are fixed at New. Evaluation runs
// synchronously inside Observe; there is no background goroutine and no
// timer: time comes only from event timestamps.
//
// Every piece of state is bounded (see the memory table on
// DefaultMaxHistoryEvents):
//   - sessions: at most Limits.MaxSessions in total, split evenly across
//     the shards; past a shard's share its least recently observed session
//     is evicted (counted in SessionsEvicted);
//   - history per session: at most Limits.MaxHistoryEvents entries after
//     each Observe (MaxEventsPerObserve more during one), and none older
//     than the newest event minus the largest rule window (counted in
//     HistoryEvicted). The budget is shared by every wanted category: see
//     Limits.MaxHistoryEvents;
//   - emitted match IDs per session: at most MaxMatchIDsPerSession 16-byte
//     digests, oldest evicted first;
//   - consumed message groups per session, across all count_by message
//     rules: at most Limits.MaxHistoryEvents, each dropped once no event it
//     could still block can arrive (earliest expiry dropped first past the
//     cap);
//   - last-match times per session: one per rule (at most MaxRules),
//     allocated at the session's first match.
type Engine struct {
	rules     []Rule          // normalized, immutable
	wants     map[string]bool // categories any rule references, immutable
	maxWindow time.Duration
	limits    Limits

	seed      maphash.Seed // shard selection
	groupSeed maphash.Seed // Event.Group digests
	idSeed    maphash.Seed // Event.ID digests (an event with no Group)

	shards    []*shard
	shardMask uint64 // len(shards)-1; the shard count is a power of two
}

// shard is one independently locked slice of the sessions.
type shard struct {
	mu          sync.Mutex
	sessions    map[string]*list.Element // value: *sessionState
	lru         *list.List               // front = most recently observed
	maxSessions int
	history     int // sum of len(history) over the shard's sessions
	stats       Stats

	// Scratch reused across evaluations under mu, so evaluation does not
	// allocate. Sized by the largest history seen.
	fresh   []uint64
	elig    []int
	seen    map[uint64]struct{}
	seqCur  [MaxSequenceSteps]int // sequence: the chain being built
	seqBest [MaxSequenceSteps]int // sequence: the best chain so far
}

type consumedKey struct {
	rule  int
	group uint64
}

type entry struct {
	ev       Event
	group    uint64 // digest of the CountByMessage key: Group, or ID if none
	seq      uint64
	consumed uint64 // bit i: already contributed to a match of rules[i]
}

type sessionState struct {
	id        string
	history   []entry // sorted by (Timestamp, seq)
	seq       uint64
	newest    time.Time
	lastMatch []time.Time // per rule: LastEvent of its latest match; nil until the first
	matchIDs  map[[16]byte]struct{}
	matchRing [][16]byte
	matchNext int
	// consumed holds, per (CountByMessage rule, group), the time until
	// which the group is consumed for the rule: the consuming match's
	// LastEvent plus the rule's Window. nil until a rule first consumes.
	consumed map[consumedKey]time.Time
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
		rules:     make([]Rule, len(rules)),
		wants:     make(map[string]bool),
		limits:    limits,
		seed:      maphash.MakeSeed(),
		groupSeed: maphash.MakeSeed(),
		idSeed:    maphash.MakeSeed(),
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
	n := shardCount(limits.MaxSessions)
	e.shards = make([]*shard, n)
	if n > 1 {
		e.shardMask = uint64(n - 1)
	}
	for i := range e.shards {
		share := limits.MaxSessions / n
		if i < limits.MaxSessions%n {
			share++
		}
		e.shards[i] = &shard{
			sessions:    make(map[string]*list.Element),
			lru:         list.New(),
			maxSessions: share,
			seen:        make(map[uint64]struct{}),
		}
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

func (e *Engine) shardFor(sessionID string) *shard {
	if len(e.shards) == 1 {
		return e.shards[0]
	}
	return e.shards[maphash.String(e.seed, sessionID)&e.shardMask]
}

// groupOf is the digest of an event's CountByMessage key: its Group, or its
// own ID when it has none (the event is then its own group). Two seeds keep
// a Group from colliding with an equal ID by construction.
func (e *Engine) groupOf(ev Event) uint64 {
	if ev.Group == "" {
		return maphash.String(e.idSeed, ev.ID)
	}
	return maphash.String(e.groupSeed, ev.Group)
}

// Observe adds events to the session's history and returns the matches they
// complete. Every returned match includes at least one of these events.
// Events with no ID, a category no rule references, or an ID already in
// the history are ignored and counted. At most MaxEventsPerObserve events
// are accepted per call; the rest are dropped and counted.
func (e *Engine) Observe(sessionID string, events []Event) []Match {
	if e == nil || len(events) == 0 {
		return nil
	}
	sh := e.shardFor(sessionID)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	if dropped := len(events) - MaxEventsPerObserve; dropped > 0 {
		sh.stats.EventsDropped += uint64(dropped)
		events = events[:MaxEventsPerObserve]
	}

	var st *sessionState
	before := 0
	fresh := sh.fresh[:0]
	for _, ev := range events {
		if ev.ID == "" || !e.wants[ev.Category] {
			sh.stats.EventsIgnored++
			continue
		}
		if st == nil {
			st = e.stateLocked(sh, sessionID)
			before = len(st.history)
		}
		if st.has(ev.ID) {
			sh.stats.DuplicateEvents++
			continue
		}
		sh.stats.EventsObserved++
		fresh = append(fresh, st.insert(ev, e.groupOf(ev)))
	}
	sh.fresh = fresh
	if st == nil || len(fresh) == 0 {
		return nil
	}
	sh.stats.HistoryEvicted += st.prune(e.maxWindow, e.limits.MaxHistoryEvents)
	sh.history += len(st.history) - before

	var matches []Match
	for _, seq := range fresh {
		at := st.indexOf(seq)
		if at < 0 {
			continue // pruned on arrival: older than any window
		}
		for ri := range e.rules {
			if m, ok := e.evaluateLocked(sh, st, ri, at); ok {
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
	sh := e.shardFor(sessionID)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if el, ok := sh.sessions[sessionID]; ok {
		sh.history -= len(stateOf(el).history)
		sh.lru.Remove(el)
		delete(sh.sessions, sessionID)
	}
}

// Stats returns the counters and current sizes summed over the shards. Each
// shard is read under its own lock, so the sum is not one atomic snapshot.
func (e *Engine) Stats() Stats {
	if e == nil {
		return Stats{}
	}
	s := Stats{Rules: len(e.rules), Shards: len(e.shards)}
	for _, sh := range e.shards {
		sh.mu.Lock()
		c := sh.stats
		s.Sessions += len(sh.sessions)
		s.HistoryEvents += sh.history
		sh.mu.Unlock()
		s.EventsObserved += c.EventsObserved
		s.EventsIgnored += c.EventsIgnored
		s.DuplicateEvents += c.DuplicateEvents
		s.EventsDropped += c.EventsDropped
		s.HistoryEvicted += c.HistoryEvicted
		s.SessionsEvicted += c.SessionsEvicted
		s.Matches += c.Matches
		s.CooldownSuppressed += c.CooldownSuppressed
		s.DuplicateMatches += c.DuplicateMatches
	}
	return s
}

// stateLocked returns the session's state, creating it and evicting the
// shard's least recently observed session past its share of MaxSessions.
// The caller holds sh.mu.
func (e *Engine) stateLocked(sh *shard, id string) *sessionState {
	if el, ok := sh.sessions[id]; ok {
		sh.lru.MoveToFront(el)
		return stateOf(el)
	}
	st := &sessionState{id: id}
	sh.sessions[id] = sh.lru.PushFront(st)
	for len(sh.sessions) > sh.maxSessions {
		back := sh.lru.Back()
		old := stateOf(back)
		sh.history -= len(old.history)
		sh.lru.Remove(back)
		delete(sh.sessions, old.id)
		sh.stats.SessionsEvicted++
	}
	return st
}

// stateOf returns the session state an LRU element holds. Every element is
// pushed by stateLocked with a *sessionState, so the assertion cannot fail.
func stateOf(el *list.Element) *sessionState {
	st, _ := el.Value.(*sessionState)
	return st
}

// inCooldown reports whether a candidate whose last event is at last falls
// inside rule ri's cooldown. The boundary is inclusive on the allowed side:
// a candidate ending exactly Cooldown after the previous match's last event
// may match.
func (s *sessionState) inCooldown(ri int, r Rule, last time.Time) bool {
	if s.lastMatch == nil {
		return false
	}
	prev := s.lastMatch[ri]
	return !prev.IsZero() && last.Before(prev.Add(r.Cooldown))
}

// evaluateLocked tries rule ri with history[at] as a required contributor.
func (e *Engine) evaluateLocked(sh *shard, st *sessionState, ri, at int) (Match, bool) {
	r := e.rules[ri]
	bit := uint64(1) << uint(ri)
	if st.history[at].consumed&bit != 0 {
		return Match{}, false
	}
	var picked []int
	suppressed := false
	switch r.Shape {
	case ShapeRepeated:
		if st.history[at].ev.Category == r.Category {
			picked, suppressed = sh.repeated(st, ri, r, bit, at)
		}
	case ShapeSequence:
		picked, suppressed = sh.sequence(st, ri, r, bit, at)
	}
	if len(picked) == 0 {
		if suppressed {
			sh.stats.CooldownSuppressed++
		}
		return Match{}, false
	}

	first := st.history[picked[0]].ev.Timestamp
	last := st.history[picked[len(picked)-1]].ev.Timestamp
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
	// duplicates), so the digest receives distinct IDs.
	digest := matchDigest(r.Name, m.EventIDs)
	m.ID = matchIDFromDigest(digest)
	e.consume(sh, st, ri, r, bit, picked, last)
	if _, dup := st.matchIDs[digest]; dup {
		sh.stats.DuplicateMatches++
		return Match{}, false
	}
	st.rememberMatch(digest)
	if st.lastMatch == nil {
		st.lastMatch = make([]time.Time, len(e.rules))
	}
	st.lastMatch[ri] = last
	sh.stats.Matches++
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
func (s *sessionState) insert(ev Event, group uint64) uint64 {
	s.seq++
	i := sort.Search(len(s.history), func(i int) bool {
		return s.history[i].ev.Timestamp.After(ev.Timestamp)
	})
	s.history = append(s.history, entry{})
	copy(s.history[i+1:], s.history[i:])
	s.history[i] = entry{ev: ev, group: group, seq: s.seq}
	if ev.Timestamp.After(s.newest) {
		s.newest = ev.Timestamp
	}
	return s.seq
}

// prune drops entries older than newest-maxWindow (an entry exactly at the
// floor is kept, so windows are inclusive), then the oldest entries past
// maxEvents. It returns how many entries were dropped. It also drops
// consumed message groups that can no longer block anything: an event they
// could block (Timestamp <= consumedAt + Window) is now older than the
// floor and would be pruned on arrival.
func (s *sessionState) prune(maxWindow time.Duration, maxEvents int) uint64 {
	floor := s.newest.Add(-maxWindow)
	for k, until := range s.consumed {
		if until.Before(floor) {
			delete(s.consumed, k)
		}
	}

	cut := 0
	var dropped uint64
	for cut < len(s.history) && s.history[cut].ev.Timestamp.Before(floor) {
		cut++
		dropped++
	}
	for len(s.history)-cut > maxEvents {
		cut++
		dropped++
	}
	if cut == 0 {
		return 0
	}
	n := copy(s.history, s.history[cut:])
	clear(s.history[n:]) // release the dropped events' strings
	s.history = s.history[:n]
	return dropped
}

func (s *sessionState) indexOf(seq uint64) int {
	for i := range s.history {
		if s.history[i].seq == seq {
			return i
		}
	}
	return -1
}

// groupBlocked reports whether a CountByMessage rule ri already consumed the
// group of h and its window has not elapsed since that match: the event
// does not count until h.Timestamp > consumedAt + Window.
func (s *sessionState) groupBlocked(ri int, h *entry) bool {
	until, ok := s.consumed[consumedKey{rule: ri, group: h.group}]
	return ok && !h.ev.Timestamp.After(until)
}

// repeated returns the history indexes of a repeated-rule match that
// includes history[at], or nil, and whether a window was found but every
// fitting window was inside the cooldown. The returned slice aliases the
// shard scratch and is valid until the next evaluation.
//
// Eligible events are the rule's category, not yet consumed by it; under
// CountByMessage one per group (its earliest), and none from a group the
// rule consumed within the last Window (see groupBlocked). Of the windows
// of Count consecutive eligible events that contain at, it takes the one
// that ends earliest, fits in Window (inclusive: last - first <= Window)
// and is not in cooldown.
func (sh *shard) repeated(st *sessionState, ri int, r Rule, bit uint64, at int) ([]int, bool) {
	// Cheap pass: give up before building candidates when the rule's
	// unconsumed events cannot reach Count.
	n := 0
	for i := range st.history {
		h := &st.history[i]
		if h.ev.Category == r.Category && h.consumed&bit == 0 {
			n++
		}
	}
	if n < r.Count {
		return nil, false
	}

	byMessage := r.CountBy == CountByMessage
	if byMessage {
		clear(sh.seen)
	}
	elig := sh.elig[:0]
	pos := -1
	for i := range st.history {
		h := &st.history[i]
		if h.ev.Category != r.Category || h.consumed&bit != 0 {
			continue
		}
		if byMessage {
			if st.groupBlocked(ri, h) {
				continue
			}
			if _, dup := sh.seen[h.group]; dup {
				continue
			}
			sh.seen[h.group] = struct{}{}
		}
		if i == at {
			pos = len(elig)
		}
		elig = append(elig, i)
	}
	sh.elig = elig
	if pos < 0 {
		return nil, false // at adds no new group, or its group is blocked
	}
	suppressed := false
	for end := pos; end < len(elig) && end < pos+r.Count; end++ {
		start := end - r.Count + 1
		if start < 0 {
			continue
		}
		last := st.history[elig[end]].ev.Timestamp
		if last.Sub(st.history[elig[start]].ev.Timestamp) > r.Window {
			continue
		}
		if st.inCooldown(ri, r, last) {
			suppressed = true
			continue // a later-ending window may clear the cooldown
		}
		return elig[start : end+1], false
	}
	return nil, suppressed
}

// consume marks the picked entries as used by rule ri. Under CountByMessage
// every event of each picked group is consumed, and each group is recorded
// with the match's last event time, so the message cannot count toward
// another match of the rule until the rule's window has elapsed since this
// one, even after its events leave the history. At most MaxHistoryEvents
// groups are recorded per session across rules; past that the record that
// expires first is dropped, which can let that message count again early.
func (e *Engine) consume(sh *shard, st *sessionState, ri int, r Rule, bit uint64, picked []int, last time.Time) {
	if r.Shape != ShapeRepeated || r.CountBy != CountByMessage {
		for _, i := range picked {
			st.history[i].consumed |= bit
		}
		return
	}
	clear(sh.seen)
	for _, i := range picked {
		sh.seen[st.history[i].group] = struct{}{}
	}
	for i := range st.history {
		h := &st.history[i]
		if h.ev.Category == r.Category {
			if _, ok := sh.seen[h.group]; ok {
				h.consumed |= bit
			}
		}
	}
	if st.consumed == nil {
		st.consumed = make(map[consumedKey]time.Time)
	}
	until := last.Add(r.Window)
	for g := range sh.seen {
		st.consumed[consumedKey{rule: ri, group: g}] = until
	}
	for len(st.consumed) > e.limits.MaxHistoryEvents {
		st.dropEarliestConsumed()
	}
}

// dropEarliestConsumed drops the consumed-group record that expires first.
// It runs only past the cap, at a match.
func (s *sessionState) dropEarliestConsumed() {
	var victim consumedKey
	var earliest time.Time
	first := true
	for k, until := range s.consumed {
		if first || until.Before(earliest) {
			victim, earliest, first = k, until, false
		}
	}
	delete(s.consumed, victim)
}

// rememberMatch records an emitted match digest, evicting the oldest past
// MaxMatchIDsPerSession.
func (s *sessionState) rememberMatch(d [16]byte) {
	if s.matchIDs == nil {
		s.matchIDs = make(map[[16]byte]struct{})
	}
	if len(s.matchRing) < MaxMatchIDsPerSession {
		s.matchRing = append(s.matchRing, d)
	} else {
		delete(s.matchIDs, s.matchRing[s.matchNext])
		s.matchRing[s.matchNext] = d
		s.matchNext = (s.matchNext + 1) % MaxMatchIDsPerSession
	}
	s.matchIDs[d] = struct{}{}
}
