package policy

import (
	"context"
	"hash/maphash"
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
// path, only copies the calls (arguments cut to max_argument_bytes) into a
// batch and offers it to the lane without blocking. A full lane drops the
// batch and counts it: correlation loses those events, the request is never
// delayed.
//
// A worker then, for each batch:
//
//  1. snapshots the taxonomy and correlator under the read lock and
//     releases it;
//  2. categorizes every call with no lock held, and drops the arguments;
//  3. under the write lock, checks that the session instance and the
//     correlator are still the ones the batch was queued for, and assigns
//     event IDs (call-ID uniqueness and the anonymous ordinal live in the
//     session's toolSession);
//  4. calls Observe with the lock released;
//  5. under the write lock again, re-checks the session instance (an ended
//     session's matches are discarded and its correlation state forgotten)
//     and the correlator (a replaced engine's matches are discarded), then
//     records the matches like any other correlation match.
//
// This is the recordViolations protocol (collect, unlock, observe, relock,
// record) with the categorization step in front, moved to a worker.
//
// Bounds: ToolLaneWorkers workers, each with a FIFO of ToolQueuePerWorker
// batches; MaxToolQueueBytes of argument and name bytes held by queued and
// running batches together; MaxToolCallsPerBatch calls per batch;
// MaxToolSessions session states of MaxToolCallIDsPerSession call IDs
// each. A session's batches always go to the same worker, so they are
// categorized and observed in arrival order. Workers start with the first
// batch and live as long as the process; an engine with no taxonomy starts
// none.
//
// Content: a batch holds tool names and arguments only until it is
// categorized. Events carry the tool event ID and the category; the
// session's toolSession holds provider call IDs. Nothing is logged.
const (
	// ToolLaneWorkers is the number of tool-lane workers.
	ToolLaneWorkers = 4
	// ToolQueuePerWorker caps the batches waiting for one worker.
	ToolQueuePerWorker = 256
	// MaxToolQueueBytes caps the tool-name and argument bytes held by
	// batches that are queued or running. A batch that would exceed it is
	// dropped.
	MaxToolQueueBytes = 32 << 20
	// MaxToolCallsPerBatch caps the calls one batch carries; calls past it
	// are dropped and counted.
	MaxToolCallsPerBatch = 128
	// MaxToolSessions caps live sessions with tool-correlation state. Past
	// it an arbitrary state is evicted (counted): that session's queued
	// batches are discarded and its call-ID history restarts.
	MaxToolSessions = 16384
	// MaxToolCallIDsPerSession caps the provider call IDs a session
	// remembers. Once full, further calls in the session use the anonymous
	// form, because their IDs can no longer be proven unique.
	MaxToolCallIDsPerSession = 1024
	// maxToolCallIDBytes is the longest provider call ID used as an
	// identity; a longer one is treated as absent.
	maxToolCallIDBytes = 256
)

// toolSession is one session instance's tool-correlation state. Its
// pointer identifies the instance: session end deletes it, so a batch
// queued before the end finds a different (or no) state and is discarded.
type toolSession struct {
	callIDs map[string]struct{}
	// ordinal numbers the session's anonymous calls, so two ID-less calls
	// never share an event ID even with equal timestamps.
	ordinal int
}

type toolJob struct {
	sessionID string
	state     *toolSession
	at        time.Time
	calls     []ToolCall
	bytes     int64
}

type toolLane struct {
	once   sync.Once
	seed   maphash.Seed
	queues [ToolLaneWorkers]chan toolJob

	// pending counts batches queued or running (FlushToolCalls waits for
	// zero); heldBytes is what they hold against MaxToolQueueBytes.
	pending   atomic.Int64
	heldBytes atomic.Int64

	batchesQueued    atomic.Uint64
	batchesDropped   atomic.Uint64
	callsDropped     atomic.Uint64
	batchesDiscarded atomic.Uint64
	sessionsEvicted  atomic.Uint64

	// beforeCategorize is a test seam: when set, a worker calls it with
	// no lock held, just before categorizing a batch. Set it before the
	// first batch is queued.
	beforeCategorize func()
}

// enqueueToolCalls copies calls into a batch and offers it to the tool
// lane. It runs on the request path: it never categorizes and never waits.
func (e *Engine) enqueueToolCalls(sessionID string, maxArgs int, calls []ToolCall) {
	if len(calls) > MaxToolCallsPerBatch {
		e.tools.callsDropped.Add(uint64(len(calls[MaxToolCallsPerBatch:])))
		calls = calls[:MaxToolCallsPerBatch]
	}
	// Copy what categorization needs, cut to what it reads, so a queued
	// batch never pins a whole response body.
	batch := make([]ToolCall, 0, len(calls))
	var held int64
	for _, tc := range calls {
		if tc.Name == "" || len(tc.Name) > correlation.MaxToolNameBytes {
			continue // falls into no category
		}
		args := tc.Arguments
		if len(args) > maxArgs {
			args = args[:maxArgs]
		}
		id := tc.ID
		if len(id) > maxToolCallIDBytes {
			id = ""
		}
		batch = append(batch, ToolCall{ID: strings.Clone(id), Name: strings.Clone(tc.Name), Arguments: strings.Clone(args)})
		held += int64(len(id) + len(tc.Name) + len(args))
	}
	if len(batch) == 0 {
		return
	}
	if e.tools.heldBytes.Add(held) > MaxToolQueueBytes {
		e.tools.heldBytes.Add(-held)
		e.tools.batchesDropped.Add(1)
		return
	}

	e.mu.Lock()
	state := e.toolSessions[sessionID]
	if state == nil {
		if len(e.toolSessions) >= MaxToolSessions {
			for id := range e.toolSessions {
				delete(e.toolSessions, id)
				e.tools.sessionsEvicted.Add(1)
				break
			}
		}
		state = &toolSession{callIDs: make(map[string]struct{})}
		e.toolSessions[sessionID] = state
	}
	e.mu.Unlock()

	e.tools.once.Do(e.startToolLane)
	job := toolJob{sessionID: sessionID, state: state, at: time.Now(), calls: batch, bytes: held}
	q := e.tools.queues[maphash.String(e.tools.seed, sessionID)%ToolLaneWorkers]
	e.tools.pending.Add(1)
	select {
	case q <- job:
		e.tools.batchesQueued.Add(1)
	default:
		e.tools.pending.Add(-1)
		e.tools.heldBytes.Add(-held)
		e.tools.batchesDropped.Add(1)
	}
}

func (e *Engine) startToolLane() {
	e.tools.seed = maphash.MakeSeed()
	for i := range e.tools.queues {
		q := make(chan toolJob, ToolQueuePerWorker)
		e.tools.queues[i] = q
		go func() {
			for job := range q {
				e.processToolJob(job)
				e.tools.heldBytes.Add(-job.bytes)
				e.tools.pending.Add(-1)
			}
		}()
	}
}

// processToolJob categorizes one batch and feeds it to correlation (steps
// 1-5 above).
func (e *Engine) processToolJob(job toolJob) {
	e.mu.RLock()
	taxonomy, corr := e.taxonomy, e.correlation
	e.mu.RUnlock()
	if taxonomy == nil || corr == nil {
		e.tools.batchesDiscarded.Add(1)
		return
	}

	if e.tools.beforeCategorize != nil {
		e.tools.beforeCategorize()
	}
	categories := make([][]string, len(job.calls))
	for i := range job.calls {
		for _, c := range taxonomy.Categorize(job.calls[i].Name, job.calls[i].Arguments) {
			if corr.Wants(c) {
				categories[i] = append(categories[i], c)
			}
		}
		job.calls[i].Arguments = ""
	}

	e.mu.Lock()
	if e.toolSessions[job.sessionID] != job.state || e.correlation != corr {
		e.mu.Unlock()
		e.tools.batchesDiscarded.Add(1)
		return
	}
	var events []correlation.Event
	for i, tc := range job.calls {
		if len(categories[i]) == 0 {
			continue
		}
		callID, ordinal := job.state.identify(tc.ID)
		for _, c := range categories[i] {
			events = append(events, correlation.Event{
				ID:        correlation.ToolEventID(job.sessionID, callID, tc.Name, c, job.at, ordinal),
				Category:  c,
				Timestamp: job.at,
			})
		}
	}
	e.mu.Unlock()
	if len(events) == 0 {
		return
	}

	e.correlationQueued.Add(uint64(len(events)))
	matches := corr.Observe(job.sessionID, events)

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.toolSessions[job.sessionID] != job.state {
		// The session instance ended (or its state was evicted) while
		// Observe ran. This Observe may have re-created correlation state
		// after the session-end Forget, so forget it again; the matches
		// belong to an instance that is gone.
		corr.Forget(job.sessionID)
		e.correlationDiscarded.Add(uint64(len(matches)))
		return
	}
	if len(matches) == 0 {
		return
	}
	if e.correlation != corr {
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

// identify returns the call ID to build a tool event ID from, and the
// anonymous ordinal. A provider call ID is used only the first time the
// session sees it: a repeat is a distinct call from a provider that reuses
// IDs (the proxy evaluates each response once), so it falls back to the
// anonymous form, as does every call once the ID set is full. The caller
// holds e.mu.
func (s *toolSession) identify(id string) (string, int) {
	s.ordinal++
	if id == "" {
		return "", s.ordinal
	}
	if _, seen := s.callIDs[id]; seen || len(s.callIDs) >= MaxToolCallIDsPerSession {
		return "", s.ordinal
	}
	s.callIDs[id] = struct{}{}
	return id, s.ordinal
}

// FlushToolCalls waits until every tool-call batch queued so far has been
// categorized, observed and recorded, or ctx ends. Shutdown calls it before
// the session drain, so late matches land before sessions are persisted;
// tests call it to wait for the lane.
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
