package unit

import (
	"testing"

	"elida/internal/config"
)

func TestIsLoopbackAddresses(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:9090", true},
		{"localhost:9090", true},
		{"[::1]:9090", true},
		{":9090", false},
		{"0.0.0.0:9090", false},
		{"10.0.1.5:9090", false},
		{"192.168.1.1:9090", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			if got := config.IsLoopback(tt.addr); got != tt.want {
				t.Errorf("IsLoopback(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

func TestValidateSecurityConfigBlocking(t *testing.T) {
	// Non-loopback without auth should fail
	cfg := config.DefaultConfig()
	cfg.Control.Listen = ":9090"
	cfg.Control.Auth.Enabled = false
	cfg.Control.Auth.APIKey = ""
	cfg.Control.Auth.AllowInsecure = false

	err := config.ValidateSecurityConfig(cfg)
	if err == nil {
		t.Error("expected error for non-loopback without auth")
	}
}

func TestValidateSecurityConfigAllowInsecure(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Control.Listen = ":9090"
	cfg.Control.Auth.AllowInsecure = true

	err := config.ValidateSecurityConfig(cfg)
	if err != nil {
		t.Errorf("allow_insecure should bypass auth requirement: %v", err)
	}
}

func TestValidateSecurityConfigLocalhostNoAuth(t *testing.T) {
	cfg := config.DefaultConfig()
	// Default is 127.0.0.1:9090 — should pass without auth
	err := config.ValidateSecurityConfig(cfg)
	if err != nil {
		t.Errorf("localhost should not require auth: %v", err)
	}
}

func TestValidateSecurityConfigNonLoopbackWithAuth(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Control.Listen = ":9090"
	cfg.Control.Auth.Enabled = true
	cfg.Control.Auth.APIKey = "test-key-12345"

	err := config.ValidateSecurityConfig(cfg)
	if err != nil {
		t.Errorf("non-loopback with auth should pass: %v", err)
	}
}

func TestDefaultConfigSecureDefaults(t *testing.T) {
	cfg := config.DefaultConfig()

	if cfg.Control.Listen != "127.0.0.1:9090" {
		t.Errorf("control.listen = %q, want 127.0.0.1:9090", cfg.Control.Listen)
	}
	if !cfg.Storage.Redaction.Enabled {
		t.Error("redaction should be enabled by default")
	}
	if cfg.Control.Auth.AllowInsecure {
		t.Error("allow_insecure should be false by default")
	}
}
