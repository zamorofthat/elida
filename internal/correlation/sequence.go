package correlation

import "time"

// sequence returns the history indexes, in step order, of a sequence-rule
// match in which history[at] fills some step, or nil, and whether a chain
// fit in Window but every fitting chain was inside the cooldown. The
// returned slice aliases the shard scratch and is valid until the next
// evaluation.
//
// History order is (Timestamp, arrival), so steps follow event time, not
// arrival: an async result that lands after later events still fills its
// time position, and equal timestamps follow arrival order. history[at] may
// fill any step whose category is its own, not only the last.
//
// For each such step k, the prefix takes, walking back from at, the latest
// event of each earlier step not yet consumed by the rule, and the suffix
// takes, walking forward, the earliest of each later step. For a fixed k
// that is the smallest span of any chain with history[at] at step k, so if
// any such chain fits in Window (inclusive: last - first <= Window), this
// one does. If its last event is inside the cooldown, the last step is
// re-picked as the earliest event that clears the cooldown and still fits,
// as a repeated rule tries a later-ending window. Among the steps k that
// yield a chain, the one ending earliest wins.
//
// Cost per (event, rule): for each of the at most MaxSequenceSteps steps k
// whose category is history[at]'s, the backward walk and the forward walk
// each move one index monotonically, so together they visit each history
// entry at most once, and the cooldown re-pick visits each at most once
// more: O(steps x history), at most 8 x 2 x (MaxHistoryEvents +
// MaxEventsPerObserve) entry visits. It is linear in the history for any
// rule length, never quadratic, and allocates nothing.
func (sh *shard) sequence(st *sessionState, ri int, r Rule, bit uint64, at int) ([]int, bool) {
	x := st.history[at].ev
	n := len(r.Sequence)
	cur := sh.seqCur[:n]
	var bestLast time.Time
	found, suppressed := false, false
	for k, step := range r.Sequence {
		if step != x.Category {
			continue
		}
		cur[k] = at
		if !st.fillBackward(r, bit, cur, k, x.Timestamp.Add(-r.Window)) {
			continue
		}
		first := st.history[cur[0]].ev.Timestamp
		limit := first.Add(r.Window) // inclusive
		if !st.fillForward(r, bit, cur, k, limit) {
			continue
		}
		last := st.history[cur[n-1]].ev.Timestamp
		if st.inCooldown(ri, r, last) {
			suppressed = true
			if k == n-1 || !st.repickLast(ri, r, bit, cur, limit) {
				continue // the last step is history[at] or nothing clears
			}
			last = st.history[cur[n-1]].ev.Timestamp
		}
		if !found || last.Before(bestLast) {
			copy(sh.seqBest[:n], cur)
			bestLast, found = last, true
		}
	}
	if !found {
		return nil, suppressed
	}
	return sh.seqBest[:n], false
}

// eligible reports whether history[i] can fill step j of rule r: it has the
// step's category and the rule has not consumed it.
func (s *sessionState) eligible(r Rule, bit uint64, i, j int) bool {
	h := &s.history[i]
	return h.consumed&bit == 0 && h.ev.Category == r.Sequence[j]
}

// fillBackward picks steps k-1 .. 0, each the latest eligible event strictly
// before the previous pick and no earlier than floor (a pick before floor
// cannot fit in Window with picked[k]).
func (s *sessionState) fillBackward(r Rule, bit uint64, picked []int, k int, floor time.Time) bool {
	i := picked[k] - 1
	for j := k - 1; j >= 0; j-- {
		for i >= 0 && !s.eligible(r, bit, i, j) {
			if s.history[i].ev.Timestamp.Before(floor) {
				return false
			}
			i--
		}
		if i < 0 || s.history[i].ev.Timestamp.Before(floor) {
			return false
		}
		picked[j] = i
		i--
	}
	return true
}

// fillForward picks steps k+1 .. n-1, each the earliest eligible event
// strictly after the previous pick and no later than limit.
func (s *sessionState) fillForward(r Rule, bit uint64, picked []int, k int, limit time.Time) bool {
	i := picked[k] + 1
	for j := k + 1; j < len(r.Sequence); j++ {
		for i < len(s.history) && !s.eligible(r, bit, i, j) {
			if s.history[i].ev.Timestamp.After(limit) {
				return false
			}
			i++
		}
		if i >= len(s.history) || s.history[i].ev.Timestamp.After(limit) {
			return false
		}
		picked[j] = i
		i++
	}
	return true
}

// repickLast replaces the last pick, which is inside the cooldown, with the
// earliest later eligible event that clears the cooldown and is no later
// than limit. The earlier picks stay: the prefix picks are the latest
// possible (maximizing first, hence limit) and the suffix picks the
// earliest, so any chain that clears the cooldown can end where this one
// does.
func (s *sessionState) repickLast(ri int, r Rule, bit uint64, picked []int, limit time.Time) bool {
	last := len(r.Sequence) - 1
	for i := picked[last] + 1; i < len(s.history); i++ {
		t := s.history[i].ev.Timestamp
		if t.After(limit) {
			return false
		}
		if s.eligible(r, bit, i, last) && !s.inCooldown(ri, r, t) {
			picked[last] = i
			return true
		}
	}
	return false
}
