package policy

import "testing"

func ladderEngine(mode string) *Engine {
	return NewEngine(Config{
		Enabled: true,
		Mode:    mode,
		RiskLadder: RiskLadderConfig{
			Enabled: true, // default thresholds: 5 warn, 15 throttle, 30 block, 50 terminate
		},
	})
}

// Feedback #2: "dry run" must not block. In audit mode the ladder may
// observe/warn but never throttle, block, or terminate — this is the exact
// "HTTP 403: Session risk score too high in mode: audit" bug.
func TestAuditModeLadderNeverBlocks(t *testing.T) {
	e := ladderEngine("audit")
	e.AddExternalRiskPoints("sess-audit", 100, "test") // far past every threshold

	if e.ShouldBlockByRisk("sess-audit") {
		t.Error("audit mode: ShouldBlockByRisk = true, want false")
	}
	if e.ShouldTerminateByRisk("sess-audit") {
		t.Error("audit mode: ShouldTerminateByRisk = true, want false")
	}
	if throttled, _ := e.ShouldThrottle("sess-audit"); throttled {
		t.Error("audit mode: ShouldThrottle = true, want false")
	}
}

// The score itself must still be computed and visible in audit mode —
// audit means observe, not off.
func TestAuditModeStillComputesRiskScore(t *testing.T) {
	e := ladderEngine("audit")
	e.AddExternalRiskPoints("sess-audit", 40, "test")

	score, action, _ := e.GetSessionRiskScore("sess-audit")
	if score <= 0 {
		t.Errorf("audit mode: risk score = %v, want > 0 (score must still accumulate)", score)
	}
	if action == string(ActionBlock) || action == string(ActionTerminate) || action == string(ActionThrottle) {
		t.Errorf("audit mode: current action = %q, want observe/warn only", action)
	}
}

// Enforce mode is unchanged: same points, ladder blocks.
func TestEnforceModeLadderStillBlocks(t *testing.T) {
	e := ladderEngine("enforce")
	e.AddExternalRiskPoints("sess-enforce", 100, "test")

	if !e.ShouldBlockByRisk("sess-enforce") {
		t.Error("enforce mode: ShouldBlockByRisk = false, want true")
	}
}

// mode is enforce unless explicitly set to audit — pins the default
func TestUnsetModeDefaultsToEnforce(t *testing.T) {
	e := NewEngine(Config{
		Enabled: true,
		RiskLadder: RiskLadderConfig{
			Enabled: true,
		},
	})

	if e.IsAuditMode() {
		t.Error("unset mode: IsAuditMode = true, want false (enforce is the default)")
	}

	e.AddExternalRiskPoints("sess-default", 100, "test")

	if !e.ShouldBlockByRisk("sess-default") {
		t.Error("unset mode: ShouldBlockByRisk = false, want true (enforce is the default)")
	}
}
