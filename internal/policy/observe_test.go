package policy

import (
	"testing"
	"time"
)

// Observe-only rules flag and capture but contribute zero to the risk ladder
// (feedback #3: statistical/PII rules are noise on coding-agent traffic —
// they must not drive escalation).
func TestObserveRuleViolationsDoNotFeedRiskLadder(t *testing.T) {
	e := NewEngine(Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []Rule{
			{Name: "observe_shell", Type: RuleTypeContentMatch, Target: RuleTargetRequest,
				Patterns: []string{"sudo\\s+rm"}, Severity: SeverityCritical,
				Action: "flag", Observe: true},
		},
		RiskLadder: RiskLadderConfig{Enabled: true},
	})

	// Record many critical observe-only violations — enough that, if they
	// fed the ladder, the session would be far past the block threshold (30).
	for i := 0; i < 50; i++ {
		e.recordViolations("sess-observe", []Violation{{
			RuleName:  "observe_shell",
			Severity:  SeverityCritical,
			Action:    "flag",
			Timestamp: time.Now(),
		}})
	}

	// Still flagged and visible…
	if !e.IsFlagged("sess-observe") {
		t.Fatal("observe-only violations must still flag the session")
	}
	// …but the ladder never moved.
	score, _, _ := e.GetSessionRiskScore("sess-observe")
	if score != 0 {
		t.Errorf("risk score = %v, want 0 (observe-only rules contribute nothing)", score)
	}
	if e.ShouldBlockByRisk("sess-observe") {
		t.Error("ShouldBlockByRisk = true from observe-only violations, want false")
	}
}

// Non-observe rules are unaffected.
func TestNonObserveRulesStillFeedRiskLadder(t *testing.T) {
	e := NewEngine(Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []Rule{
			{Name: "real_rule", Type: RuleTypeContentMatch, Target: RuleTargetRequest,
				Patterns: []string{"x"}, Severity: SeverityCritical, Action: "flag"},
		},
		RiskLadder: RiskLadderConfig{Enabled: true},
	})

	for i := 0; i < 50; i++ {
		e.recordViolations("sess-real", []Violation{{
			RuleName:  "real_rule",
			Severity:  SeverityCritical,
			Action:    "flag",
			Timestamp: time.Now(),
		}})
	}

	score, _, _ := e.GetSessionRiskScore("sess-real")
	if score <= 0 {
		t.Errorf("risk score = %v, want > 0 for non-observe violations", score)
	}
}
