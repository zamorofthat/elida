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

// Feedback #4: precedence is header -> body_path (explicit config wins) ->
// user field -> "" (IP fallback).
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

	// body_path wins over the user field when both are present and configured.
	pBoth := identityProxy(true, "metadata.conversation_id")
	if got := pBoth.resolveSessionID(r, []byte(`{"user":"u1","metadata":{"conversation_id":"c9"}}`)); got != "user-c9" {
		t.Errorf("body_path should win over user field: got %q, want user-c9", got)
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
	if len(cfg.Session.DeriveFrom.BodyPaths) != 0 {
		t.Error("derive_from.body_paths should default to empty")
	}
}

// One config covers both A2A HTTP bindings: the same contextId resolves to
// the same session ID from a REST body and from a JSON-RPC body.
func TestResolveSessionIDBodyPaths(t *testing.T) {
	p := identityProxy(false, "")
	p.config.Session.DeriveFrom.BodyPaths = []string{"message.contextId", "params.message.contextId"}
	r := httptest.NewRequest("POST", "/message:send", bytes.NewReader(nil))

	rest := `{"message":{"contextId":"ctx-9","role":"ROLE_USER"}}`
	rpc := `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"contextId":"ctx-9"}}}`
	for name, body := range map[string]string{"rest": rest, "json-rpc": rpc} {
		if got := p.resolveSessionID(r, []byte(body)); got != "user-ctx-9" {
			t.Errorf("%s: got %q, want user-ctx-9", name, got)
		}
	}

	// body_path is tried ahead of body_paths.
	p.config.Session.DeriveFrom.BodyPath = "metadata.conversation_id"
	both := `{"metadata":{"conversation_id":"c1"},"message":{"contextId":"ctx-9"}}`
	if got := p.resolveSessionID(r, []byte(both)); got != "user-c1" {
		t.Errorf("body_path should be tried first: got %q, want user-c1", got)
	}

	// The session header still wins over every body path.
	r.Header.Set(p.config.Session.Header, "explicit-id")
	if got := p.resolveSessionID(r, []byte(rest)); got != "explicit-id" {
		t.Errorf("header should win, got %q", got)
	}
}
