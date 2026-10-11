package unit

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
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

func TestReleaseFlaggedSession_RetainedEntryHoldsNoContent(t *testing.T) {
	quietSlog(t)
	pe := newRiskLadderEngine(baselineRules(), baselineThresholds())
	const sid = "sess-slim"
	const body = "request body zzmarkerzz with private words"
	res := pe.EvaluateMessages(sid, []policy.MessageToScan{{Role: "user", Index: 0, Content: body}})
	if res == nil || len(res.Violations) == 0 {
		t.Fatal("fixture: the marker must fire baseline_marker")
	}
	pe.CaptureRequest(sid, policy.CapturedRequest{Method: "POST", Path: "/v1", RequestBody: body})
	pe.AddExternalRiskPoints(sid, 60, "test")

	before := pe.GetFlaggedSession(sid)
	if len(before.CapturedContent) == 0 || len(before.ViolationEvents) == 0 {
		t.Fatal("fixture: the live entry holds captures and events")
	}
	if !pe.ReleaseFlaggedSession(sid) {
		t.Fatal("a terminate session must be retained")
	}

	fs := pe.GetFlaggedSession(sid)
	if fs == nil {
		t.Fatal("retained entry missing")
	}
	if len(fs.CapturedContent) != 0 || len(fs.ViolationEvents) != 0 {
		t.Fatalf("retained entry keeps captures=%d events=%d", len(fs.CapturedContent), len(fs.ViolationEvents))
	}
	raw, err := json.Marshal(fs)
	if err != nil {
		t.Fatal(err)
	}
	// MatchedPattern is the rule's own regex (configuration, not request
	// content), so the check looks for the request's other words.
	if strings.Contains(string(raw), "request body") || strings.Contains(string(raw), "private words") {
		t.Fatalf("retained entry holds content: %s", raw)
	}
	if len(raw) > 2048 {
		t.Errorf("retained entry is %d bytes, want about 1 KB", len(raw))
	}
	if len(fs.Violations) == 0 || fs.Violations[0].RuleName != "baseline_marker" {
		t.Fatalf("violations must stay (content-free): %+v", fs.Violations)
	}
	// The reused ID still meets terminate.
	if _, action, _ := pe.GetSessionRiskScore(sid); action != "terminate" || !pe.ShouldBlockByRisk(sid) {
		t.Fatalf("reused ID action = %q", action)
	}
	// The copy taken before the release is untouched (no in-place edit).
	if before.Violations[0].MatchedText == "" {
		t.Error("slimming must not edit a previously returned copy in place")
	}
}
