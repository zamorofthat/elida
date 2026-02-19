package unit

import (
	"testing"

	"elida/internal/policy"
)

// Helper to create a risk-ladder enabled policy engine
func newRiskLadderEngine(rules []policy.Rule, thresholds []policy.RiskThreshold) *policy.Engine {
	return policy.NewEngine(policy.Config{
		Enabled:        true,
		Mode:           "enforce",
		CaptureContent: true,
		MaxCaptureSize: 10000,
		Rules:          rules,
		RiskLadder: policy.RiskLadderConfig{
			Enabled:    true,
			Thresholds: thresholds,
		},
	})
}

func TestRiskScore_Accumulation(t *testing.T) {
	rules := []policy.Rule{
		{
			Name:     "test_pattern",
			Type:     "content_match",
			Target:   "request",
			Patterns: []string{"BAD_WORD"},
			Severity: "warning",
			Action:   "flag",
		},
	}

	thresholds := []policy.RiskThreshold{
		{Score: 5, Action: policy.ActionWarn},
		{Score: 15, Action: policy.ActionBlock},
	}

	engine := newRiskLadderEngine(rules, thresholds)
	sessionID := "risk-test-1"

	// First violation
	engine.EvaluateRequestContent(sessionID, "This has BAD_WORD in it")
	score1, action1, _ := engine.GetSessionRiskScore(sessionID)

	if score1 == 0 {
		t.Error("expected non-zero risk score after first violation")
	}
	if action1 != string(policy.ActionObserve) && action1 != string(policy.ActionWarn) {
		t.Logf("score: %f, action: %s", score1, action1)
	}

	// Second violation should increase score
	engine.EvaluateRequestContent(sessionID, "Another BAD_WORD here")
	score2, _, _ := engine.GetSessionRiskScore(sessionID)

	if score2 <= score1 {
		t.Errorf("expected score to increase: first=%f, second=%f", score1, score2)
	}

	// Many violations should escalate action
	for i := 0; i < 5; i++ {
		engine.EvaluateRequestContent(sessionID, "More BAD_WORD content")
	}

	scoreFinal, actionFinal, _ := engine.GetSessionRiskScore(sessionID)
	t.Logf("Final score: %f, action: %s", scoreFinal, actionFinal)

	if scoreFinal < 15 {
		t.Errorf("expected score >= 15 after many violations, got %f", scoreFinal)
	}
}

func TestRiskScore_SeverityWeights(t *testing.T) {
	// Test that critical violations add more risk than info
	infoRule := []policy.Rule{
		{
			Name:     "info_pattern",
			Type:     "content_match",
			Target:   "request",
			Patterns: []string{"INFO_MATCH"},
			Severity: "info",
			Action:   "flag",
		},
	}

	criticalRule := []policy.Rule{
		{
			Name:     "critical_pattern",
			Type:     "content_match",
			Target:   "request",
			Patterns: []string{"CRITICAL_MATCH"},
			Severity: "critical",
			Action:   "flag",
		},
	}

	thresholds := []policy.RiskThreshold{
		{Score: 5, Action: policy.ActionWarn},
	}

	infoEngine := newRiskLadderEngine(infoRule, thresholds)
	criticalEngine := newRiskLadderEngine(criticalRule, thresholds)

	// Single info violation
	infoEngine.EvaluateRequestContent("info-session", "INFO_MATCH")
	infoScore, _, _ := infoEngine.GetSessionRiskScore("info-session")

	// Single critical violation
	criticalEngine.EvaluateRequestContent("critical-session", "CRITICAL_MATCH")
	criticalScore, _, _ := criticalEngine.GetSessionRiskScore("critical-session")

	t.Logf("Info score: %f, Critical score: %f", infoScore, criticalScore)

	if criticalScore <= infoScore {
		t.Errorf("expected critical score (%f) > info score (%f)", criticalScore, infoScore)
	}

	// Verify weight ratio (critical=10, info=1)
	expectedRatio := policy.SeverityWeights[policy.SeverityCritical] / policy.SeverityWeights[policy.SeverityInfo]
	actualRatio := criticalScore / infoScore

	if actualRatio != expectedRatio {
		t.Errorf("expected weight ratio %f, got %f", expectedRatio, actualRatio)
	}
}

func TestRiskLadder_Escalation(t *testing.T) {
	rules := []policy.Rule{
		{
			Name:     "test_pattern",
			Type:     "content_match",
			Target:   "request",
			Patterns: []string{"VIOLATION"},
			Severity: "warning", // Weight = 3
			Action:   "flag",
		},
	}

	thresholds := []policy.RiskThreshold{
		{Score: 5, Action: policy.ActionWarn},
		{Score: 15, Action: policy.ActionThrottle, ThrottleRate: 10},
		{Score: 30, Action: policy.ActionBlock},
		{Score: 50, Action: policy.ActionTerminate},
	}

	engine := newRiskLadderEngine(rules, thresholds)
	sessionID := "escalation-test"

	// First violation (score=3) - should be observe or warn
	engine.EvaluateRequestContent(sessionID, "VIOLATION")
	_, action1, _ := engine.GetSessionRiskScore(sessionID)
	t.Logf("After 1 violation: action=%s", action1)

	// 2nd violation (score=6) - should be warn
	engine.EvaluateRequestContent(sessionID, "VIOLATION")
	score2, action2, _ := engine.GetSessionRiskScore(sessionID)
	t.Logf("After 2 violations: score=%f, action=%s", score2, action2)
	if action2 != string(policy.ActionWarn) {
		t.Errorf("expected warn at score %f, got %s", score2, action2)
	}

	// 5th violation (score=15) - should be throttle
	for i := 0; i < 3; i++ {
		engine.EvaluateRequestContent(sessionID, "VIOLATION")
	}
	score5, action5, throttleRate := engine.GetSessionRiskScore(sessionID)
	t.Logf("After 5 violations: score=%f, action=%s, throttle=%d", score5, action5, throttleRate)
	if action5 != string(policy.ActionThrottle) {
		t.Errorf("expected throttle at score %f, got %s", score5, action5)
	}
	if throttleRate != 10 {
		t.Errorf("expected throttle rate 10, got %d", throttleRate)
	}

	// 10th violation (score=30) - should be block
	for i := 0; i < 5; i++ {
		engine.EvaluateRequestContent(sessionID, "VIOLATION")
	}
	score10, action10, _ := engine.GetSessionRiskScore(sessionID)
	t.Logf("After 10 violations: score=%f, action=%s", score10, action10)
	if action10 != string(policy.ActionBlock) {
		t.Errorf("expected block at score %f, got %s", score10, action10)
	}

	// 17th violation (score=51) - should be terminate
	for i := 0; i < 7; i++ {
		engine.EvaluateRequestContent(sessionID, "VIOLATION")
	}
	scoreFinal, actionFinal, _ := engine.GetSessionRiskScore(sessionID)
	t.Logf("After 17 violations: score=%f, action=%s", scoreFinal, actionFinal)
	if actionFinal != string(policy.ActionTerminate) {
		t.Errorf("expected terminate at score %f, got %s", scoreFinal, actionFinal)
	}
}

func TestRiskLadder_Throttle(t *testing.T) {
	rules := []policy.Rule{
		{
			Name:     "test_pattern",
			Type:     "content_match",
			Target:   "request",
			Patterns: []string{"VIOLATION"},
			Severity: "warning",
			Action:   "flag",
		},
	}

	thresholds := []policy.RiskThreshold{
		{Score: 3, Action: policy.ActionThrottle, ThrottleRate: 5},
	}

	engine := newRiskLadderEngine(rules, thresholds)
	sessionID := "throttle-test"

	// Before any violations - should not throttle
	shouldThrottle, _ := engine.ShouldThrottle(sessionID)
	if shouldThrottle {
		t.Error("should not throttle before any violations")
	}

	// Trigger violation
	engine.EvaluateRequestContent(sessionID, "VIOLATION")

	// After violation - should throttle
	shouldThrottle, rate := engine.ShouldThrottle(sessionID)
	if !shouldThrottle {
		t.Error("should throttle after violation")
	}
	if rate != 5 {
		t.Errorf("expected throttle rate 5, got %d", rate)
	}
}

func TestRiskLadder_ShouldBlock(t *testing.T) {
	rules := []policy.Rule{
		{
			Name:     "critical_pattern",
			Type:     "content_match",
			Target:   "request",
			Patterns: []string{"CRITICAL"},
			Severity: "critical", // Weight = 10
			Action:   "flag",
		},
	}

	thresholds := []policy.RiskThreshold{
		{Score: 20, Action: policy.ActionBlock},
	}

	engine := newRiskLadderEngine(rules, thresholds)
	sessionID := "block-test"

	// First violation (score=10) - should not block
	engine.EvaluateRequestContent(sessionID, "CRITICAL")
	if engine.ShouldBlockByRisk(sessionID) {
		t.Error("should not block after first violation")
	}

	// Second violation (score=20) - should block
	engine.EvaluateRequestContent(sessionID, "CRITICAL")
	if !engine.ShouldBlockByRisk(sessionID) {
		t.Error("should block after second violation")
	}
}

func TestRiskLadder_ShouldTerminate(t *testing.T) {
	rules := []policy.Rule{
		{
			Name:     "critical_pattern",
			Type:     "content_match",
			Target:   "request",
			Patterns: []string{"DANGER"},
			Severity: "critical", // Weight = 10
			Action:   "flag",
		},
	}

	thresholds := []policy.RiskThreshold{
		{Score: 50, Action: policy.ActionTerminate},
	}

	engine := newRiskLadderEngine(rules, thresholds)
	sessionID := "terminate-test"

	// Not enough violations - should not terminate
	for i := 0; i < 4; i++ {
		engine.EvaluateRequestContent(sessionID, "DANGER")
	}
	if engine.ShouldTerminateByRisk(sessionID) {
		t.Error("should not terminate below threshold")
	}

	// 5th violation (score=50) - should terminate
	engine.EvaluateRequestContent(sessionID, "DANGER")
	if !engine.ShouldTerminateByRisk(sessionID) {
		t.Error("should terminate at threshold")
	}
}

func TestRiskLadder_DefaultThresholds(t *testing.T) {
	// Test that default thresholds are applied when enabled but none specified
	rules := []policy.Rule{
		{
			Name:     "test_pattern",
			Type:     "content_match",
			Target:   "request",
			Patterns: []string{"TEST"},
			Severity: "critical",
			Action:   "flag",
		},
	}

	engine := policy.NewEngine(policy.Config{
		Enabled: true,
		Mode:    "enforce",
		Rules:   rules,
		RiskLadder: policy.RiskLadderConfig{
			Enabled:    true,
			Thresholds: nil, // No thresholds specified - should use defaults
		},
	})

	// Verify engine is working
	if !engine.IsRiskLadderEnabled() {
		t.Error("expected risk ladder to be enabled")
	}

	sessionID := "default-test"
	engine.EvaluateRequestContent(sessionID, "TEST")

	score, action, _ := engine.GetSessionRiskScore(sessionID)
	t.Logf("Score: %f, Action: %s", score, action)

	if score == 0 {
		t.Error("expected non-zero score")
	}
}

func TestRiskLadder_Stats(t *testing.T) {
	rules := []policy.Rule{
		{
			Name:     "test_pattern",
			Type:     "content_match",
			Target:   "request",
			Patterns: []string{"TRIGGER"},
			Severity: "critical",
			Action:   "flag",
		},
	}

	thresholds := []policy.RiskThreshold{
		{Score: 10, Action: policy.ActionThrottle, ThrottleRate: 10},
		{Score: 30, Action: policy.ActionBlock},
	}

	engine := newRiskLadderEngine(rules, thresholds)

	// Create some flagged sessions with different risk levels
	engine.EvaluateRequestContent("session1", "TRIGGER") // score=10, throttled
	engine.EvaluateRequestContent("session2", "TRIGGER")
	engine.EvaluateRequestContent("session2", "TRIGGER")
	engine.EvaluateRequestContent("session2", "TRIGGER") // score=30, blocked

	stats := engine.Stats()

	t.Logf("Stats: %+v", stats)

	if stats["risk_ladder"] != true {
		t.Error("expected risk_ladder=true in stats")
	}

	throttled, ok := stats["throttled"].(int)
	if !ok || throttled < 1 {
		t.Error("expected at least 1 throttled session")
	}

	blocked, ok := stats["blocked"].(int)
	if !ok || blocked < 1 {
		t.Error("expected at least 1 blocked session")
	}

	avgScore, ok := stats["avg_risk_score"].(float64)
	if !ok || avgScore == 0 {
		t.Error("expected non-zero average risk score")
	}
}

func TestRiskLadder_Disabled(t *testing.T) {
	rules := []policy.Rule{
		{
			Name:     "test_pattern",
			Type:     "content_match",
			Target:   "request",
			Patterns: []string{"TEST"},
			Severity: "warning",
			Action:   "flag",
		},
	}

	// Engine without risk ladder
	engine := policy.NewEngine(policy.Config{
		Enabled: true,
		Mode:    "enforce",
		Rules:   rules,
		RiskLadder: policy.RiskLadderConfig{
			Enabled: false,
		},
	})

	if engine.IsRiskLadderEnabled() {
		t.Error("expected risk ladder to be disabled")
	}

	sessionID := "disabled-test"
	engine.EvaluateRequestContent(sessionID, "TEST")

	score, _, _ := engine.GetSessionRiskScore(sessionID)
	if score != 0 {
		t.Errorf("expected zero score when risk ladder disabled, got %f", score)
	}

	// ShouldThrottle should return false
	shouldThrottle, _ := engine.ShouldThrottle(sessionID)
	if shouldThrottle {
		t.Error("should not throttle when risk ladder disabled")
	}
}
