package unit

import (
	"fmt"
	"io"
	"log/slog"
	"testing"

	"elida/internal/policy"
)

// quietSlog discards logs for the test: the engine logs every external
// risk addition at info.
func quietSlog(t *testing.T) {
	t.Helper()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
}

// ladderSession flags sid and drives it to the given action with external
// points against baselineThresholds (warn 5, throttle 15, block 30,
// terminate 50). points 0 records an evidence-only event, which flags the
// session at observe.
func ladderSession(t *testing.T, pe *policy.Engine, sid string, points int) {
	t.Helper()
	if points == 0 {
		pe.RecordSemanticViolation(sid, policy.Violation{
			RuleName: "injection_elevated", Severity: policy.SeverityInfo, SourceRole: "tool",
			EvidenceOnly: true, Semantic: &policy.SemanticEvidence{DecisionID: "d-" + sid},
		})
		return
	}
	pe.AddExternalRiskPoints(sid, points, "test")
}

func TestReleaseFlaggedSession_RemovesBelowBlock(t *testing.T) {
	quietSlog(t)
	for _, tc := range []struct {
		points int
		action string
	}{
		{0, "observe"},
		{6, "warn"},
		{16, "throttle"},
	} {
		pe := newRiskLadderEngine(baselineRules(), baselineThresholds())
		sid := "sess-release-" + tc.action
		ladderSession(t, pe, sid, tc.points)
		if _, action, _ := pe.GetSessionRiskScore(sid); action != tc.action {
			t.Fatalf("fixture: action = %q, want %q", action, tc.action)
		}
		if pe.ReleaseFlaggedSession(sid) {
			t.Errorf("%s: a session below block must not be retained", tc.action)
		}
		if pe.GetFlaggedSession(sid) != nil || pe.IsFlagged(sid) {
			t.Errorf("%s: the entry must be removed at session end", tc.action)
		}
		if n := pe.RetainedFlaggedSessions(); n != 0 {
			t.Errorf("%s: retained = %d", tc.action, n)
		}
	}
}

func TestReleaseFlaggedSession_RetainsBlockAndTerminate(t *testing.T) {
	quietSlog(t)
	for _, tc := range []struct {
		points int
		action string
	}{
		{31, "block"},
		{60, "terminate"},
	} {
		pe := newRiskLadderEngine(baselineRules(), baselineThresholds())
		sid := "sess-keep-" + tc.action
		ladderSession(t, pe, sid, tc.points)
		if !pe.ReleaseFlaggedSession(sid) {
			t.Fatalf("%s: must be retained", tc.action)
		}
		if _, action, _ := pe.GetSessionRiskScore(sid); action != tc.action {
			t.Errorf("%s: a reused ID must meet the same action, got %q", tc.action, action)
		}
		if !pe.ShouldBlockByRisk(sid) {
			t.Errorf("%s: a reused ID must still be blocked", tc.action)
		}
		// Releasing again (the reused session ends too) keeps one entry.
		if !pe.ReleaseFlaggedSession(sid) || pe.RetainedFlaggedSessions() != 1 {
			t.Errorf("%s: a second release must keep exactly one retained entry", tc.action)
		}
		pe.RemoveFlaggedSession(sid)
		if pe.RetainedFlaggedSessions() != 0 || pe.GetFlaggedSession(sid) != nil {
			t.Errorf("%s: RemoveFlaggedSession must drop a retained entry", tc.action)
		}
	}
}

func TestReleaseFlaggedSession_RetainedSetIsBounded(t *testing.T) {
	quietSlog(t)
	pe := newRiskLadderEngine(baselineRules(), baselineThresholds())
	const extra = 10
	total := policy.MaxRetainedFlaggedSessions + extra
	for i := 0; i < total; i++ {
		sid := fmt.Sprintf("sess-cap-%d", i)
		ladderSession(t, pe, sid, 60)
		pe.ReleaseFlaggedSession(sid)
	}
	if n := pe.RetainedFlaggedSessions(); n != policy.MaxRetainedFlaggedSessions {
		t.Fatalf("retained = %d, want the cap %d", n, policy.MaxRetainedFlaggedSessions)
	}
	for i := 0; i < extra; i++ {
		if pe.GetFlaggedSession(fmt.Sprintf("sess-cap-%d", i)) != nil {
			t.Fatalf("sess-cap-%d is among the oldest and must have been evicted", i)
		}
	}
	if pe.GetFlaggedSession(fmt.Sprintf("sess-cap-%d", total-1)) == nil {
		t.Fatal("the newest retained entry must be kept")
	}
}

func TestReleaseFlaggedSession_StaleOrderEntryEvictsNothing(t *testing.T) {
	quietSlog(t)
	pe := newRiskLadderEngine(baselineRules(), baselineThresholds())
	// A is retained, then B; A is removed and retained again. The FIFO is
	// now [A(stale), B, A]: going one past the cap must skip the stale slot
	// and evict B, the oldest current retention, not A.
	retain := func(sid string) {
		ladderSession(t, pe, sid, 60)
		pe.ReleaseFlaggedSession(sid)
	}
	retain("sess-a")
	retain("sess-b")
	pe.RemoveFlaggedSession("sess-a")
	retain("sess-a")
	for i := 0; i < policy.MaxRetainedFlaggedSessions-1; i++ {
		retain(fmt.Sprintf("sess-fill-%d", i))
	}
	if pe.GetFlaggedSession("sess-a") == nil {
		t.Fatal("a stale FIFO slot evicted a current retention")
	}
	if pe.GetFlaggedSession("sess-b") != nil {
		t.Fatal("the oldest current retention (B) must be evicted")
	}
	if n := pe.RetainedFlaggedSessions(); n != policy.MaxRetainedFlaggedSessions {
		t.Fatalf("retained = %d", n)
	}
}
