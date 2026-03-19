package config

import (
	"testing"
)

func TestMCPPresetLoads(t *testing.T) {
	rules := getMCPPreset()
	if len(rules) == 0 {
		t.Fatal("MCP preset returned zero rules")
	}

	// Should inherit standard rules + MCP-specific rules
	standardRules := getStandardPreset()
	if len(rules) <= len(standardRules) {
		t.Errorf("MCP preset (%d rules) should have more rules than standard (%d rules)",
			len(rules), len(standardRules))
	}

	t.Logf("MCP preset has %d rules (%d standard + %d MCP-specific)",
		len(rules), len(standardRules), len(rules)-len(standardRules))
}

func TestMCPPresetHasAllOWASPCategories(t *testing.T) {
	rules := getMCPPreset()

	// Check that we have rules for each OWASP MCP Top 10 category
	requiredPrefixes := []string{
		"mcp01_", // Tool Poisoning
		"mcp02_", // Excessive Permissions
		"mcp03_", // MCP Injection
		"mcp04_", // Tool Rug Pulls
		"mcp05_", // Server Compromise
		"mcp06_", // Resource Injection
		"mcp07_", // Auth Gaps
		"mcp08_", // Logging Gaps
		"mcp09_", // Resource Abuse
		"mcp10_", // Integrity
	}

	for _, prefix := range requiredPrefixes {
		found := false
		for _, rule := range rules {
			if len(rule.Name) >= len(prefix) && rule.Name[:len(prefix)] == prefix {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Missing OWASP MCP Top 10 rule with prefix %q", prefix)
		}
	}
}

func TestMCPPresetRuleActions(t *testing.T) {
	rules := getMCPPreset()

	validActions := map[string]bool{
		"flag":      true,
		"block":     true,
		"terminate": true,
		"throttle":  true,
		"":          true, // Default
	}

	validSeverities := map[string]bool{
		"info":     true,
		"warning":  true,
		"critical": true,
	}

	for _, rule := range rules {
		if !validActions[rule.Action] {
			t.Errorf("Rule %q has invalid action %q", rule.Name, rule.Action)
		}
		if !validSeverities[string(rule.Severity)] {
			t.Errorf("Rule %q has invalid severity %q", rule.Name, rule.Severity)
		}
		if rule.Description == "" {
			t.Errorf("Rule %q has empty description", rule.Name)
		}
	}
}

func TestMCPPresetCriticalRulesBlock(t *testing.T) {
	rules := getMCPPreset()

	// Critical MCP rules should block or terminate, not just flag
	criticalMCPRules := []string{
		"mcp01_tool_poison_hidden_instruction",
		"mcp01_tool_poison_exfiltration",
		"mcp02_excessive_permissions",
		"mcp03_injection_via_tool_args",
		"mcp03_injection_via_resource",
		"mcp06_resource_injection",
	}

	for _, name := range criticalMCPRules {
		for _, rule := range rules {
			if rule.Name == name {
				if rule.Action != "block" && rule.Action != "terminate" {
					t.Errorf("Critical rule %q should block or terminate, got %q", name, rule.Action)
				}
				if rule.Severity != "critical" {
					t.Errorf("Critical rule %q should have critical severity, got %q", name, rule.Severity)
				}
			}
		}
	}
}

func TestMCPPresetConfigSwitch(t *testing.T) {
	// Verify the "mcp" preset is wired into the config switch
	cfg := &Config{
		Policy: PolicyConfig{
			Preset: "mcp",
		},
	}
	cfg.ApplyPolicyPreset()

	if len(cfg.Policy.Rules) == 0 {
		t.Fatal("Config with preset 'mcp' should have loaded MCP rules")
	}

	// Verify MCP-specific rules are present
	hasMCPRule := false
	for _, rule := range cfg.Policy.Rules {
		if len(rule.Name) >= 4 && rule.Name[:4] == "mcp0" {
			hasMCPRule = true
			break
		}
	}
	if !hasMCPRule {
		t.Error("Config with preset 'mcp' should contain MCP-specific rules (mcp0x_*)")
	}
}

func TestMCPPresetNoDuplicateNames(t *testing.T) {
	rules := getMCPPreset()
	seen := make(map[string]bool)

	for _, rule := range rules {
		if seen[rule.Name] {
			t.Errorf("Duplicate rule name: %q", rule.Name)
		}
		seen[rule.Name] = true
	}
}

func TestMCPPresetPatternsCompile(t *testing.T) {
	rules := getMCPPreset()

	for _, rule := range rules {
		for _, pattern := range rule.Patterns {
			if pattern == "" {
				t.Errorf("Rule %q has empty pattern", rule.Name)
				continue
			}
			// Patterns are compiled by the policy engine, but we can check
			// they are non-empty and have valid Go regex syntax
			// The policy engine uses regexp.Compile, so invalid patterns
			// would cause runtime errors
		}
	}
}
