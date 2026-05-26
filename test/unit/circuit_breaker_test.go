package unit

import (
	"strings"
	"testing"

	"elida/internal/config"
)

func TestDefaultConfigHasCircuitBreaker(t *testing.T) {
	cfg := config.DefaultConfig()
	cb := cfg.Policy.CircuitBreaker
	if !cb.Enabled {
		t.Error("circuit breaker should be enabled by default")
	}
	if cb.TokensPerMinute != 50000 {
		t.Errorf("tokens_per_minute = %d, want 50000", cb.TokensPerMinute)
	}
	if cb.MaxTokensPerSession != 1000000 {
		t.Errorf("max_tokens_per_session = %d, want 1000000", cb.MaxTokensPerSession)
	}
	if cb.MaxToolCalls != 500 {
		t.Errorf("max_tool_calls = %d, want 500", cb.MaxToolCalls)
	}
	if cb.MaxToolFanout != 30 {
		t.Errorf("max_tool_fanout = %d, want 30", cb.MaxToolFanout)
	}
}

func TestCircuitBreakerGeneratesRules(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Policy.Preset = "minimal"
	cfg.Policy.CircuitBreaker.Enabled = true
	cfg.Policy.CircuitBreaker.TokensPerMinute = 50000
	cfg.Policy.CircuitBreaker.MaxToolCalls = 500
	cfg.ApplyPolicyPreset()

	found := map[string]bool{}
	for _, r := range cfg.Policy.Rules {
		if strings.HasPrefix(r.Name, "circuit_breaker_") {
			found[r.Name] = true
		}
	}
	if !found["circuit_breaker_tokens_per_min"] {
		t.Error("expected circuit_breaker_tokens_per_min rule")
	}
	if !found["circuit_breaker_tool_calls"] {
		t.Error("expected circuit_breaker_tool_calls rule")
	}
}

func TestCircuitBreakerDisabledNoRules(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Policy.Preset = "minimal"
	cfg.Policy.CircuitBreaker.Enabled = false
	cfg.ApplyPolicyPreset()

	for _, r := range cfg.Policy.Rules {
		if strings.HasPrefix(r.Name, "circuit_breaker_") {
			t.Errorf("unexpected circuit breaker rule when disabled: %s", r.Name)
		}
	}
}
