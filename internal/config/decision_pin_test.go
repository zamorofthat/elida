package config

import (
	"strings"
	"testing"
)

func pinErrors(c *Config) []string {
	var fields []string
	for _, e := range c.Validate().Errors {
		if e.Field == "decision.model_checksum" {
			fields = append(fields, e.Message)
		}
	}
	return fields
}

func TestDecisionModelChecksumDefaultsToUnpinned(t *testing.T) {
	if got := defaults().Decision.ModelChecksum; got != "" {
		t.Errorf("default decision.model_checksum = %q, want empty (no pin)", got)
	}
}

func TestValidateDecisionModelChecksum(t *testing.T) {
	good := strings.Repeat("ab", 32)
	for name, pin := range map[string]string{
		"unset":     "",
		"lowercase": good,
		"uppercase": strings.ToUpper(good),
		"padded":    " " + good + " ",
	} {
		c := defaults()
		c.Decision.Enabled = true
		c.Decision.ModelChecksum = pin
		if got := pinErrors(c); len(got) != 0 {
			t.Errorf("%s: valid pin rejected: %v", name, got)
		}
	}
	for name, pin := range map[string]string{
		"too short":   "abc123",
		"too long":    good + "0",
		"not hex":     strings.Repeat("zz", 32),
		"with prefix": "sha256:" + good,
	} {
		c := defaults()
		c.Decision.Enabled = true
		c.Decision.ModelChecksum = pin
		if got := pinErrors(c); len(got) != 1 {
			t.Errorf("%s: want one error for %q, got %v", name, pin, got)
		}
	}
}

func TestDecisionModelChecksumEnvOverride(t *testing.T) {
	pin := strings.Repeat("cd", 32)
	t.Setenv("ELIDA_DECISION_MODEL_CHECKSUM", pin)
	c := defaults()
	c.applyEnvOverrides()
	if c.Decision.ModelChecksum != pin {
		t.Errorf("env override: got %q, want %q", c.Decision.ModelChecksum, pin)
	}
}

// The pin applies to the embedded model. With another provider there is no
// manifest to check, and a pin that silently does nothing is a false assurance.
func TestValidateWarnsWhenModelChecksumCannotApply(t *testing.T) {
	c := defaults()
	c.Decision.Enabled = true
	c.Decision.Provider = "systemone"
	c.Decision.Endpoint = "https://decide.internal/v1/score"
	c.Decision.ModelChecksum = strings.Repeat("ab", 32)
	found := false
	for _, w := range c.Validate().Warnings {
		if w.Field == "decision.model_checksum" {
			found = true
		}
	}
	if !found {
		t.Error("model_checksum with a non-embedded provider: want a decision.model_checksum warning")
	}
}
