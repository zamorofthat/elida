package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsLoopback(t *testing.T) {
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
			if got := IsLoopback(tt.addr); got != tt.want {
				t.Errorf("IsLoopback(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

func TestValidateSecurityConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "localhost no auth — ok",
			cfg: Config{
				Control: ControlConfig{
					Listen:  "127.0.0.1:9090",
					Enabled: true,
				},
			},
			wantErr: false,
		},
		{
			name: "non-loopback with auth — ok",
			cfg: Config{
				Control: ControlConfig{
					Listen:  ":9090",
					Enabled: true,
					Auth: ControlAuthConfig{
						Enabled: true,
						APIKey:  "secret-key-12345",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "non-loopback no auth — error",
			cfg: Config{
				Control: ControlConfig{
					Listen:  ":9090",
					Enabled: true,
				},
			},
			wantErr: true,
		},
		{
			name: "non-loopback no auth but allow_insecure — ok",
			cfg: Config{
				Control: ControlConfig{
					Listen:  ":9090",
					Enabled: true,
					Auth: ControlAuthConfig{
						AllowInsecure: true,
					},
				},
			},
			wantErr: false,
		},
		{
			name: "control disabled — skip validation",
			cfg: Config{
				Control: ControlConfig{
					Listen:  ":9090",
					Enabled: false,
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSecurityConfig(&tt.cfg)
			if tt.wantErr && err == nil {
				t.Error("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestDefaultConfigSecurityDefaults(t *testing.T) {
	cfg := DefaultConfig()

	// Control binds to localhost
	if cfg.Control.Listen != "127.0.0.1:9090" {
		t.Errorf("control.listen = %q, want %q", cfg.Control.Listen, "127.0.0.1:9090")
	}

	// Redaction enabled by default
	if !cfg.Storage.Redaction.Enabled {
		t.Error("storage.redaction.enabled should be true by default")
	}

	// AllowInsecure defaults to false
	if cfg.Control.Auth.AllowInsecure {
		t.Error("control.auth.allow_insecure should be false by default")
	}

	// Security validation passes with defaults
	if err := ValidateSecurityConfig(cfg); err != nil {
		t.Errorf("default config should pass security validation: %v", err)
	}

	// redact_private_ips defaults to false (feedback #10: every measured
	// hit in production was loopback/RFC1918 noise, not PII).
	if cfg.Storage.Redaction.RedactPrivateIPs {
		t.Error("storage.redaction.redact_private_ips should be false by default")
	}
}

// redact_private_ips parses from YAML and is off by default when omitted.
func TestRedactPrivateIPsConfig(t *testing.T) {
	load := func(t *testing.T, yaml string) *Config {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "elida.yaml")
		if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	t.Run("explicit true", func(t *testing.T) {
		cfg := load(t, "listen: \"127.0.0.1:0\"\nbackend: \"http://127.0.0.1:1\"\n"+
			"storage:\n  redaction:\n    enabled: true\n    redact_private_ips: true\n")
		if !cfg.Storage.Redaction.RedactPrivateIPs {
			t.Error("expected redact_private_ips: true to parse as true")
		}
	})

	t.Run("omitted defaults false", func(t *testing.T) {
		cfg := load(t, "listen: \"127.0.0.1:0\"\nbackend: \"http://127.0.0.1:1\"\n"+
			"storage:\n  redaction:\n    enabled: true\n")
		if cfg.Storage.Redaction.RedactPrivateIPs {
			t.Error("expected redact_private_ips to default false when omitted")
		}
	})
}
