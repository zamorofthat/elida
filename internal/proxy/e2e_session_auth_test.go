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

// minimalChatCompletionResponse is a minimal but valid OpenAI-style
// chat-completions JSON response, good enough for the proxy's response-side
// parsing (token usage, tool calls) to no-op cleanly.
const minimalChatCompletionResponse = `{"id":"chatcmpl-e2e","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

// newE2EProxy builds a real Proxy via New(...) against a live httptest
// backend, following the pattern established in test/unit/proxy_test.go and
// test/unit/proxy_auth_test.go. configure may be nil.
func newE2EProxy(t *testing.T, backendURL string, configure func(*config.Config)) (*Proxy, *session.Manager) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Backend = backendURL
	if configure != nil {
		configure(cfg)
	}

	store := session.NewMemoryStore()
	manager := session.NewManager(store, cfg.Session.Timeout)
	p, err := New(cfg, store, manager)
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}
	return p, manager
}

// e2eBackend returns an httptest server that answers with a minimal valid
// chat-completions response and counts how many requests it received.
func e2eBackend(hits *int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(minimalChatCompletionResponse))
	}))
}

// TestE2EAuthEnforcement exercises the real ServeHTTP path for
// proxy.auth.enabled + trusted_networks: untrusted peers must present a
// valid key, trusted peers bypass the check, and X-Forwarded-For /
// X-Real-IP spoofing from an untrusted direct peer must never grant a
// bypass (feedback #4b). Also asserts the backend is never reached when
// the request is rejected.
func TestE2EAuthEnforcement(t *testing.T) {
	var hits int64
	backend := e2eBackend(&hits)
	defer backend.Close()

	p, _ := newE2EProxy(t, backend.URL, func(cfg *config.Config) {
		cfg.Proxy.Auth.Enabled = true
		cfg.Proxy.Auth.APIKey = "sekret"
		cfg.Proxy.Auth.TrustedNetworks = []string{"10.9.0.0/16"}
	})

	tests := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		wantStatus int
		wantHit    bool
	}{
		{
			name:       "untrusted peer, no key",
			remoteAddr: "192.0.2.10:1000",
			wantStatus: http.StatusUnauthorized,
			wantHit:    false,
		},
		{
			name:       "untrusted peer, bearer key",
			remoteAddr: "192.0.2.10:1000",
			headers:    map[string]string{"Authorization": "Bearer sekret"},
			wantStatus: http.StatusOK,
			wantHit:    true,
		},
		{
			name:       "trusted peer, no key",
			remoteAddr: "10.9.1.5:2000",
			wantStatus: http.StatusOK,
			wantHit:    true,
		},
		{
			name:       "untrusted peer, spoofed X-Forwarded-For/X-Real-IP",
			remoteAddr: "192.0.2.10:1000",
			headers: map[string]string{
				"X-Forwarded-For": "10.9.1.5",
				"X-Real-IP":       "10.9.1.5",
			},
			wantStatus: http.StatusUnauthorized,
			wantHit:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			atomic.StoreInt64(&hits, 0)

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m1"}`))
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = tt.remoteAddr
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body=%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if gotHit := atomic.LoadInt64(&hits) > 0; gotHit != tt.wantHit {
				t.Errorf("backend hit = %v, want %v", gotHit, tt.wantHit)
			}
		})
	}
}

// TestE2EDerivedSessionUnification exercises the real ServeHTTP path for
// session identity derivation: two requests carrying the same OpenAI
// "user" value (with different models, so backend-independence is also
// covered) must land in the SAME session, while a request without a user
// field must fall back to a separate client-IP-derived session rather than
// merging into either.
func TestE2EDerivedSessionUnification(t *testing.T) {
	var hits int64
	backend := e2eBackend(&hits)
	defer backend.Close()

	p, manager := newE2EProxy(t, backend.URL, nil) // auth off, GenerateIfMissing + openai_user derive_from are defaults

	post := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := post(`{"model":"m1","user":"conv-e2e"}`); code != http.StatusOK {
		t.Fatalf("request 1 (model m1): got status %d", code)
	}
	if code := post(`{"model":"m2","user":"conv-e2e"}`); code != http.StatusOK {
		t.Fatalf("request 2 (model m2, same user): got status %d", code)
	}
	if code := post(`{"model":"m1"}`); code != http.StatusOK {
		t.Fatalf("request 3 (no user): got status %d", code)
	}

	sess, ok := manager.Get("user-conv-e2e")
	if !ok {
		t.Fatal("expected unified session \"user-conv-e2e\" to exist")
	}
	if sess.RequestCount != 2 {
		t.Errorf("expected 2 requests recorded on the unified session, got %d", sess.RequestCount)
	}

	all := manager.ListAll()
	if len(all) != 2 {
		ids := make([]string, len(all))
		for i, s := range all {
			ids[i] = s.ID
		}
		t.Fatalf("expected exactly 2 sessions total, got %d: %v", len(all), ids)
	}

	var foundFallback bool
	for _, s := range all {
		if s.ID == "user-conv-e2e" {
			continue
		}
		foundFallback = true
		if !strings.HasPrefix(s.ID, "client-") {
			t.Errorf("expected the no-user fallback session ID to start with \"client-\", got %q", s.ID)
		}
	}
	if !foundFallback {
		t.Error("expected a separate client-* fallback session for the request with no user field")
	}
}

// TestE2EKillSwitchOnDerivedSession pins the branch's headline claim: the
// kill-switch operates per-conversation. Killing the session derived from
// an OpenAI "user" value must reject subsequent requests carrying that same
// user value with 403 session_terminated, exercised through the real
// ServeHTTP path (not the manager directly).
func TestE2EKillSwitchOnDerivedSession(t *testing.T) {
	var hits int64
	backend := e2eBackend(&hits)
	defer backend.Close()

	p, manager := newE2EProxy(t, backend.URL, nil)

	// First request establishes the derived session.
	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m1","user":"conv-kill"}`))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	p.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("initial request: got status %d, body=%s", rec1.Code, rec1.Body.String())
	}

	if _, ok := manager.Get("user-conv-kill"); !ok {
		t.Fatal("expected derived session \"user-conv-kill\" to exist before kill")
	}

	// Kill through the manager's kill API, as the control API's kill-switch does.
	if !manager.Kill("user-conv-kill") {
		t.Fatal("manager.Kill(\"user-conv-kill\") returned false")
	}

	// Second request, same user value -> must be rejected, not proxied.
	atomic.StoreInt64(&hits, 0)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m1","user":"conv-kill"}`))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	p.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusForbidden {
		t.Errorf("post-kill request: status = %d, want %d (body=%s)", rec2.Code, http.StatusForbidden, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "session_terminated") {
		t.Errorf("post-kill request: expected session_terminated in body, got %q", rec2.Body.String())
	}
	if h := atomic.LoadInt64(&hits); h != 0 {
		t.Errorf("post-kill request reached the backend (%d hits), it should have been rejected before proxying", h)
	}
}
