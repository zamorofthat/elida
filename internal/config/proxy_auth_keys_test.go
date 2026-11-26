package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func authKeyErrors(c *Config) []string {
	var fields []string
	for _, e := range c.Validate().Errors {
		if strings.HasPrefix(e.Field, "proxy.auth") {
			fields = append(fields, e.Field+": "+e.Message)
		}
	}
	return fields
}

func keyedConfig(keys ...ProxyAuthKey) *Config {
	c := defaults()
	c.Proxy.Auth.Enabled = true
	c.Proxy.Auth.Keys = keys
	return c
}

// Per-client keys stand alone: auth is enabled with keys and no shared key.
func TestProxyAuthKeysAloneAreValid(t *testing.T) {
	c := keyedConfig(
		ProxyAuthKey{Name: "team-a", Key: "key-a"},
		ProxyAuthKey{Name: "team.b_2", Key: "key-b"},
	)
	if got := authKeyErrors(c); len(got) != 0 {
		t.Errorf("valid keyed config rejected: %v", got)
	}
}

func TestProxyAuthKeysValidation(t *testing.T) {
	cases := map[string]*Config{
		"empty name":     keyedConfig(ProxyAuthKey{Name: "", Key: "k"}),
		"empty key":      keyedConfig(ProxyAuthKey{Name: "a", Key: ""}),
		"colon in name":  keyedConfig(ProxyAuthKey{Name: "a:b", Key: "k"}),
		"space in name":  keyedConfig(ProxyAuthKey{Name: "a b", Key: "k"}),
		"name too long":  keyedConfig(ProxyAuthKey{Name: strings.Repeat("a", 65), Key: "k"}),
		"duplicate name": keyedConfig(ProxyAuthKey{Name: "a", Key: "k1"}, ProxyAuthKey{Name: "a", Key: "k2"}),
		"duplicate key":  keyedConfig(ProxyAuthKey{Name: "a", Key: "same"}, ProxyAuthKey{Name: "b", Key: "same"}),
		"equals shared": func() *Config {
			c := keyedConfig(ProxyAuthKey{Name: "a", Key: "shared"})
			c.Proxy.Auth.APIKey = "shared"
			return c
		}(),
		"keys but no auth": func() *Config {
			c := keyedConfig(ProxyAuthKey{Name: "a", Key: "k"})
			c.Proxy.Auth.Enabled = false
			return c
		}(),
	}
	for name, c := range cases {
		if got := authKeyErrors(c); len(got) == 0 {
			t.Errorf("%s: want a proxy.auth validation error, got none", name)
		}
	}
}

// Enabled auth still needs some key: the shared one or at least one per-client key.
func TestProxyAuthEnabledStillNeedsAKey(t *testing.T) {
	c := defaults()
	c.Proxy.Auth.Enabled = true
	if got := authKeyErrors(c); len(got) == 0 {
		t.Error("auth enabled with no shared key and no keys must be an error")
	}
}

// Keys come from the environment the way the shared key does.
func TestProxyAuthKeysExpandEnvReferences(t *testing.T) {
	t.Setenv("TEAM_A_KEY", "secret-from-env")
	dir := t.TempDir()
	path := filepath.Join(dir, "elida.yaml")
	yaml := "listen: \":8080\"\nbackend: http://localhost:11434\nproxy:\n  auth:\n    enabled: true\n    keys:\n      - name: team-a\n        key: ${TEAM_A_KEY}\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Proxy.Auth.Keys) != 1 || cfg.Proxy.Auth.Keys[0].Key != "secret-from-env" {
		t.Errorf("keys = %+v, want team-a with the expanded key", cfg.Proxy.Auth.Keys)
	}
}
