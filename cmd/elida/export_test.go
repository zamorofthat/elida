package main

import (
	"testing"
	"time"

	"elida/internal/session"
)

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
