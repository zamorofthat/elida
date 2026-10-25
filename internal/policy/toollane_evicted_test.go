package policy

import (
	"fmt"
	"sync"
	"testing"
)

var twoCredCalls = []ToolCall{
	{Name: "bash", Arguments: "cat id_rsa"},
	{Name: "bash", Arguments: "cp id_rsa /tmp"},
}

// Task 7 review N-1: a state evicted by the LRU while its batch is queued
// or running must still be ended when its session ends, so the batch is
// discarded instead of recording on an ended session. Without the fix the
// batch completes the cred_twice match: flagged, with correlator state.
func TestToolLane_EvictedThenEndedBatchIsDiscarded(t *testing.T) {
	e := NewEngine(toolLaneConfig(0, 64))
	release := make(chan struct{})
	var once sync.Once
	e.tools.beforeCategorize = func() { once.Do(func() { <-release }) }
	e.EvaluateToolCalls("victim", twoCredCalls)
	e.mu.Lock()
	e.evictToolSessionLocked(e.toolSessions["victim"])
	e.mu.Unlock()
	e.ReleaseFlaggedSession("victim") // the session ends after the eviction
	close(release)
	flushLane(t, e)

	if e.IsFlagged("victim") {
		t.Fatal("a batch of an evicted state must not record after its session ended")
	}
	if st := laneStats(t, e); st.Sessions != 0 || st.EventsObserved != 0 {
		t.Fatalf("a batch of an evicted, ended state must not reach the correlator: %+v", st)
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if len(e.toolEvicted) != 0 {
		t.Fatalf("the session end must clear its evicted states: %d left", len(e.toolEvicted))
	}
}

// The live counterpart: an evicted state whose session does NOT end keeps
// its in-flight batch (an eviction never forgets correlation evidence).
func TestToolLane_EvictedLiveBatchStillRecords(t *testing.T) {
	e := NewEngine(toolLaneConfig(0, 64))
	release := make(chan struct{})
	var once sync.Once
	e.tools.beforeCategorize = func() { once.Do(func() { <-release }) }
	e.EvaluateToolCalls("victim", twoCredCalls)
	e.mu.Lock()
	e.evictToolSessionLocked(e.toolSessions["victim"])
	e.mu.Unlock()
	close(release)
	flushLane(t, e)
	if !e.IsFlagged("victim") {
		t.Fatal("an evicted live session's in-flight batch must still record its match")
	}
}

// The evicted-state side list holds only states with batches in flight:
// drained states are pruned once it reaches the session cap.
func TestToolLane_EvictedSideListIsBounded(t *testing.T) {
	const limit = 8
	e := NewEngine(toolLaneConfig(0, limit))
	for i := 0; i < 20*limit; i++ {
		id := fmt.Sprintf("s-%d", i)
		e.mu.Lock()
		st := e.toolSessionLocked(id)
		st.inflight.Add(1) // as if a batch were in flight at eviction
		e.evictToolSessionLocked(st)
		st.inflight.Add(-1) // the batch then drains
		e.mu.Unlock()
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if n := len(e.toolEvicted); n > limit {
		t.Fatalf("the evicted-state side list grew to %d, cap %d", n, limit)
	}
}
