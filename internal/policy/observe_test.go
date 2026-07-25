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

// observeContentEngine builds an enforce-mode engine with a single critical
// content_match rule, varying only Observe/Action, for the content-path
// tests below.
func observeContentEngine(observe bool, action string) *Engine {
	return NewEngine(Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []Rule{
			{Name: "shell_observe", Type: RuleTypeContentMatch, Target: RuleTargetRequest,
				Patterns: []string{`sudo\s+rm`}, Severity: SeverityCritical,
				Action: action, Observe: observe},
		},
		RiskLadder: RiskLadderConfig{Enabled: true},
	})
}

// Observe rules run through the real EvaluateRequestContent path: they must
// flag and capture but never block, and must not feed the risk ladder.
func TestObserveRuleContentMatchDoesNotBlock(t *testing.T) {
	e := observeContentEngine(true, "flag")

	result := e.EvaluateRequestContent("sess-observe-content", "please run sudo rm -rf /tmp/x")
	if result == nil {
		t.Fatal("expected a content check result for the matched observe rule")
	}
	if result.ShouldBlock {
		t.Error("ShouldBlock = true for observe rule, want false")
	}
	if result.ShouldTerminate {
		t.Error("ShouldTerminate = true for observe rule, want false")
	}
	if !e.IsFlagged("sess-observe-content") {
		t.Error("session not flagged despite observe rule match")
	}
	score, _, _ := e.GetSessionRiskScore("sess-observe-content")
	if score != 0 {
		t.Errorf("risk score = %v, want 0 (observe rule must not feed the ladder)", score)
	}
}

// A non-observe rule with the same pattern and a block action still blocks
// through the real content-evaluation path — observe-only is opt-in, not a
// change to normal enforcement.
func TestEnforcingRuleContentMatchStillBlocks(t *testing.T) {
	e := observeContentEngine(false, "block")

	result := e.EvaluateRequestContent("sess-enforce-content", "please run sudo rm -rf /tmp/x")
	if result == nil {
		t.Fatal("expected a content check result for the matched rule")
	}
	if !result.ShouldBlock {
		t.Error("ShouldBlock = false for enforcing block rule, want true")
	}
}

// The observe violation is deliberately visible-but-inert: it still raises
// the flagged session's MaxSeverity even though it contributes 0 risk score.
func TestObserveViolationStillRaisesMaxSeverity(t *testing.T) {
	e := observeContentEngine(true, "flag")

	e.EvaluateRequestContent("sess-observe-severity", "please run sudo rm -rf /tmp/x")

	fs := e.GetFlaggedSession("sess-observe-severity")
	if fs == nil {
		t.Fatal("expected a flagged session")
	}
	if fs.MaxSeverity != SeverityCritical {
		t.Errorf("MaxSeverity = %q, want %q", fs.MaxSeverity, SeverityCritical)
	}
}

// ReloadConfig must rebuild the observe index: flipping a rule's Observe
// flag via reload changes whether subsequent violations feed the risk
// ladder, without needing a process restart.
func TestReloadConfigRebuildsObserveIndex(t *testing.T) {
	observeRule := Rule{Name: "x", Type: RuleTypeContentMatch, Target: RuleTargetRequest,
		Patterns: []string{"x"}, Severity: SeverityCritical, Action: "flag", Observe: true}

	e := NewEngine(Config{
		Enabled:    true,
		Mode:       "enforce",
		Rules:      []Rule{observeRule},
		RiskLadder: RiskLadderConfig{Enabled: true},
	})

	e.recordViolations("sess-reload", []Violation{{
		RuleName: "x", Severity: SeverityCritical, Action: "flag", Timestamp: time.Now(),
	}})
	score, _, _ := e.GetSessionRiskScore("sess-reload")
	if score != 0 {
		t.Fatalf("pre-reload (observe=true): score = %v, want 0", score)
	}

	// Reload with the same rule name but observe:false.
	enforceRule := observeRule
	enforceRule.Observe = false
	e.ReloadConfig(Config{
		Enabled:    true,
		Mode:       "enforce",
		Rules:      []Rule{enforceRule},
		RiskLadder: RiskLadderConfig{Enabled: true},
	})

	e.recordViolations("sess-reload", []Violation{{
		RuleName: "x", Severity: SeverityCritical, Action: "flag", Timestamp: time.Now(),
	}})
	scoreAfterEnforce, _, _ := e.GetSessionRiskScore("sess-reload")
	if scoreAfterEnforce <= 0 {
		t.Fatalf("post-reload (observe=false): score = %v, want > 0", scoreAfterEnforce)
	}

	// Reload back to observe:true — further violations must add nothing
	// beyond decay of the already-recorded enforce-era events.
	e.ReloadConfig(Config{
		Enabled:    true,
		Mode:       "enforce",
		Rules:      []Rule{observeRule},
		RiskLadder: RiskLadderConfig{Enabled: true},
	})

	e.recordViolations("sess-reload", []Violation{{
		RuleName: "x", Severity: SeverityCritical, Action: "flag", Timestamp: time.Now(),
	}})
	scoreAfterObserveAgain, _, _ := e.GetSessionRiskScore("sess-reload")
	if scoreAfterObserveAgain > scoreAfterEnforce {
		t.Errorf("post-reload (observe=true again): score = %v, want <= %v (decay only, no new contribution)",
			scoreAfterObserveAgain, scoreAfterEnforce)
	}
}

// ReloadConfig must also switch the audit-mode ladder clamp live: a session
// flagged after reloading into audit mode must never be blocked/terminated,
// and a pre-existing session's next recompute must clamp too.
func TestReloadConfigSwitchesAuditClamp(t *testing.T) {
	e := NewEngine(Config{
		Enabled:    true,
		Mode:       "enforce",
		RiskLadder: RiskLadderConfig{Enabled: true},
	})

	e.AddExternalRiskPoints("sess-preexisting", 100, "test")
	if !e.ShouldBlockByRisk("sess-preexisting") {
		t.Fatal("enforce mode: ShouldBlockByRisk = false, want true")
	}

	e.ReloadConfig(Config{
		Enabled:    true,
		Mode:       "audit",
		RiskLadder: RiskLadderConfig{Enabled: true},
	})

	// A brand new session flagged post-reload must be clamped immediately.
	e.AddExternalRiskPoints("sess-postreload", 100, "test")
	if e.ShouldBlockByRisk("sess-postreload") {
		t.Error("audit mode post-reload: ShouldBlockByRisk = true for new session, want false")
	}

	// The pre-existing session's next recompute must also clamp.
	e.AddExternalRiskPoints("sess-preexisting", 1, "test")
	if e.ShouldBlockByRisk("sess-preexisting") {
		t.Error("audit mode post-reload: ShouldBlockByRisk = true for pre-existing session on recompute, want false")
	}
}
