package policy

import (
	"testing"
	"time"
)

// Shadow rules flag and capture but contribute zero to the risk ladder
// (feedback #3: statistical/PII rules are noise on coding-agent traffic —
// they must not drive escalation).
func TestShadowRuleViolationsDoNotFeedRiskLadder(t *testing.T) {
	e := NewEngine(Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []Rule{
			{Name: "shadow_shell", Type: RuleTypeContentMatch, Target: RuleTargetRequest,
				Patterns: []string{"sudo\\s+rm"}, Severity: SeverityCritical,
				Action: "flag", Shadow: true},
		},
		RiskLadder: RiskLadderConfig{Enabled: true},
	})

	// Record many critical shadow violations — enough that, if they fed the
	// ladder, the session would be far past the block threshold (30).
	for i := 0; i < 50; i++ {
		e.recordViolations("sess-shadow", []Violation{{
			RuleName:  "shadow_shell",
			Severity:  SeverityCritical,
			Action:    "flag",
			Timestamp: time.Now(),
		}})
	}

	// Still flagged and visible…
	if !e.IsFlagged("sess-shadow") {
		t.Fatal("shadow violations must still flag the session")
	}
	// …but the ladder never moved.
	score, _, _ := e.GetSessionRiskScore("sess-shadow")
	if score != 0 {
		t.Errorf("risk score = %v, want 0 (shadow rules contribute nothing)", score)
	}
	if e.ShouldBlockByRisk("sess-shadow") {
		t.Error("ShouldBlockByRisk = true from shadow-only violations, want false")
	}
}

// Non-shadow rules are unaffected.
func TestNonShadowRulesStillFeedRiskLadder(t *testing.T) {
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
		t.Errorf("risk score = %v, want > 0 for non-shadow violations", score)
	}
}
