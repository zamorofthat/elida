package policy

import (
	"fmt"
	"testing"
	"time"

	"elida/internal/correlation"
)

// multiCategoryLaneConfig is toolLaneConfig plus a tool that falls into
// three wanted categories, so one full batch yields 3 x MaxToolCallsPerBatch
// events: more than one Observe accepts.
func multiCategoryLaneConfig() Config {
	cfg := toolLaneConfig(0, 64)
	cfg.Correlation.ToolCategories = append(cfg.Correlation.ToolCategories,
		correlation.ToolCategoryRule{Category: "c3", Tools: []string{"multi"}},
		correlation.ToolCategoryRule{Category: "c4", Tools: []string{"multi"}},
		correlation.ToolCategoryRule{Category: "c5", Tools: []string{"multi"}},
	)
	cfg.Correlation.Rules = append(cfg.Correlation.Rules, correlation.Rule{
		Name: "multi3", Shape: correlation.ShapeSequence, Sequence: []string{"c3", "c4", "c5"}, Window: time.Minute,
	})
	return cfg
}

// Final review M-2: a batch whose calls x categories exceed
// MaxEventsPerObserve is observed in chunks, so every event of the batch is
// evaluated and none is dropped.
func TestToolLane_BatchAboveObserveCapIsObservedInChunks(t *testing.T) {
	e := NewEngine(multiCategoryLaneConfig())
	calls := make([]ToolCall, MaxToolCallsPerBatch)
	for i := range calls {
		calls[i] = ToolCall{ID: fmt.Sprintf("c%d", i), Name: "multi", Arguments: "x"}
	}
	e.EvaluateToolCalls("s-multi", calls)
	flushLane(t, e)

	st := laneStats(t, e)
	want := uint64(3 * MaxToolCallsPerBatch)
	if want <= correlation.MaxEventsPerObserve {
		t.Fatalf("fixture: %d events must exceed MaxEventsPerObserve (%d)", want, correlation.MaxEventsPerObserve)
	}
	if st.EventsQueued != want {
		t.Fatalf("events_queued = %d, want %d", st.EventsQueued, want)
	}
	if st.EventsDropped != 0 {
		t.Fatalf("events_dropped = %d, want 0: a batch above the Observe cap lost events (%+v)", st.EventsDropped, st)
	}
	if st.EventsObserved != want {
		t.Fatalf("events_observed = %d, want %d (%+v)", st.EventsObserved, want, st)
	}
	if st.Matches == 0 {
		t.Fatalf("the sequence rule must still match across the batch: %+v", st)
	}
}
