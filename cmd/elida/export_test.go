package main

import (
	"context"
	"os"
	"testing"
	"time"

	"elida/internal/session"
	"elida/internal/storage"
)

func newTestStore(t *testing.T) *storage.SQLiteStore {
	t.Helper()
	f, err := os.CreateTemp("", "elida-export-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	store, err := storage.NewSQLiteStore(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestToolCallHistory_PrefersToolSequence(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Millisecond)

	// A tool_sequence event carries faithful order + timing.
	if err := store.RecordEvent(ctx, storage.EventToolSequence, "sess-seq", "", storage.ToolSequenceData{Calls: []storage.ToolCall{
		{ToolName: "read", Timestamp: base},
		{ToolName: "edit", Timestamp: base.Add(150 * time.Millisecond)},
		{ToolName: "read", Timestamp: base.Add(400 * time.Millisecond)},
	}}); err != nil {
		t.Fatal(err)
	}
	// An aggregate tool_called event (map order) is also present; it must be ignored.
	if err := store.RecordEvent(ctx, storage.EventToolCalled, "sess-seq", "", storage.ToolCalledData{ToolName: "bash", CallCount: 5}); err != nil {
		t.Fatal(err)
	}

	got, err := toolCallHistory(store, "sess-seq")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	wantNames := []string{"read", "edit", "read"}
	for i, name := range wantNames {
		if got[i].ToolName != name {
			t.Errorf("call %d name = %q, want %q", i, got[i].ToolName, name)
		}
	}
	if !got[1].Timestamp.Equal(base.Add(150 * time.Millisecond)) {
		t.Errorf("call 1 ts = %v, want %v", got[1].Timestamp, base.Add(150*time.Millisecond))
	}

	// The faithful sequence yields real inter-call durations end-to-end.
	turns := toAgentTurns("sess-seq", "global", got)
	if turns[1].DurationMs != 150 || turns[2].DurationMs != 250 {
		t.Errorf("durations = %d,%d want 150,250", turns[1].DurationMs, turns[2].DurationMs)
	}
}

func TestToolCallHistory_FallsBackToAggregate(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// No tool_sequence event: only aggregate tool_called events exist
	// (a pre-migration session). Fall back to count-expansion.
	if err := store.RecordEvent(ctx, storage.EventToolCalled, "sess-agg", "", storage.ToolCalledData{ToolName: "read", CallCount: 2}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordEvent(ctx, storage.EventToolCalled, "sess-agg", "", storage.ToolCalledData{ToolName: "edit", CallCount: 1}); err != nil {
		t.Fatal(err)
	}

	got, err := toolCallHistory(store, "sess-agg")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (2 read + 1 edit)", len(got))
	}
	counts := map[string]int{}
	for _, r := range got {
		counts[r.ToolName]++
	}
	if counts["read"] != 2 || counts["edit"] != 1 {
		t.Errorf("counts = %v, want read:2 edit:1", counts)
	}
}

func TestToAgentTurns_ProjectsHistory(t *testing.T) {
	base := time.Now()
	h := []session.ToolCallRecord{
		{ToolName: "read", Timestamp: base},
		{ToolName: "bash", Timestamp: base.Add(200 * time.Millisecond)},
	}
	got := toAgentTurns("sess-1", "anthropic", h)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Signal != "agent_turn" || got[0].Tool != "read" || got[0].Turn != 0 || got[0].DurationMs != 0 || got[0].Entity != "anthropic" || got[0].SessionID != "sess-1" {
		t.Fatalf("turn0 = %+v", got[0])
	}
	if got[1].Tool != "bash" || got[1].Turn != 1 || got[1].DurationMs != 200 {
		t.Fatalf("turn1 = %+v", got[1])
	}
}
