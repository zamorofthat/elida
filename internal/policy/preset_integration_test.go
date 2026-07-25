package policy

import (
	"testing"

	"elida/internal/config"
)

// toPolicyRules replicates the config.PolicyRule -> policy.Rule conversion
// done in cmd/elida/main.go, so preset rules loaded via the config package
// can be evaluated through a real policy.Engine.
func toPolicyRules(rules []config.PolicyRule) []Rule {
	out := make([]Rule, len(rules))
	for i, r := range rules {
		out[i] = Rule{
			Name:           r.Name,
			Type:           RuleType(r.Type),
			Target:         RuleTarget(r.Target),
			Threshold:      r.Threshold,
			ThresholdFloat: r.ThresholdFloat,
			MinSamples:     r.MinSamples,
			Patterns:       r.Patterns,
			Severity:       Severity(r.Severity),
			Description:    r.Description,
			Action:         r.Action,
			Observe:        r.Observe,
		}
	}
	return out
}

// codingAgentEngine loads the coding-agent preset through the real config
// layer (config.ApplyPolicyPreset) and builds a policy.Engine from the
// converted rules, mirroring what cmd/elida/main.go does at startup.
func codingAgentEngine(t *testing.T, mode string) *Engine {
	t.Helper()
	cfg := &config.Config{}
	cfg.Policy.Preset = "coding-agent"
	cfg.ApplyPolicyPreset()

	return NewEngine(Config{
		Enabled:    true,
		Mode:       mode,
		Rules:      toPolicyRules(cfg.Policy.Rules),
		RiskLadder: RiskLadderConfig{Enabled: true},
	})
}

// The coding-agent preset's block_dangerous_tools rule (glob shell_*) must
// block a shell_exec tool call through the real EvaluateToolCalls path,
// while leaving unrelated tool names untouched.
func TestCodingAgentPresetToolBlocking(t *testing.T) {
	e := codingAgentEngine(t, "enforce")

	blocked := e.EvaluateToolCalls("sess-tool-blocked", []ToolCall{
		{Name: "shell_exec", Arguments: "{}"},
	})
	if blocked == nil || !blocked.ShouldBlock {
		t.Fatalf("shell_exec tool call: result = %+v, want ShouldBlock = true", blocked)
	}

	benign := e.EvaluateToolCalls("sess-tool-benign", []ToolCall{
		{Name: "Read", Arguments: "{}"},
	})
	if benign != nil && benign.ShouldBlock {
		t.Errorf("benign tool call Read: result = %+v, want not blocked", benign)
	}
}

// dangerous_tool_arguments must block (not terminate) on a destructive
// command in tool arguments — the coding-agent preset treats a tool-arg
// pattern match as suspicious, not a confirmed breach.
func TestCodingAgentPresetDangerousArgsBlocked(t *testing.T) {
	e := codingAgentEngine(t, "enforce")

	result := e.EvaluateToolCalls("sess-dangerous-args", []ToolCall{
		{Name: "run_command", Arguments: `{"cmd":"rm -rf /tmp/demo"}`},
	})
	if result == nil || !result.ShouldBlock {
		t.Fatalf("dangerous tool arguments: result = %+v, want ShouldBlock = true", result)
	}
	if result.ShouldTerminate {
		t.Error("dangerous_tool_arguments: ShouldTerminate = true, want false (block, not terminate)")
	}
}

// PII content (SSN pattern) is observe-only in the coding-agent preset: it
// must flag and capture through EvaluateRequestContent but never block, and
// must not feed the risk ladder.
func TestCodingAgentPresetPIIObservedNotBlocked(t *testing.T) {
	e := codingAgentEngine(t, "enforce")

	result := e.EvaluateRequestContent("sess-pii", "my SSN is 123-45-6789")
	if result == nil {
		t.Fatal("expected a content check result for the matched PII pattern")
	}
	if result.ShouldBlock || result.ShouldTerminate {
		t.Errorf("PII observe rule: ShouldBlock=%v ShouldTerminate=%v, want both false",
			result.ShouldBlock, result.ShouldTerminate)
	}
	if !e.IsFlagged("sess-pii") {
		t.Error("session not flagged despite PII match")
	}
	score, _, _ := e.GetSessionRiskScore("sess-pii")
	if score != 0 {
		t.Errorf("risk score = %v, want 0 (observe rule must not feed the ladder)", score)
	}
}

// Risk-based termination survives the coding-agent preset: even though its
// content/statistical heuristics are all observe-only, the risk ladder
// itself (external points, e.g. from behavioral fingerprinting) still
// terminates at the default threshold.
func TestCodingAgentPresetLadderStillTerminates(t *testing.T) {
	e := codingAgentEngine(t, "enforce")

	e.AddExternalRiskPoints("sess-ladder", 100, "test")
	if !e.ShouldTerminateByRisk("sess-ladder") {
		t.Error("ShouldTerminateByRisk = false, want true after 100 external risk points")
	}
}
