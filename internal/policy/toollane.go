package policy

import (
	"container/list"
	"context"
	"hash/maphash"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"elida/internal/correlation"
)

// The tool lane: how tool calls reach temporal correlation (Plan B D7).
//
// Categorizing a tool call runs the operator's taxonomy regexes over up to
// max_argument_bytes of its arguments. That is bounded but not cheap (about
// 1.4 s per call in the worst case a valid taxonomy allows; see
// correlation's cost model), so it never runs on the request path and never
// under a proxy, session or policy lock. EvaluateToolCalls, on the request
// path, only sizes the calls, reserves their bytes, registers the batch
// with the session's tool state under a brief lock, copies the calls
// (arguments cut to max_argument_bytes) and offers the batch to the lane
// without blocking. Anything that does not fit is dropped and counted:
// correlation loses those events, the request is never delayed.
//
// The batch carries the correlator and taxonomy that were current when it
// was queued. A worker then, for each batch:
//
//  1. categorizes every call with no lock held, and drops the arguments
//     (it stops early, abandoning the batch, once CloseToolLane has
//     stopped the lane);
//  2. under the write lock, checks that the session instance is still
//     live (not ended, not retained at block/terminate) and that the
//     correlator is still the one the batch was queued for, and assigns
//     each categorized call its identity (call ID or anonymous ordinal);
//  3. builds the event IDs (SHA-256) and calls Observe, lock released;
//  4. under the write lock again, re-checks the session instance (an ended
//     or retained session's matches are discarded and its correlation
//     state forgotten) and the correlator (a replaced engine's matches are
//     discarded), then records the matches like any other correlation
//     match.
//
// This is the recordViolations protocol (collect, unlock, observe, relock,
// record, with the N-2/N-3/N-4 checks) with categorization in front, moved
// to a worker.
//
// Bounds:
//   - ToolLaneWorkers workers, each with a FIFO of ToolQueuePerWorker
//     batches. A session's batches always go to the same worker (keyed
//     hash), so they are processed in arrival order.
//   - MaxToolBatchesPerSession batches queued or running per session, so
//     one session cannot fill a worker's FIFO and starve the sessions that
//     share it.
//   - MaxToolQueueBytes of tool-name, call-ID and argument bytes held by
//     queued and running batches together (reserved before copying).
//   - MaxToolCallsPerBatch calls per batch.
//   - Session states: at most the correlator's MaxSessions (default 4096,
//     state past that cap would be evicted by the correlator anyway),
//     least recently used evicted first (counted; an eviction is not a
//     session end, see evictToolSessionLocked). Each remembers at most
//     MaxToolCallIDsPerSession call IDs as 64-bit keyed hashes, whatever
//     the ID length: about 9.3 KiB when full (measured on darwin/arm64,
//     TestToolLane_CallIDMemory), so at most about 37 MiB at the default
//     4096 sessions, and typically far less (a session remembers only the
//     IDs of its categorized calls).
//
// Lifecycle: workers start with the first batch. CloseToolLane refuses new
// batches, drains the queue until its context ends, then stops the workers;
// what is still queued is abandoned and counted.
//
// Content: a batch holds tool names and arguments only until it is
// categorized. Events carry the tool event ID and the category; the
// session's toolSession holds hashes of provider call IDs. Nothing is
// logged.
const (
	// ToolLaneWorkers is the number of tool-lane workers.
	ToolLaneWorkers = 4
	// ToolQueuePerWorker caps the batches waiting for one worker.
	ToolQueuePerWorker = 256
	// MaxToolBatchesPerSession caps one session's batches queued or
	// running.
	MaxToolBatchesPerSession = 64
	// MaxToolQueueBytes caps the tool-name, call-ID and argument bytes held
	// by batches that are queued or running. A batch that would exceed it
	// is dropped.
	MaxToolQueueBytes = 32 << 20
	// MaxToolCallsPerBatch caps the calls one batch carries; calls past it
	// are dropped and counted.
	MaxToolCallsPerBatch = 128
	// MaxToolCallIDsPerSession caps the provider call IDs a session
	// remembers. Once full, further calls in the session use the anonymous
	// form, because their IDs can no longer be proven unique.
	MaxToolCallIDsPerSession = 256
	// maxToolCallIDBytes is the longest provider call ID used as an
	// identity; a longer one is treated as absent.
	maxToolCallIDBytes = 256
)

// toolSession is one session instance's tool-correlation state. Fields
// other than inflight are guarded by Engine.mu.
type toolSession struct {
	id string
	// epoch is unique per state (an engine-wide counter). It is part of
	// every call-ID identity, so a state re-created after an LRU eviction,
	// whose call-ID memory starts empty, can never rebuild an event ID an
	// earlier state of the same session already used.
	epoch uint64
	// callIDs holds keyed 64-bit hashes of the provider call IDs the
	// session has used as identities. A collision only makes a fresh ID
	// look seen, which makes that call anonymous: it still counts.
	callIDs map[uint64]struct{}
	// ordinal numbers the session's calls, so two anonymous calls never
	// share an event ID even with equal timestamps.
	ordinal int
	// ended is set when the session instance ends (ReleaseFlaggedSession,
	// RemoveFlaggedSession): its queued batches are then discarded. An
	// LRU eviction does not set it: the session is live and its in-flight
	// batches still count.
	ended bool
	// inflight counts the session's batches queued or running.
	inflight atomic.Int32
	lru      *list.Element
}

type toolJob struct {
	sessionID string
	state     *toolSession
	corr      *correlation.Engine
	taxonomy  *correlation.Taxonomy
	at        time.Time
	calls     []ToolCall
	bytes     int64
}

type toolLane struct {
	once    sync.Once
	seed    maphash.Seed
	queues  [ToolLaneWorkers]chan toolJob
	stop    chan struct{}
	stopper sync.Once
	workers sync.WaitGroup
	closed  atomic.Bool

	// pending counts batches queued or running (FlushToolCalls waits for
	// zero); heldBytes is what they hold against MaxToolQueueBytes.
	pending   atomic.Int64
	heldBytes atomic.Int64

	batchesQueued    atomic.Uint64
	batchesDropped   atomic.Uint64
	callsDropped     atomic.Uint64
	batchesDiscarded atomic.Uint64
	sessionsEvicted  atomic.Uint64
	batchesRefused   atomic.Uint64
	batchesAbandoned atomic.Uint64

	// Test seams, called by a worker with no lock held. Set them before
	// the first batch is queued.
	beforeCategorize func()                             // before a batch is categorized
	onCategorize     func()                             // after each call is categorized
	afterAssign      func(job *toolJob)                 // after identities are assigned, before Observe
	afterObserve     func(job *toolJob)                 // after Observe, before the relock
	categorize       func(job *toolJob, i int) []string // replaces Taxonomy.Categorize
}

func (l *toolLane) init() {
	l.seed = maphash.MakeSeed()
	l.stop = make(chan struct{})
}

func (l *toolLane) stopped() bool {
	select {
	case <-l.stop:
		return true
	default:
		return false
	}
}

// enqueueToolCalls registers a batch with the session and offers it to the
// tool lane. It runs on the request path: it never categorizes and never
// waits.
func (e *Engine) enqueueToolCalls(sessionID string, taxonomy *correlation.Taxonomy, calls []ToolCall) {
	if e.tools.closed.Load() {
		e.tools.batchesRefused.Add(1)
		return
	}
	if len(calls) > MaxToolCallsPerBatch {
		e.tools.callsDropped.Add(uint64(len(calls[MaxToolCallsPerBatch:])))
		calls = calls[:MaxToolCallsPerBatch]
	}
	maxArgs := taxonomy.MaxArgumentBytes()

	// Size first, reserve, then copy: a full budget costs no allocation.
	var held int64
	for _, tc := range calls {
		if !laneCandidate(tc) {
			continue
		}
		held += int64(len(laneCallID(tc.ID)) + len(tc.Name) + min(len(tc.Arguments), maxArgs))
	}
	if held == 0 {
		return
	}
	if e.tools.heldBytes.Add(held) > MaxToolQueueBytes {
		e.tools.heldBytes.Add(-held)
		e.tools.batchesDropped.Add(1)
		return
	}

	e.mu.Lock()
	corr := e.correlation
	if corr == nil || e.taxonomy != taxonomy || e.retained[sessionID] != 0 {
		// Replaced since the snapshot, or the session instance ended at
		// block/terminate and is retained: nothing may be added to it.
		e.mu.Unlock()
		e.tools.heldBytes.Add(-held)
		e.tools.batchesDiscarded.Add(1)
		return
	}
	state := e.toolSessionLocked(sessionID)
	if state.inflight.Load() >= MaxToolBatchesPerSession {
		e.mu.Unlock()
		e.tools.heldBytes.Add(-held)
		e.tools.batchesDropped.Add(1)
		return
	}
	state.inflight.Add(1)
	e.mu.Unlock()

	// Copy only what categorization reads, so a queued batch never pins a
	// whole response body.
	batch := make([]ToolCall, 0, len(calls))
	for _, tc := range calls {
		if !laneCandidate(tc) {
			continue
		}
		args := tc.Arguments
		if len(args) > maxArgs {
			args = args[:maxArgs]
		}
		batch = append(batch, ToolCall{ID: strings.Clone(laneCallID(tc.ID)), Name: strings.Clone(tc.Name), Arguments: strings.Clone(args)})
	}

	e.tools.once.Do(e.startToolLane)
	job := toolJob{sessionID: sessionID, state: state, corr: corr, taxonomy: taxonomy, at: time.Now(), calls: batch, bytes: held}
	e.tools.pending.Add(1)
	if e.tools.closed.Load() {
		e.finishToolJob(&job)
		e.tools.batchesRefused.Add(1)
		return
	}
	// A nil queue (CloseToolLane ran before the lane ever started) takes
	// the default branch.
	q := e.tools.queues[maphash.String(e.tools.seed, sessionID)%ToolLaneWorkers]
	select {
	case q <- job:
		e.tools.batchesQueued.Add(1)
	default:
		e.finishToolJob(&job)
		e.tools.batchesDropped.Add(1)
	}
}

// laneCandidate reports whether a call can fall into any category.
func laneCandidate(tc ToolCall) bool {
	return tc.Name != "" && len(tc.Name) <= correlation.MaxToolNameBytes
}

// laneCallID drops a call ID too long to be a provider ID.
func laneCallID(id string) string {
	if len(id) > maxToolCallIDBytes {
		return ""
	}
	return id
}

// toolSessionLocked returns the session's tool state, creating it and
// evicting the least recently used state past the correlator's session
// cap. The caller holds e.mu.
func (e *Engine) toolSessionLocked(sessionID string) *toolSession {
	if st := e.toolSessions[sessionID]; st != nil {
		e.toolLRU.MoveToFront(st.lru)
		return st
	}
	limit := e.correlationConfig.Limits.MaxSessions
	if limit <= 0 {
		limit = correlation.DefaultMaxSessions
	}
	for len(e.toolSessions) >= limit {
		oldest := e.toolLRU.Back()
		if oldest == nil {
			break
		}
		victim, ok := oldest.Value.(*toolSession)
		if !ok {
			break
		}
		e.evictToolSessionLocked(victim)
	}
	e.toolEpoch++
	st := &toolSession{id: sessionID, epoch: e.toolEpoch, callIDs: make(map[uint64]struct{})}
	st.lru = e.toolLRU.PushFront(st)
	e.toolSessions[sessionID] = st
	return st
}

// evictToolSessionLocked drops a live session's tool state to make room.
// The session is not ended: batches already queued with this state still
// run and record (an eviction never forgets correlation history), and the
// session's next batch starts a fresh state with an empty call-ID memory
// (its later calls may be anonymous, never suppressed). A state with a
// batch queued or running is kept in toolEvicted, so a session end that
// comes before that batch runs still marks it ended and the batch is
// discarded. The caller holds e.mu.
func (e *Engine) evictToolSessionLocked(st *toolSession) {
	e.toolLRU.Remove(st.lru)
	delete(e.toolSessions, st.id)
	e.tools.sessionsEvicted.Add(1)
	if st.inflight.Load() > 0 {
		e.keepEvictedToolStateLocked(st)
	}
}

// keepEvictedToolStateLocked records an evicted state with batches in
// flight. inflight only grows under e.mu, and an evicted state gets no new
// batches, so a state seen drained stays drained and is pruned. The side
// list is bounded by the batches in flight: whenever it reaches the
// session cap, every drained state is pruned. The caller holds e.mu.
func (e *Engine) keepEvictedToolStateLocked(st *toolSession) {
	limit := e.correlationConfig.Limits.MaxSessions
	if limit <= 0 {
		limit = correlation.DefaultMaxSessions
	}
	if len(e.toolEvicted) >= limit {
		for id, states := range e.toolEvicted {
			if kept := liveToolStates(states); len(kept) > 0 {
				e.toolEvicted[id] = kept
			} else {
				delete(e.toolEvicted, id)
			}
		}
	}
	e.toolEvicted[st.id] = append(liveToolStates(e.toolEvicted[st.id]), st)
}

// liveToolStates keeps the states that still have a batch in flight.
func liveToolStates(states []*toolSession) []*toolSession {
	kept := states[:0]
	for _, st := range states {
		if st.inflight.Load() > 0 {
			kept = append(kept, st)
		}
	}
	return kept
}

// endToolSessionLocked ends the session instance's tool state, and any
// evicted state of it whose batches are still in flight: their queued
// batches are discarded. The caller holds e.mu.
func (e *Engine) endToolSessionLocked(sessionID string) {
	for _, st := range e.toolEvicted[sessionID] {
		st.ended = true
	}
	delete(e.toolEvicted, sessionID)
	st := e.toolSessions[sessionID]
	if st == nil {
		return
	}
	st.ended = true
	e.toolLRU.Remove(st.lru)
	delete(e.toolSessions, sessionID)
}

// toolJobLiveLocked reports whether the batch may still affect its
// session: the instance has not ended, is not retained, and the correlator
// is the one the batch was queued for. The caller holds e.mu.
func (e *Engine) toolJobLiveLocked(job *toolJob) bool {
	return !job.state.ended && e.retained[job.sessionID] == 0 && e.correlation == job.corr
}

func (e *Engine) startToolLane() {
	for i := range e.tools.queues {
		q := make(chan toolJob, ToolQueuePerWorker)
		e.tools.queues[i] = q
		e.tools.workers.Add(1)
		go e.toolWorker(q)
	}
}

func (e *Engine) toolWorker(q chan toolJob) {
	defer e.tools.workers.Done()
	for {
		select {
		case <-e.tools.stop:
			return
		case job := <-q:
			if e.tools.stopped() {
				e.tools.batchesAbandoned.Add(1)
			} else {
				e.processToolJob(&job)
			}
			e.finishToolJob(&job)
		}
	}
}

// finishToolJob releases what a batch held. It never takes e.mu.
func (e *Engine) finishToolJob(job *toolJob) {
	e.tools.heldBytes.Add(-job.bytes)
	job.state.inflight.Add(-1)
	e.tools.pending.Add(-1)
}

type laneCall struct {
	name       string
	callID     string
	ordinal    int
	categories []string
}

// processToolJob categorizes one batch and feeds it to correlation (steps
// 1-4 above).
func (e *Engine) processToolJob(job *toolJob) {
	// Step 1: categorize, no lock held.
	if e.tools.beforeCategorize != nil {
		e.tools.beforeCategorize()
	}
	calls := make([]laneCall, 0, len(job.calls))
	idHashes := make([]uint64, 0, len(job.calls))
	for i := range job.calls {
		if e.tools.stopped() {
			e.tools.batchesAbandoned.Add(1)
			return
		}
		var cats []string
		if e.tools.categorize != nil {
			cats = e.tools.categorize(job, i)
		} else {
			cats = job.taxonomy.Categorize(job.calls[i].Name, job.calls[i].Arguments)
		}
		if e.tools.onCategorize != nil {
			e.tools.onCategorize()
		}
		job.calls[i].Arguments = ""
		var wanted []string
		for _, c := range cats {
			if job.corr.Wants(c) {
				wanted = append(wanted, c)
			}
		}
		if len(wanted) == 0 {
			continue
		}
		var h uint64
		if id := job.calls[i].ID; id != "" {
			h = maphash.String(e.tools.seed, id)
		}
		calls = append(calls, laneCall{name: job.calls[i].Name, callID: job.calls[i].ID, categories: wanted})
		idHashes = append(idHashes, h)
	}
	if len(calls) == 0 {
		return
	}

	// Step 2: identities, under the lock.
	e.mu.Lock()
	if !e.toolJobLiveLocked(job) {
		e.mu.Unlock()
		e.tools.batchesDiscarded.Add(1)
		return
	}
	for i := range calls {
		calls[i].callID, calls[i].ordinal = job.state.identify(calls[i].callID, idHashes[i])
	}
	e.mu.Unlock()
	if e.tools.afterAssign != nil {
		e.tools.afterAssign(job)
	}

	// Step 3: event IDs and Observe, lock released.
	var events []correlation.Event
	for _, c := range calls {
		for _, cat := range c.categories {
			events = append(events, correlation.Event{
				ID:        correlation.ToolEventID(job.sessionID, c.callID, c.name, cat, job.at, c.ordinal),
				Category:  cat,
				Timestamp: job.at,
			})
		}
	}
	e.correlationQueued.Add(uint64(len(events)))
	matches := job.corr.Observe(job.sessionID, events)
	if e.tools.afterObserve != nil {
		e.tools.afterObserve(job)
	}

	// Step 4: record, under the lock, after the re-checks.
	e.mu.Lock()
	defer e.mu.Unlock()
	if job.state.ended || e.retained[job.sessionID] != 0 {
		// The session instance ended (or was retained at block/terminate)
		// while Observe ran. This Observe may have re-created correlation
		// state after the session-end Forget, so forget it again; the
		// matches belong to an instance that is gone.
		job.corr.Forget(job.sessionID)
		e.correlationDiscarded.Add(uint64(len(matches)))
		return
	}
	if len(matches) == 0 {
		return
	}
	if e.correlation != job.corr {
		// Replaced during Observe: its matches follow rules no longer in
		// force.
		e.correlationDiscarded.Add(uint64(len(matches)))
		return
	}
	mv := make([]Violation, len(matches))
	for i, m := range matches {
		mv[i] = e.matchViolationLocked(m)
	}
	e.recordViolationsLocked(job.sessionID, mv, nil)
}

// identify returns the call ID to build a tool event ID from ("" for the
// anonymous form), and the call's ordinal. A provider call ID is used only
// the first time the state sees it, qualified by the state's epoch: a
// repeat is a distinct call from a provider that reuses IDs (the proxy
// evaluates each response once), so it falls back to the anonymous form,
// as does every call once the ID set is full. h is the keyed hash of id.
// The epoch is a per-process counter, so these event IDs (and the match IDs
// built from them) are not stable across restarts or re-created states;
// nothing consumes cross-restart stability today, since correlator state
// and match dedup are in memory. The caller holds e.mu.
func (s *toolSession) identify(id string, h uint64) (string, int) {
	s.ordinal++
	if id == "" {
		return "", s.ordinal
	}
	if _, seen := s.callIDs[h]; seen || len(s.callIDs) >= MaxToolCallIDsPerSession {
		return "", s.ordinal
	}
	s.callIDs[h] = struct{}{}
	return id + "#" + strconv.FormatUint(s.epoch, 10), s.ordinal
}

// FlushToolCalls waits until every tool-call batch queued so far has been
// categorized, observed and recorded, or ctx ends. Tests use it to wait
// for the lane; shutdown uses CloseToolLane.
func (e *Engine) FlushToolCalls(ctx context.Context) error {
	if e.tools.pending.Load() == 0 {
		return nil
	}
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for e.tools.pending.Load() != 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
	return nil
}

// CloseToolLane shuts the tool lane down: it refuses new batches (counted
// as refused), waits for the queued ones until ctx ends, then stops the
// workers. A worker stops between calls, so a batch it is categorizing and
// every batch still queued are abandoned (counted); a single Categorize
// call already running finishes on its own and is then discarded. The
// channels are never closed (a concurrent enqueue would panic), and
// CloseToolLane does not wait for the workers to exit. It returns ctx's
// error when the queue did not drain in time. Calling it again is safe.
func (e *Engine) CloseToolLane(ctx context.Context) error {
	e.tools.closed.Store(true)
	// Settle a concurrent first start (or prevent a later one) before the
	// queues are read.
	e.tools.once.Do(func() {})
	err := e.FlushToolCalls(ctx)
	e.tools.stopper.Do(func() { close(e.tools.stop) })
	for _, q := range e.tools.queues {
		for drained := false; !drained && q != nil; {
			select {
			case job := <-q:
				e.tools.batchesAbandoned.Add(1)
				e.finishToolJob(&job)
			default:
				drained = true
			}
		}
	}
	return err
}
