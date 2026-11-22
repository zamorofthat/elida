package config

import (
	"regexp"
	"strings"
	"testing"
)

// matchesRule mirrors the policy engine, which compiles every content_match
// pattern case-insensitively.
func matchesRule(t *testing.T, r *PolicyRule, text string) bool {
	t.Helper()
	for _, p := range r.Patterns {
		re, err := regexp.Compile("(?i)" + p)
		if err != nil {
			t.Fatalf("pattern %q does not compile: %v", p, err)
		}
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// A real credential in an outgoing prompt is sent to the provider and every
// hop in front of it. The standard and coding-agent presets must at least
// flag it, and must never block on it by default.
func TestSecretInRequestRule(t *testing.T) {
	secrets := map[string]string{
		"aws access key":     "AKIA" + strings.Repeat("A", 16),
		"aws temp key":       "ASIA" + strings.Repeat("B", 16),
		"github token":       "ghp_" + strings.Repeat("a1", 18),
		"github fine-grain":  "github_pat_" + strings.Repeat("a1", 22),
		"slack token":        "xoxb-" + strings.Repeat("1", 12) + "-" + strings.Repeat("a", 24),
		"anthropic key":      "sk-ant-api03-" + strings.Repeat("Ab1_", 10),
		"openai key":         "sk-" + strings.Repeat("a1", 24),
		"openai project key": "sk-proj-" + strings.Repeat("Ab1_-", 12),
		"private key":        "-----BEGIN " + "RSA PRIVATE KEY-----",
	}
	benign := []string{
		"the ghp_ prefix marks a GitHub personal access token",
		"AKIA is the prefix of an AWS access key id",
		"-----BEGIN CERTIFICATE-----",
		"ask-ant-eater is a hyphenated word",
		"deploy to asia-southeast1 next week",
		"explain how sk- prefixed keys are used",
	}

	for _, preset := range []string{"standard", "strict", "coding-agent"} {
		cfg := &Config{}
		cfg.Policy.Preset = preset
		cfg.ApplyPolicyPreset()

		r := findRule(cfg.Policy.Rules, "secret_in_request")
		if r == nil {
			t.Fatalf("%s preset has no secret_in_request rule", preset)
		}
		if r.Type != "content_match" || r.Target != "request" {
			t.Errorf("%s: rule is %s on %s, want content_match on request", preset, r.Type, r.Target)
		}
		if !r.RedactMatch {
			t.Errorf("%s: the rule must not record the credential it matched", preset)
		}
		if r.Action != "flag" {
			t.Errorf("%s: action = %q, want flag (a secret match must not block by default)", preset, r.Action)
		}
		if wantObserve := preset == "coding-agent"; r.Observe != wantObserve {
			t.Errorf("%s: observe = %v, want %v", preset, r.Observe, wantObserve)
		}
		for name, secret := range secrets {
			if !matchesRule(t, r, "here is my credential "+secret+" please deploy") {
				t.Errorf("%s: %s not detected", preset, name)
			}
		}
		for _, text := range benign {
			if matchesRule(t, r, text) {
				t.Errorf("%s: benign text flagged: %q", preset, text)
			}
		}
	}
}

// Presets that do not look at content stay without the rule.
func TestSecretInRequestRuleAbsentFromMinimal(t *testing.T) {
	cfg := &Config{}
	cfg.Policy.Preset = "minimal"
	cfg.ApplyPolicyPreset()
	if findRule(cfg.Policy.Rules, "secret_in_request") != nil {
		t.Error("minimal preset must not gain content rules")
	}
}
