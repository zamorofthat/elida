package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"elida/internal/config"
	"elida/internal/session"
)

// keyedFixture is a proxy whose callers authenticate with per-client keys.
func keyedFixture(t *testing.T, hits *int64, configure func(*config.Config)) (*Proxy, *session.Manager, func()) {
	t.Helper()
	backend := e2eBackend(hits)
	p, manager := newE2EProxy(t, backend.URL, func(cfg *config.Config) {
		cfg.Proxy.Auth.Enabled = true
		cfg.Proxy.Auth.APIKey = "shared-key"
		cfg.Proxy.Auth.Keys = []config.ProxyAuthKey{
			{Name: "team-a", Key: "key-a"},
			{Name: "team-b", Key: "key-b"},
		}
		if configure != nil {
			configure(cfg)
		}
	})
	return p, manager, backend.Close
}

// send posts one chat request. key is sent as X-Elida-API-Key unless empty;
// sessionHeader, when set, is the caller's session ID.
func send(p *Proxy, key, sessionHeader string) int {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m1"}`))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("X-Elida-API-Key", key)
	}
	if sessionHeader != "" {
		req.Header.Set("X-Session-ID", sessionHeader)
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec.Code
}

// Two clients that send the same session ID get two sessions, and killing one
// leaves the other running: the collision the shared key could not prevent.
func TestKeyedClientsNeverShareASession(t *testing.T) {
	var hits int64
	p, manager, closeBackend := keyedFixture(t, &hits, nil)
	defer closeBackend()

	if code := send(p, "key-a", "conv-1"); code != http.StatusOK {
		t.Fatalf("team-a: status %d", code)
	}
	if code := send(p, "key-b", "conv-1"); code != http.StatusOK {
		t.Fatalf("team-b: status %d", code)
	}
	for _, id := range []string{"team-a:conv-1", "team-b:conv-1"} {
		if _, ok := manager.Get(id); !ok {
			t.Errorf("session %q does not exist", id)
		}
	}
	if _, ok := manager.Get("conv-1"); ok {
		t.Error("a keyed client's session must not exist under the plain, shared ID")
	}

	if !manager.Kill("team-a:conv-1") {
		t.Fatal("kill team-a:conv-1 failed")
	}
	if code := send(p, "key-a", "conv-1"); code != http.StatusForbidden {
		t.Errorf("team-a after its kill: status %d, want 403", code)
	}
	if code := send(p, "key-b", "conv-1"); code != http.StatusOK {
		t.Errorf("team-b after team-a's kill: status %d, want 200", code)
	}
}

// The shared key carries no client: its IDs are what they were before.
func TestSharedKeyKeepsThePlainSessionID(t *testing.T) {
	var hits int64
	p, manager, closeBackend := keyedFixture(t, &hits, nil)
	defer closeBackend()

	if code := send(p, "shared-key", "conv-9"); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if _, ok := manager.Get("conv-9"); !ok {
		t.Error("a shared-key request must keep its plain session ID")
	}
}

// A request from a trusted network skips the key check and has no client.
func TestTrustedNetworkClientHasNoClientName(t *testing.T) {
	var hits int64
	p, manager, closeBackend := keyedFixture(t, &hits, func(cfg *config.Config) {
		cfg.Proxy.Auth.TrustedNetworks = []string{"192.0.2.0/24"} // httptest's peer
	})
	defer closeBackend()

	if code := send(p, "", "conv-t"); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if _, ok := manager.Get("conv-t"); !ok {
		t.Error("a trusted-network request must keep its plain session ID")
	}
}

// An unknown key is refused exactly as a wrong shared key is, and the backend
// is never reached.
func TestUnknownKeyIsRefused(t *testing.T) {
	var hits int64
	p, _, closeBackend := keyedFixture(t, &hits, nil)
	defer closeBackend()

	if code := send(p, "not-a-key", "conv-1"); code != http.StatusUnauthorized {
		t.Errorf("unknown key: status %d, want 401", code)
	}
	if code := send(p, "", "conv-1"); code != http.StatusUnauthorized {
		t.Errorf("no key: status %d, want 401", code)
	}
	if h := atomic.LoadInt64(&hits); h != 0 {
		t.Errorf("the backend was reached %d times by unauthorized requests", h)
	}
}

// A per-client key also works as a bearer token, like the shared key.
func TestKeyedClientMayUseABearerToken(t *testing.T) {
	var hits int64
	p, manager, closeBackend := keyedFixture(t, &hits, nil)
	defer closeBackend()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer key-b")
	req.Header.Set("X-Session-ID", "conv-bearer")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := manager.Get("team-b:conv-bearer"); !ok {
		t.Error("a bearer-token request from team-b must be in team-b's namespace")
	}
}

// With no ID of its own, a keyed client still gets a session in its namespace,
// and the client's name is recorded on every session it creates.
func TestKeyedClientWithoutAnIDAndTheRecordedClient(t *testing.T) {
	var hits int64
	p, manager, closeBackend := keyedFixture(t, &hits, nil)
	defer closeBackend()

	if code := send(p, "key-a", ""); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	var found *session.Session
	for _, s := range manager.ListAll() {
		if strings.HasPrefix(s.ID, "team-a:") {
			found = s
		}
	}
	if found == nil {
		t.Fatalf("no session in team-a's namespace; sessions: %v", manager.ListAll())
	}
	if got := found.Snapshot().Metadata["client"]; got != "team-a" {
		t.Errorf("recorded client = %q, want team-a", got)
	}
	for _, s := range manager.ListAll() {
		if strings.HasPrefix(s.ID, "client-") {
			t.Errorf("a keyed client fell into the shared IP-hash session %q", s.ID)
		}
	}
}
