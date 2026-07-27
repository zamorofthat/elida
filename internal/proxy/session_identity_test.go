package proxy

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"elida/internal/config"
)

func identityProxy(openaiUser bool, bodyPath string) *Proxy {
	cfg := config.DefaultConfig()
	cfg.Session.DeriveFrom.OpenAIUser = openaiUser
	cfg.Session.DeriveFrom.BodyPath = bodyPath
	return &Proxy{config: cfg}
}

// Feedback #4: precedence is header -> user field -> body path config -> "" (IP fallback).
func TestResolveSessionIDPrecedence(t *testing.T) {
	p := identityProxy(true, "")

	// Header wins even when a user field is present.
	r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(nil))
	r.Header.Set(p.config.Session.Header, "explicit-id")
	if got := p.resolveSessionID(r, []byte(`{"user":"conv-1"}`)); got != "explicit-id" {
		t.Errorf("header should win, got %q", got)
	}

	// user field used when no header.
	r = httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(nil))
	if got := p.resolveSessionID(r, []byte(`{"user":"conv-1"}`)); got != "user-conv-1" {
		t.Errorf("user field: got %q, want user-conv-1", got)
	}

	// Nothing derivable -> "" (caller falls back to IP-hash session).
	if got := p.resolveSessionID(r, []byte(`{"model":"gemma"}`)); got != "" {
		t.Errorf("no source: got %q, want empty", got)
	}
}

// The same user value must yield the same session ID regardless of backend —
// this is what keeps one conversation in one session across failover.
func TestDerivedIDIsBackendIndependent(t *testing.T) {
	p := identityProxy(true, "")
	r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(nil))

	local := p.resolveSessionID(r, []byte(`{"model":"gemma","user":"conv-7"}`))
	cloud := p.resolveSessionID(r, []byte(`{"model":"mistral-small-latest","user":"conv-7"}`))
	if local == "" || local != cloud {
		t.Errorf("same conversation split: %q vs %q", local, cloud)
	}
}

// derive_from defaults: openai_user on, body_path empty.
func TestDeriveFromDefaults(t *testing.T) {
	cfg := config.DefaultConfig()
	if !cfg.Session.DeriveFrom.OpenAIUser {
		t.Error("derive_from.openai_user should default to true")
	}
	if cfg.Session.DeriveFrom.BodyPath != "" {
		t.Error("derive_from.body_path should default to empty")
	}
}
