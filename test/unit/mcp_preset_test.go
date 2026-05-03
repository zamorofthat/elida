package unit

import (
	"testing"

	"elida/internal/config"
)

func newMCPConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Policy.Preset = "mcp"
	cfg.ApplyPolicyPreset()
	return cfg
}

func TestMCPPreset_ReturnsRules(t *testing.T) {
	cfg := newMCPConfig()
	if len(cfg.Policy.Rules) == 0 {
		t.Fatal("MCP preset produced no rules")
	}
}

func TestMCPPreset_IncludesStandardPreset(t *testing.T) {
	mcp := &config.Config{}
	mcp.Policy.Preset = "mcp"
	mcp.ApplyPolicyPreset()

	standard := &config.Config{}
	standard.Policy.Preset = "standard"
	standard.ApplyPolicyPreset()

	if len(mcp.Policy.Rules) <= len(standard.Policy.Rules) {
		t.Errorf("MCP preset should have more rules than standard, got %d vs %d",
			len(mcp.Policy.Rules), len(standard.Policy.Rules))
	}
}

func TestMCPPreset_HasRequiredRuleNames(t *testing.T) {
	cfg := newMCPConfig()
	ruleMap := make(map[string]bool)
	for _, r := range cfg.Policy.Rules {
		ruleMap[r.Name] = true
	}

	required := []string{
		"mcp01_tool_poison_hidden_instruction",
		"mcp01_tool_poison_exfiltration",
		"mcp02_excessive_permissions",
		"mcp03_injection_via_tool_args",
		"mcp03_injection_via_resource",
		"mcp04_tool_list_flood",
		"mcp04_tool_change_notification",
		"mcp05_server_error_flood",
		"mcp05_unexpected_method",
		"mcp06_resource_injection",
		"mcp07_initialize_without_auth",
		"mcp08_sensitive_tool_call",
		"mcp09_resource_enumeration",
		"mcp09_large_resource_read",
		"mcp10_unsigned_tool_call",
		"mcp_protocol_version_mismatch",
		"mcp_connection_storm",
		"mcp_session_flood",
		"mcp_block_exec_tools",
		"mcp_dangerous_tool_args",
	}

	for _, name := range required {
		if !ruleMap[name] {
			t.Errorf("MCP preset missing expected rule: %s", name)
		}
	}
}

func TestMCPPreset_RulesHaveValidSeverity(t *testing.T) {
	cfg := newMCPConfig()
	valid := map[string]bool{"info": true, "warning": true, "critical": true}
	for _, r := range cfg.Policy.Rules {
		if !valid[r.Severity] {
			t.Errorf("rule %q has invalid severity %q", r.Name, r.Severity)
		}
	}
}

func TestMCPPreset_RulesHaveValidActions(t *testing.T) {
	cfg := newMCPConfig()
	valid := map[string]bool{"flag": true, "block": true, "terminate": true, "": true}
	for _, r := range cfg.Policy.Rules {
		if !valid[r.Action] {
			t.Errorf("rule %q has invalid action %q", r.Name, r.Action)
		}
	}
}

func TestMCPPreset_PrependsToCustomRules(t *testing.T) {
	cfg := &config.Config{}
	cfg.Policy.Preset = "mcp"
	cfg.Policy.Rules = []config.PolicyRule{
		{Name: "my_custom_rule", Type: "bytes_out", Threshold: 1000, Severity: "warning"},
	}
	cfg.ApplyPolicyPreset()

	last := cfg.Policy.Rules[len(cfg.Policy.Rules)-1]
	if last.Name != "my_custom_rule" {
		t.Errorf("custom rule should be last after prepend, got %q", last.Name)
	}
}

func TestMCPPreset_BlockRulesPresent(t *testing.T) {
	cfg := newMCPConfig()
	hasBlock := false
	for _, r := range cfg.Policy.Rules {
		if r.Action == "block" {
			hasBlock = true
			break
		}
	}
	if !hasBlock {
		t.Error("MCP preset should contain at least one block action rule")
	}
}

func TestMCPPreset_TerminateRulesPresent(t *testing.T) {
	cfg := newMCPConfig()
	hasTerminate := false
	for _, r := range cfg.Policy.Rules {
		if r.Action == "terminate" {
			hasTerminate = true
			break
		}
	}
	if !hasTerminate {
		t.Error("MCP preset should contain at least one terminate action rule")
	}
}
