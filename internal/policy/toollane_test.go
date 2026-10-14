package policy

import (
	"context"
	"strings"
	"testing"
	"time"

	"elida/internal/correlation"
)

func toolLaneEngine(t *testing.T, maxArgs int) *Engine {
	t.Helper()
	return NewEngine(Config{
		Enabled: true, Mode: "enforce",
		Correlation: CorrelationConfig{
			Enabled: true,
			Rules: []correlation.Rule{{
				Name: "cred_twice", Shape: correlation.ShapeRepeated,
				Category: "credential_discovery", Count: 2, Window: time.Minute,
			}},
			Limits: correlation.Limits{MaxHistoryEvents: 64, MaxSessions: 64},
			Taxonomy: correlation.TaxonomyConfig{
				MaxArgumentBytes: maxArgs,
				ToolCategories: []correlation.ToolCategoryRule{
					{Category: "credential_discovery", Tools: []string{"bash"}, ArgumentPatterns: []string{`id_rsa`}},
				},
			},
		},
	})
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

// The binding ruling: categorization runs off the request path and under no
// lock. EvaluateToolCalls returns while categorization is held at a gate,
// and at the gate the policy lock is free.
func TestToolLane_RequestPathReturnsBeforeCategorization(t *testing.T) {
	e := toolLaneEngine(t, 0)
	entered := make(chan struct{})
	release := make(chan struct{})
	lockFree := make(chan bool, 1)
	e.tools.beforeCategorize = func() {
		ok := e.mu.TryLock()
		if ok {
			e.mu.Unlock()
		}
		lockFree <- ok
		close(entered)
		<-release
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
	if !<-lockFree {
		t.Fatal("categorization ran with the policy lock held")
	}
	if st := laneStats(t, e); st.EventsObserved != 0 || st.EventsQueued != 0 {
		t.Fatalf("nothing may be observed before categorization: %+v", st)
	}
	close(release)
	flushLane(t, e)
	if st := laneStats(t, e); st.EventsObserved != 1 || st.ToolBatchesQueued != 1 {
		t.Fatalf("after the gate opens the call is observed: %+v", st)
	}
}

// One session's batches go to one worker with a ToolQueuePerWorker FIFO.
// With that worker held, the queue fills and further batches are dropped
// and counted, without blocking the caller.
func TestToolLane_QueueBoundDropsAndCounts(t *testing.T) {
	e := toolLaneEngine(t, 0)
	release := make(chan struct{})
	e.tools.beforeCategorize = func() { <-release }
	const n = ToolQueuePerWorker + 50
	start := time.Now()
	for i := 0; i < n; i++ {
		e.EvaluateToolCalls("sess-full", []ToolCall{{Name: "bash", Arguments: "id_rsa"}})
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("enqueueing blocked: %v", d)
	}
	st := laneStats(t, e)
	if st.ToolBatchesQueued+st.ToolBatchesDropped != n ||
		st.ToolBatchesQueued > ToolQueuePerWorker+1 || st.ToolBatchesDropped < n-(ToolQueuePerWorker+1) {
		t.Fatalf("queue bound: queued %d dropped %d of %d", st.ToolBatchesQueued, st.ToolBatchesDropped, n)
	}
	close(release)
	flushLane(t, e)
	if held := e.tools.heldBytes.Load(); held != 0 {
		t.Fatalf("held bytes after flush = %d", held)
	}
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
// the session. The first use keeps it, a repeat and every call past the
// per-session cap fall back to "" (anonymous), and ordinals never repeat.
func TestToolLane_CallIDResolution(t *testing.T) {
	s := &toolSession{callIDs: make(map[string]struct{})}
	if id, _ := s.identify("call_1"); id != "call_1" {
		t.Fatalf("first use keeps the ID, got %q", id)
	}
	if id, _ := s.identify("call_1"); id != "" {
		t.Fatalf("a repeated ID must fall back to anonymous, got %q", id)
	}
	if id, _ := s.identify(""); id != "" {
		t.Fatalf("absent stays absent, got %q", id)
	}
	for i := len(s.callIDs); i < MaxToolCallIDsPerSession; i++ {
		s.identify("fill_" + string(rune('a'+i%26)) + time.Duration(i).String())
	}
	if id, _ := s.identify("call_new"); id != "" {
		t.Fatalf("past the cap a new ID cannot be proven unique, got %q", id)
	}
	_, o1 := s.identify("")
	_, o2 := s.identify("")
	if o1 == o2 {
		t.Fatal("anonymous ordinals must be distinct")
	}
}

// A batch queued before its session ended is discarded: no flagged entry,
// no correlation state for the ended instance.
func TestToolLane_SessionEndDiscardsQueuedBatch(t *testing.T) {
	e := toolLaneEngine(t, 0)
	release := make(chan struct{})
	e.tools.beforeCategorize = func() { <-release }
	call := []ToolCall{{Name: "bash", Arguments: "id_rsa"}}
	e.EvaluateToolCalls("sess-end", call)
	e.EvaluateToolCalls("sess-end", call)
	e.ReleaseFlaggedSession("sess-end")
	close(release)
	flushLane(t, e)
	st := laneStats(t, e)
	if e.IsFlagged("sess-end") || st.ToolBatchesDiscarded != 2 || st.EventsObserved != 0 || st.Sessions != 0 {
		t.Fatalf("an ended session's queued batches must be discarded: flagged=%v %+v", e.IsFlagged("sess-end"), st)
	}
}
