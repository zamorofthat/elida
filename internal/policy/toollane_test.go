package policy

import (
	"context"
	"errors"
	"fmt"
	"hash/maphash"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"elida/internal/correlation"
)

func toolLaneConfig(maxArgs, maxSessions int) Config {
	return Config{
		Enabled: true, Mode: "enforce",
		Correlation: CorrelationConfig{
			Enabled: true,
			Rules: []correlation.Rule{{
				Name: "cred_twice", Shape: correlation.ShapeRepeated,
				Category: "credential_discovery", Count: 2, Window: time.Minute,
			}},
			Limits: correlation.Limits{MaxHistoryEvents: 64, MaxSessions: maxSessions},
			TaxonomyConfig: correlation.TaxonomyConfig{
				MaxArgumentBytes: maxArgs,
				ToolCategories: []correlation.ToolCategoryRule{
					{Category: "credential_discovery", Tools: []string{"bash"}, ArgumentPatterns: []string{`id_rsa`}},
				},
			},
		},
	}
}

func toolLaneEngine(t *testing.T, maxArgs int) *Engine {
	t.Helper()
	return NewEngine(toolLaneConfig(maxArgs, 64))
}

// changedCorrelation is cfg with a different correlation block, so
// ReloadConfig replaces the correlator.
func changedCorrelation(cfg Config) Config {
	cfg.Correlation = cloneCorrelationConfig(cfg.Correlation)
	cfg.Correlation.Limits.MaxHistoryEvents++
	return cfg
}

func laneStats(t *testing.T, e *Engine) CorrelationStats {
	t.Helper()
	st, ok := e.Stats()["correlation"].(CorrelationStats)
	if !ok {
		t.Fatal("no correlation stats")
	}
	return st
}

func flushLane(t *testing.T, e *Engine) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.FlushToolCalls(ctx); err != nil {
		t.Fatalf("FlushToolCalls: %v", err)
	}
}

var credCall = []ToolCall{{Name: "bash", Arguments: "cat id_rsa"}}

// The binding ruling: categorization runs off the request path and under no
// lock. EvaluateToolCalls returns while the worker is held at a gate, and
// a detector that runs during categorization (after each Categorize call,
// inside the loop) finds the policy lock free.
func TestToolLane_RequestPathReturnsBeforeCategorization(t *testing.T) {
	e := toolLaneEngine(t, 0)
	entered := make(chan struct{})
	release := make(chan struct{})
	var detections, lockHeld atomic.Int32
	e.tools.beforeCategorize = func() {
		close(entered)
		<-release
	}
	e.tools.onCategorize = func() {
		detections.Add(1)
		if !e.mu.TryLock() {
			lockHeld.Add(1)
			return
		}
		e.mu.Unlock()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.EvaluateToolCalls("sess-gate", []ToolCall{{ID: "a", Name: "bash", Arguments: "cat id_rsa"}})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("EvaluateToolCalls waited for categorization")
	}
	<-entered
	if st := laneStats(t, e); st.EventsObserved != 0 || st.EventsQueued != 0 {
		t.Fatalf("nothing may be observed before categorization: %+v", st)
	}
	close(release)
	flushLane(t, e)
	if detections.Load() != 1 || lockHeld.Load() != 0 {
		t.Fatalf("categorization ran with the policy lock held (%d of %d calls)", lockHeld.Load(), detections.Load())
	}
	if st := laneStats(t, e); st.EventsObserved != 1 || st.ToolBatchesQueued != 1 {
		t.Fatalf("after the gate opens the call is observed: %+v", st)
	}
}

// sessionsOnWorker returns n session IDs whose batches go to worker w.
func sessionsOnWorker(e *Engine, w uint64, n int) []string {
	var out []string
	for i := 0; len(out) < n; i++ {
		id := fmt.Sprintf("sess-w%d-%d", w, i)
		if maphash.String(e.tools.seed, id)%ToolLaneWorkers == w {
			out = append(out, id)
		}
	}
	return out
}

// A worker's FIFO holds ToolQueuePerWorker batches. With that worker held,
// further batches for its sessions are dropped and counted, without
// blocking the caller.
func TestToolLane_QueueBoundDropsAndCounts(t *testing.T) {
	e := toolLaneEngine(t, 0)
	release := make(chan struct{})
	e.tools.beforeCategorize = func() { <-release }
	sessions := sessionsOnWorker(e, 0, 6)
	n := 0
	start := time.Now()
	for _, s := range sessions {
		for i := 0; i < MaxToolBatchesPerSession; i++ {
			e.EvaluateToolCalls(s, credCall)
			n++
		}
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("enqueueing blocked: %v", d)
	}
	st := laneStats(t, e)
	if st.ToolBatchesQueued+st.ToolBatchesDropped != uint64(n) ||
		st.ToolBatchesQueued > ToolQueuePerWorker+1 || st.ToolBatchesDropped < uint64(n-(ToolQueuePerWorker+1)) {
		t.Fatalf("queue bound: queued %d dropped %d of %d", st.ToolBatchesQueued, st.ToolBatchesDropped, n)
	}
	close(release)
	flushLane(t, e)
	if held := e.tools.heldBytes.Load(); held != 0 {
		t.Fatalf("held bytes after flush = %d", held)
	}
}

// Review M-1: one session holds at most MaxToolBatchesPerSession batches,
// so flooding cannot fill the worker it shares with other sessions.
func TestToolLane_PerSessionQuotaProtectsPeers(t *testing.T) {
	e := toolLaneEngine(t, 0)
	release := make(chan struct{})
	e.tools.beforeCategorize = func() { <-release }
	sessions := sessionsOnWorker(e, 1, 65)
	flooder, victims := sessions[0], sessions[1:]
	const flood = 300
	for i := 0; i < flood; i++ {
		e.EvaluateToolCalls(flooder, credCall)
	}
	st := laneStats(t, e)
	if st.ToolBatchesQueued != MaxToolBatchesPerSession || st.ToolBatchesDropped != flood-MaxToolBatchesPerSession {
		t.Fatalf("per-session quota: queued %d dropped %d", st.ToolBatchesQueued, st.ToolBatchesDropped)
	}
	for _, v := range victims {
		e.EvaluateToolCalls(v, credCall)
	}
	if after := laneStats(t, e); after.ToolBatchesDropped != st.ToolBatchesDropped {
		t.Fatalf("victims on the flooder's worker lost %d batches", after.ToolBatchesDropped-st.ToolBatchesDropped)
	}
	close(release)
	flushLane(t, e)
}

// Queued and running batches together hold at most MaxToolQueueBytes of
// names and arguments; past it batches are dropped.
func TestToolLane_ByteBudgetDropsAndCounts(t *testing.T) {
	e := toolLaneEngine(t, correlation.MaxArgumentBytesCeiling)
	release := make(chan struct{})
	e.tools.beforeCategorize = func() { <-release }
	big := strings.Repeat("x", correlation.MaxArgumentBytesCeiling)
	calls := make([]ToolCall, MaxToolCallsPerBatch)
	for i := range calls {
		calls[i] = ToolCall{Name: "noop", Arguments: big} // no glob match: no argument scan
	}
	perBatch := int64(MaxToolCallsPerBatch * (len("noop") + len(big)))
	const n = 10
	for i := 0; i < n; i++ {
		e.EvaluateToolCalls("sess-bytes", calls)
		if held := e.tools.heldBytes.Load(); held > MaxToolQueueBytes {
			t.Fatalf("held %d bytes, budget %d", held, MaxToolQueueBytes)
		}
	}
	st := laneStats(t, e)
	maxQueued := uint64(MaxToolQueueBytes / perBatch)
	if st.ToolBatchesQueued != maxQueued || st.ToolBatchesDropped != n-maxQueued {
		t.Fatalf("byte budget: queued %d dropped %d, want %d queued", st.ToolBatchesQueued, st.ToolBatchesDropped, maxQueued)
	}
	close(release)
	flushLane(t, e)
}

// Review M-3: a batch refused by the byte budget is not copied first.
func TestToolLane_FullBudgetRefusesBeforeCopying(t *testing.T) {
	e := toolLaneEngine(t, correlation.MaxArgumentBytesCeiling)
	e.tools.heldBytes.Store(MaxToolQueueBytes)
	big := strings.Repeat("x", correlation.MaxArgumentBytesCeiling)
	calls := make([]ToolCall, MaxToolCallsPerBatch)
	for i := range calls {
		calls[i] = ToolCall{Name: "bash", Arguments: big}
	}
	allocs := testing.AllocsPerRun(5, func() { e.EvaluateToolCalls("sess-full", calls) })
	if allocs != 0 {
		t.Fatalf("a refused batch allocated %.0f times (copied before the budget check)", allocs)
	}
	e.tools.heldBytes.Store(0)
}

func TestToolLane_CallsPastTheBatchCapAreDropped(t *testing.T) {
	e := toolLaneEngine(t, 0)
	calls := make([]ToolCall, MaxToolCallsPerBatch+72)
	for i := range calls {
		calls[i] = ToolCall{Name: "noop"}
	}
	e.EvaluateToolCalls("sess-cap", calls)
	flushLane(t, e)
	if st := laneStats(t, e); st.ToolCallsDropped != 72 {
		t.Fatalf("calls dropped = %d, want 72", st.ToolCallsDropped)
	}
}

// A queued batch keeps only what categorization reads: arguments cut to
// max_argument_bytes, copied (no pinned response body).
func TestToolLane_BatchHoldsAtMostMaxArgumentBytes(t *testing.T) {
	e := toolLaneEngine(t, 16)
	release := make(chan struct{})
	e.tools.beforeCategorize = func() { <-release }
	e.EvaluateToolCalls("sess-cut", []ToolCall{{ID: "c", Name: "bash", Arguments: strings.Repeat("y", 4096)}})
	if held := e.tools.heldBytes.Load(); held != int64(len("c")+len("bash")+16) {
		t.Fatalf("held bytes = %d", held)
	}
	close(release)
	flushLane(t, e)
}

// Binding ruling: a provider call ID is an identity only while unique in
// the session. The first use keeps it (qualified by the state's epoch), a
// repeat and every call past the per-session cap fall back to ""
// (anonymous), and ordinals never repeat.
func TestToolLane_CallIDResolution(t *testing.T) {
	seed := maphash.MakeSeed()
	h := func(id string) uint64 { return maphash.String(seed, id) }
	s := &toolSession{epoch: 7, callIDs: make(map[uint64]struct{})}
	if id, _ := s.identify("call_1", h("call_1")); id != "call_1#7" {
		t.Fatalf("first use keeps the ID, got %q", id)
	}
	if id, _ := s.identify("call_1", h("call_1")); id != "" {
		t.Fatalf("a repeated ID must fall back to anonymous, got %q", id)
	}
	if id, _ := s.identify("", 0); id != "" {
		t.Fatalf("absent stays absent, got %q", id)
	}
	for i := len(s.callIDs); i < MaxToolCallIDsPerSession; i++ {
		id := fmt.Sprintf("fill_%d", i)
		s.identify(id, h(id))
	}
	if id, _ := s.identify("call_new", h("call_new")); id != "" {
		t.Fatalf("past the cap a new ID cannot be proven unique, got %q", id)
	}
	_, o1 := s.identify("", 0)
	_, o2 := s.identify("", 0)
	if o1 == o2 {
		t.Fatal("anonymous ordinals must be distinct")
	}
	// A re-created state (after an LRU eviction) has a new epoch, so the
	// same provider ID never rebuilds an earlier state's event ID.
	s2 := &toolSession{epoch: 8, callIDs: make(map[uint64]struct{})}
	if id, _ := s2.identify("call_1", h("call_1")); id != "call_1#8" {
		t.Fatalf("a new state qualifies by its own epoch, got %q", id)
	}
}

// Review I-4: call IDs are kept as 64-bit hashes; a full ID memory costs
// tens of KiB whatever the ID length. The lane documents the measured
// bound.
func TestToolLane_CallIDMemory(t *testing.T) {
	const sessions = 256
	seed := maphash.MakeSeed()
	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	keep := make([]*toolSession, sessions)
	for i := range keep {
		s := &toolSession{callIDs: make(map[uint64]struct{})}
		for j := 0; j < MaxToolCallIDsPerSession; j++ {
			id := fmt.Sprintf("%0256d", i*MaxToolCallIDsPerSession+j)
			s.identify(id, maphash.String(seed, id))
		}
		keep[i] = s
	}
	runtime.GC()
	runtime.ReadMemStats(&m1)
	per := float64(m1.HeapAlloc-m0.HeapAlloc) / sessions
	runtime.KeepAlive(keep)
	t.Logf("full call-ID memory: %.1f KiB/session, %.1f MiB at %d sessions", per/1024, per*correlation.DefaultMaxSessions/(1<<20), correlation.DefaultMaxSessions)
	if per > 16<<10 {
		t.Fatalf("full call-ID memory %.0f B/session, want <= 16 KiB (hashes, not strings)", per)
	}
}

// Review I-4/M-5: tool states are capped at the correlator's MaxSessions,
// least recently used evicted first, and every eviction is counted.
func TestToolLane_SessionStatesAreAnLRUAtTheCorrelatorCap(t *testing.T) {
	e := NewEngine(toolLaneConfig(0, 64))
	e.EvaluateToolCalls("keep", credCall)
	for i := 0; i < 100; i++ {
		e.EvaluateToolCalls(fmt.Sprintf("s%d", i), credCall)
		e.EvaluateToolCalls("keep", credCall) // most recently used
		flushLane(t, e)
	}
	e.mu.RLock()
	n, kept := len(e.toolSessions), e.toolSessions["keep"] != nil
	e.mu.RUnlock()
	st := laneStats(t, e)
	if n != 64 || !kept || st.ToolSessionsEvicted != 101-64 {
		t.Fatalf("states=%d keep=%v evicted=%d, want 64, true, %d", n, kept, st.ToolSessionsEvicted, 101-64)
	}
}

// Review M-5: an eviction is not a session end. A batch already queued
// with the evicted state still records, and the session's correlation
// history is not forgotten.
func TestToolLane_EvictionNeverForgetsALiveSession(t *testing.T) {
	e := toolLaneEngine(t, 0)
	e.EvaluateToolCalls("live", []ToolCall{{ID: "a", Name: "bash", Arguments: "id_rsa"}})
	flushLane(t, e)

	release := make(chan struct{})
	var once sync.Once
	e.tools.beforeCategorize = func() { once.Do(func() { <-release }) }
	e.EvaluateToolCalls("live", []ToolCall{{ID: "b", Name: "bash", Arguments: "id_rsa"}})
	e.mu.Lock()
	e.evictToolSessionLocked(e.toolSessions["live"])
	e.mu.Unlock()
	close(release)
	flushLane(t, e)
	if !e.IsFlagged("live") {
		t.Fatal("the evicted state's queued batch must still complete the match")
	}
	if st := laneStats(t, e); st.Sessions != 1 || st.MatchesDiscarded != 0 {
		t.Fatalf("eviction must not forget the live session: %+v", st)
	}
}

// A batch queued before its session ended is discarded: no flagged entry,
// no correlation state for the ended instance.
func TestToolLane_SessionEndDiscardsQueuedBatch(t *testing.T) {
	e := toolLaneEngine(t, 0)
	release := make(chan struct{})
	e.tools.beforeCategorize = func() { <-release }
	e.EvaluateToolCalls("sess-end", credCall)
	e.EvaluateToolCalls("sess-end", credCall)
	e.ReleaseFlaggedSession("sess-end")
	close(release)
	flushLane(t, e)
	st := laneStats(t, e)
	if e.IsFlagged("sess-end") || st.ToolBatchesDiscarded != 2 || st.EventsObserved != 0 || st.Sessions != 0 {
		t.Fatalf("an ended session's queued batches must be discarded: flagged=%v %+v", e.IsFlagged("sess-end"), st)
	}
}

// retainedEngine flags "ret" to block with a content rule and releases it,
// so it is retained.
func retainedEngine(t *testing.T) *Engine {
	t.Helper()
	cfg := toolLaneConfig(0, 64)
	cfg.RiskLadder = RiskLadderConfig{Enabled: true, Thresholds: []RiskThreshold{{Score: 1, Action: ActionBlock}}}
	cfg.Rules = []Rule{{Name: "r", Type: RuleTypeContentMatch, Patterns: []string{"boom"}, Severity: SeverityCritical, Action: "block"}}
	e := NewEngine(cfg)
	e.EvaluateRequestContent("ret", "boom")
	if !e.ReleaseFlaggedSession("ret") {
		t.Fatal("want the blocked session retained")
	}
	return e
}

// Review I-2 (wiring N-2 semantics): a retained block/terminate session
// gets no new violation and no correlation state from a late batch. The
// batch is refused at enqueue, before any tool state exists.
func TestToolLane_RetainedSessionGetsNoLateMatch(t *testing.T) {
	e := retainedEngine(t)
	before := len(e.GetFlaggedSession("ret").Violations)
	for i := 0; i < 2; i++ {
		e.EvaluateToolCalls("ret", []ToolCall{{ID: fmt.Sprint(i), Name: "bash", Arguments: "id_rsa"}})
	}
	flushLane(t, e)
	e.mu.RLock()
	_, hasState := e.toolSessions["ret"]
	e.mu.RUnlock()
	st := laneStats(t, e)
	if after := len(e.GetFlaggedSession("ret").Violations); after != before || hasState || st.Sessions != 0 || st.ToolBatchesDiscarded != 2 {
		t.Fatalf("retained session: violations %d -> %d, state=%v, %+v", before, after, hasState, st)
	}
}

// matchingSecondBatch records one credential event for sess, then queues a
// second that completes cred_twice, with hook installed for that batch.
func matchingSecondBatch(t *testing.T, e *Engine, sess string, install func()) {
	t.Helper()
	e.EvaluateToolCalls(sess, []ToolCall{{ID: "a", Name: "bash", Arguments: "id_rsa"}})
	flushLane(t, e)
	install()
	e.EvaluateToolCalls(sess, []ToolCall{{ID: "b", Name: "bash", Arguments: "id_rsa"}})
	flushLane(t, e)
}

// Review I-3, step 4 session check: a session that ends while Observe runs
// gets nothing recorded, its matches are discarded and its re-created
// correlation state is forgotten.
func TestToolLane_SessionEndDuringObserveDiscardsMatches(t *testing.T) {
	e := toolLaneEngine(t, 0)
	matchingSecondBatch(t, e, "sess-obs", func() {
		e.tools.afterObserve = func(job *toolJob) { e.ReleaseFlaggedSession(job.sessionID) }
	})
	st := laneStats(t, e)
	if e.IsFlagged("sess-obs") || st.MatchesRecorded != 0 || st.MatchesDiscarded == 0 || st.Sessions != 0 {
		t.Fatalf("ended during Observe: flagged=%v %+v", e.IsFlagged("sess-obs"), st)
	}
}

// Review I-2/I-3: the same for a session retained while Observe runs.
func TestToolLane_RetainedDuringObserveDiscardsMatches(t *testing.T) {
	e := toolLaneEngine(t, 0)
	matchingSecondBatch(t, e, "sess-ret", func() {
		e.tools.afterObserve = func(job *toolJob) {
			e.mu.Lock()
			e.retained[job.sessionID] = 1
			e.mu.Unlock()
		}
	})
	if st := laneStats(t, e); e.IsFlagged("sess-ret") || st.MatchesRecorded != 0 || st.MatchesDiscarded == 0 || st.Sessions != 0 {
		t.Fatalf("retained during Observe: %+v", st)
	}
}

// Review I-3, step 4 correlator check: a correlator replaced while Observe
// runs has its matches discarded.
func TestToolLane_CorrelatorSwapDuringObserveDiscardsMatches(t *testing.T) {
	e := toolLaneEngine(t, 0)
	matchingSecondBatch(t, e, "sess-swap", func() {
		e.tools.afterObserve = func(*toolJob) { e.ReloadConfig(changedCorrelation(e.GetConfig())) }
	})
	if st := laneStats(t, e); e.IsFlagged("sess-swap") || st.MatchesRecorded != 0 || st.MatchesDiscarded == 0 {
		t.Fatalf("swapped during Observe: flagged=%v %+v", e.IsFlagged("sess-swap"), st)
	}
}

// Review I-3/M-2, step 2 correlator check: a batch whose correlator was
// replaced after it was queued never reaches Observe.
func TestToolLane_CorrelatorSwapBeforeAssignDiscardsTheBatch(t *testing.T) {
	e := toolLaneEngine(t, 0)
	release := make(chan struct{})
	e.tools.beforeCategorize = func() { <-release }
	e.EvaluateToolCalls("sess-pre", credCall)
	e.ReloadConfig(changedCorrelation(e.GetConfig()))
	close(release)
	flushLane(t, e)
	if st := laneStats(t, e); st.EventsQueued != 0 || st.ToolBatchesDiscarded != 1 {
		t.Fatalf("a batch for a replaced correlator must be discarded before Observe: %+v", st)
	}
}

// Review M-4: event IDs (SHA-256) are built after the identity lock is
// released.
func TestToolLane_EventIDsAreHashedOutsideTheLock(t *testing.T) {
	e := toolLaneEngine(t, 0)
	var assigned atomic.Bool
	e.tools.afterAssign = func(*toolJob) {
		if !e.mu.TryLock() {
			t.Error("the policy lock is held after identities are assigned")
			return
		}
		e.mu.Unlock()
		assigned.Store(true)
	}
	e.EvaluateToolCalls("sess-hash", credCall)
	flushLane(t, e)
	if !assigned.Load() {
		t.Fatal("afterAssign never ran")
	}
}

// Review I-1: CloseToolLane drains, refuses new batches (counted) and stops
// the workers, which exit.
func TestToolLane_CloseDrainsRefusesAndStopsWorkers(t *testing.T) {
	base := runtime.NumGoroutine()
	e := toolLaneEngine(t, 0)
	e.EvaluateToolCalls("sess-close", credCall)
	if err := e.CloseToolLane(context.Background()); err != nil {
		t.Fatalf("CloseToolLane: %v", err)
	}
	if st := laneStats(t, e); st.EventsObserved != 1 {
		t.Fatalf("the queued batch must be drained before the lane stops: %+v", st)
	}
	exited := make(chan struct{})
	go func() { e.tools.workers.Wait(); close(exited) }()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("workers did not exit after CloseToolLane")
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > base && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > base {
		t.Fatalf("goroutines %d, want <= %d after close", n, base)
	}
	e.EvaluateToolCalls("sess-close", credCall)
	if st := laneStats(t, e); st.ToolBatchesRefused != 1 || st.ToolBatchesQueued != 1 {
		t.Fatalf("a closed lane refuses and counts: %+v", st)
	}
	if err := e.CloseToolLane(context.Background()); err != nil {
		t.Fatalf("a second close must be harmless: %v", err)
	}
}

// Review I-1: a stuck worker (a slow categorization) cannot hold
// CloseToolLane past its deadline; what is still queued is abandoned and
// counted, and the stuck batch is abandoned at the next call.
func TestToolLane_CloseWithAStuckWorkerReturnsAtTheDeadline(t *testing.T) {
	e := toolLaneEngine(t, 0)
	stuck := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	e.tools.categorize = func(job *toolJob, i int) []string {
		once.Do(func() { close(stuck); <-release })
		return job.taxonomy.Categorize(job.calls[i].Name, job.calls[i].Arguments)
	}
	e.EvaluateToolCalls("sess-stuck", append(append([]ToolCall{}, credCall...), credCall...))
	for i := 0; i < 3; i++ {
		e.EvaluateToolCalls("sess-stuck", credCall)
	}
	<-stuck
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := e.CloseToolLane(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseToolLane = %v, want the deadline", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("CloseToolLane took %v with a stuck worker", d)
	}
	close(release)
	flushLane(t, e)
	if st := laneStats(t, e); st.ToolBatchesAbandoned != 4 || st.EventsQueued != 0 {
		t.Fatalf("want the stuck batch and the 3 queued abandoned, nothing observed: %+v", st)
	}
}

// Closing a lane that never started is safe, and later batches are refused.
func TestToolLane_CloseBeforeStart(t *testing.T) {
	e := toolLaneEngine(t, 0)
	if err := e.CloseToolLane(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.EvaluateToolCalls("sess-never", credCall)
	if st := laneStats(t, e); st.ToolBatchesRefused != 1 || st.ToolBatchesQueued != 0 {
		t.Fatalf("closed before start: %+v", st)
	}
	e.mu.RLock()
	n := len(e.toolSessions)
	e.mu.RUnlock()
	if n != 0 {
		t.Fatalf("a refused batch must not create tool state, got %d states", n)
	}
}

// Review I-2, step 2 retained check: a session retained after its batch
// was queued gets nothing observed.
func TestToolLane_RetainedBeforeAssignDiscardsTheBatch(t *testing.T) {
	e := toolLaneEngine(t, 0)
	release := make(chan struct{})
	e.tools.beforeCategorize = func() { <-release }
	e.EvaluateToolCalls("sess-ret2", credCall)
	e.mu.Lock()
	e.retained["sess-ret2"] = 1
	e.mu.Unlock()
	close(release)
	flushLane(t, e)
	if st := laneStats(t, e); st.EventsQueued != 0 || st.ToolBatchesDiscarded != 1 || st.Sessions != 0 {
		t.Fatalf("a batch for a retained session must be discarded before Observe: %+v", st)
	}
}
