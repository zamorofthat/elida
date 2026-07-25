package config

import (
	"strings"
	"testing"
)

func findRule(rules []PolicyRule, name string) *PolicyRule {
	for i := range rules {
		if rules[i].Name == name {
			return &rules[i]
		}
	}
	return nil
}

func countRule(rules []PolicyRule, name string) int {
	n := 0
	for i := range rules {
		if rules[i].Name == name {
			n++
		}
	}
	return n
}

// Feedback #1: a same-named custom rule must REPLACE the preset rule
// (local-overrides-default), not coexist with it.
func TestCustomRuleOverridesPresetRule(t *testing.T) {
	cfg := &Config{}
	cfg.Policy.Preset = "standard"
	cfg.Policy.Rules = []PolicyRule{
		{Name: "shell_execution", Type: "content_match", Target: "response",
			Patterns: []string{"bash\\s+-c\\s+"}, Severity: "warning", Action: "flag",
			Description: "softened for trusted coding agent"},
	}

	cfg.ApplyPolicyPreset()

	if got := countRule(cfg.Policy.Rules, "shell_execution"); got != 1 {
		t.Fatalf("shell_execution appears %d times, want exactly 1 (custom replaces preset)", got)
	}
	r := findRule(cfg.Policy.Rules, "shell_execution")
	if r.Action != "flag" {
		t.Errorf("shell_execution action = %q, want %q (custom rule must win)", r.Action, "flag")
	}
}

// Custom rules with unique names are still appended alongside preset rules.
func TestUniqueCustomRulesStillAppended(t *testing.T) {
	cfg := &Config{}
	cfg.Policy.Preset = "minimal"
	cfg.Policy.Rules = []PolicyRule{
		{Name: "my_custom_rule", Type: "content_match", Target: "request",
			Patterns: []string{"foo"}, Severity: "info", Action: "flag"},
	}

	cfg.ApplyPolicyPreset()

	if findRule(cfg.Policy.Rules, "my_custom_rule") == nil {
		t.Error("unique custom rule was dropped by the merge")
	}
	if findRule(cfg.Policy.Rules, "rate_limit_high") == nil {
		t.Error("preset rule rate_limit_high missing after merge")
	}
}

// Feedback #1: suppress_rules drops rules by name without redefining them.
func TestSuppressRulesRemovesPresetRule(t *testing.T) {
	cfg := &Config{}
	cfg.Policy.Preset = "standard"
	cfg.Policy.SuppressRules = []string{"destructive_file_ops", "compound_anomaly"}

	cfg.ApplyPolicyPreset()

	for _, name := range []string{"destructive_file_ops", "compound_anomaly"} {
		if findRule(cfg.Policy.Rules, name) != nil {
			t.Errorf("rule %q present after being listed in suppress_rules", name)
		}
	}
	// Sibling rules survive.
	if findRule(cfg.Policy.Rules, "destructive_file_ops_request") == nil {
		t.Error("destructive_file_ops_request was wrongly removed")
	}
}

// suppress_rules also applies to generated circuit-breaker rules and works
// with no preset set.
func TestSuppressRulesAppliesToCircuitBreakerAndNoPreset(t *testing.T) {
	cfg := &Config{}
	cfg.Policy.Preset = "minimal"
	cfg.Policy.CircuitBreaker.Enabled = true
	cfg.Policy.CircuitBreaker.MaxToolFanout = 30
	cfg.Policy.SuppressRules = []string{"circuit_breaker_tool_fanout"}

	cfg.ApplyPolicyPreset()

	if findRule(cfg.Policy.Rules, "circuit_breaker_tool_fanout") != nil {
		t.Error("generated circuit_breaker_tool_fanout present despite suppress_rules")
	}

	cfg2 := &Config{}
	cfg2.Policy.Rules = []PolicyRule{
		{Name: "noisy_rule", Type: "content_match", Patterns: []string{"x"}, Action: "flag"},
	}
	cfg2.Policy.SuppressRules = []string{"noisy_rule"}
	cfg2.ApplyPolicyPreset()
	if findRule(cfg2.Policy.Rules, "noisy_rule") != nil {
		t.Error("suppress_rules ignored when no preset is set")
	}
}

// An observe-only rule can never block: its action is normalized to flag at load.
func TestObserveRuleActionNormalizedToFlag(t *testing.T) {
	cfg := &Config{}
	cfg.Policy.Rules = []PolicyRule{
		{Name: "observed", Type: "content_match", Patterns: []string{"x"},
			Action: "block", Observe: true},
	}
	cfg.ApplyPolicyPreset()
	r := findRule(cfg.Policy.Rules, "observed")
	if r == nil || r.Action != "flag" {
		t.Fatalf("observe rule action = %v, want flag", r)
	}
}

// Feedback #3: the coding-agent preset enforces deterministic structural
// rules and observes the content/statistical heuristics that false-fire on
// legitimate agent traffic.
func TestCodingAgentPreset(t *testing.T) {
	cfg := &Config{}
	cfg.Policy.Preset = "coding-agent"
	cfg.ApplyPolicyPreset()

	if len(cfg.Policy.Rules) == 0 {
		t.Fatal("coding-agent preset produced no rules")
	}

	// Structural rules enforce.
	enforced := map[string]string{
		"block_dangerous_tools":    "block",
		"dangerous_tool_arguments": "block", // block, not terminate: a tool-arg pattern is not a confirmed breach
		"tool_credential_access":   "block",
	}
	for name, wantAction := range enforced {
		r := findRule(cfg.Policy.Rules, name)
		if r == nil {
			t.Errorf("enforced rule %q missing", name)
			continue
		}
		if r.Observe {
			t.Errorf("rule %q is observe, must enforce", name)
		}
		if r.Action != wantAction {
			t.Errorf("rule %q action = %q, want %q", name, r.Action, wantAction)
		}
	}

	// Content/statistical heuristics are observe — the exact rules that
	// broke real agent turns under `standard` (bash -c, sudo, rm -rf,
	// curl|sh) plus the measured-noisy anomaly rules.
	observed := []string{
		"shell_execution", "privilege_escalation", "destructive_file_ops",
		"network_exfiltration", "rate_anomaly", "compound_anomaly",
	}
	for _, name := range observed {
		r := findRule(cfg.Policy.Rules, name)
		if r == nil {
			t.Errorf("observe rule %q missing", name)
			continue
		}
		if !r.Observe {
			t.Errorf("rule %q must be observe in coding-agent preset", name)
		}
		if r.Action != "flag" {
			t.Errorf("rule %q action = %q, want flag", name, r.Action)
		}
	}

	// No rule in this preset may terminate — a coding agent's own output
	// must never kill the session.
	for _, r := range cfg.Policy.Rules {
		if r.Action == "terminate" {
			t.Errorf("rule %q has action terminate; coding-agent preset must not terminate", r.Name)
		}
	}
}

// Overriding a coding-agent rule by name still works (Task 1 semantics).
func TestCodingAgentPresetIsOverridable(t *testing.T) {
	cfg := &Config{}
	cfg.Policy.Preset = "coding-agent"
	cfg.Policy.Rules = []PolicyRule{
		{Name: "shell_execution", Type: "content_match", Target: "response",
			Patterns: []string{"bash\\s+-c\\s+"}, Severity: "critical", Action: "block",
			Description: "this deployment wants shell blocked"},
	}
	cfg.ApplyPolicyPreset()

	r := findRule(cfg.Policy.Rules, "shell_execution")
	if r == nil || r.Action != "block" || r.Observe {
		t.Fatalf("override failed: got %+v, want enforcing block rule", r)
	}
}

// Feedback #3 (bonus): a bare rate/entropy heuristic must not be worded as
// a confirmed exfiltration.
func TestAnomalyDescriptionsNotAlarmist(t *testing.T) {
	for _, preset := range [][]PolicyRule{getStandardPreset(), getCodingAgentPreset()} {
		if r := findRule(preset, "compound_anomaly"); r != nil {
			if containsFold(r.Description, "exfiltration") {
				t.Errorf("compound_anomaly description still says 'exfiltration': %q", r.Description)
			}
		}
	}
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}
