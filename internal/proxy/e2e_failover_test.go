package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/router"
	"elida/internal/session"
)

// newE2EFailoverRouter builds a *router.Router from the given backend configs,
// then overrides each backend's URL/Transport to point at the corresponding
// httptest server. This mirrors the pattern established in
// internal/proxy/failover_model_test.go's TestFailover_SkipsUnmappableBackend_ThenSucceeds
// and test/unit/failover_test.go's TestProxy_FailoverSuccess_SecondBackendWorks.
func newE2EFailoverRouter(t *testing.T, backends map[string]config.BackendConfig, servers map[string]*httptest.Server) *router.Router {
	t.Helper()
	rt, err := router.NewRouter(backends, config.RoutingConfig{Methods: []string{"model", "default"}})
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}
	for name, srv := range servers {
		b, ok := rt.GetBackend(name)
		if !ok {
			t.Fatalf("backend %q not found in router", name)
		}
		u, err := url.Parse(srv.URL)
		if err != nil {
			t.Fatalf("failed to parse server URL: %v", err)
		}
		b.URL = u
		b.Transport = srv.Client().Transport.(*http.Transport) //nolint:errcheck
	}
	return rt
}

// TestE2EFailover_RewritesModelOnSuccess exercises the full ServeHTTP path
// for the success side of feedback #8: a "gemma" request is routed to
// "primary" via its models: [gemma] glob, primary always fails (500), and
// failover rewrites the model to the fallback backend's configured
// backends.<name>.model before forwarding. Asserts the client sees the
// fallback's 200 response, the fallback backend actually received the
// rewritten model id, and the session's backends_used records both hops.
func TestE2EFailover_RewritesModelOnSuccess(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer primary.Close()

	var fallbackBody []byte
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		fallbackBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("fallback: failed to read request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi from fallback"}}]}`))
	}))
	defer fallback.Close()

	backends := map[string]config.BackendConfig{
		"primary":  {URL: primary.URL, Type: "openai", Default: true, Models: []string{"gemma"}},
		"fallback": {URL: fallback.URL, Type: "openai", Model: "fallback-model"},
	}
	rt := newE2EFailoverRouter(t, backends, map[string]*httptest.Server{"primary": primary, "fallback": fallback})

	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	cfg := &config.Config{
		Backend: primary.URL,
		Session: config.SessionConfig{
			Timeout:           5 * time.Minute,
			Header:            "X-Session-ID",
			GenerateIfMissing: true,
		},
	}

	p, err := New(cfg, store, manager, WithRouter(rt))
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	fc := NewFailoverController(FailoverConfig{
		Enabled:       true,
		MaxRetries:    2,
		FallbackOrder: []string{"primary", "fallback"},
		RetryDelay:    0,
	})
	fc.RegisterBackend("primary", primary.URL, "openai", 0)
	fc.RegisterBackend("fallback", fallback.URL, "openai", 1)
	p.SetFailoverController(fc)

	const sessionID = "e2e-failover-success"
	reqBody := `{"model":"gemma","messages":[{"role":"user","content":"hello-e2e-conversation"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Session-ID", sessionID)

	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 from fallback, got %d (body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "hi from fallback") {
		t.Errorf("expected response body from fallback, got: %s", w.Body.String())
	}

	if fallbackBody == nil {
		t.Fatal("fallback backend never received a request")
	}
	if !strings.Contains(string(fallbackBody), `"model":"fallback-model"`) {
		t.Errorf("expected fallback to receive rewritten model \"fallback-model\", got body: %s", fallbackBody)
	}
	// The session has no recorded history (RecordMessage/SetSystemPrompt have
	// no production callers), so rehydration must fall back to preserving the
	// original request's conversation rather than replaying an empty one -
	// otherwise the user's prompt is silently dropped on failover.
	if !strings.Contains(string(fallbackBody), "hello-e2e-conversation") {
		t.Errorf("expected fallback to receive the original conversation, got body: %s", fallbackBody)
	}

	sess, ok := manager.Get(sessionID)
	if !ok {
		t.Fatal("expected session to exist")
	}
	used := sess.GetBackendsUsed()
	if used["primary"] < 1 {
		t.Errorf("expected backends_used to record primary, got: %v", used)
	}
	if used["fallback"] < 1 {
		t.Errorf("expected backends_used to record fallback, got: %v", used)
	}
}

// TestE2EFailover_SkipsUnmappableBackend_AllUnavailable covers the brief's
// second case: the only configured fallback has no backends.<name>.model and
// its models globs (["zzz-*"]) don't match "gemma" and can't be validated
// against the remap table's result either, so ResolveFailoverModel rejects
// it and it must be skipped (never receive a request) per feedback #8.
//
// With only two registered failover backends, skipping the sole candidate
// makes SelectFallback's subsequent lookup return "no available fallback
// backends" before attemptFailoverWithDepth's depth>=maxFailoverRetries(3)
// safety valve is ever reached (that valve only fires with >=4 registered
// backends, as TestProxy_FailoverDepthLimit_AllBackendsFail in
// test/unit/failover_test.go demonstrates). Rather than silently forwarding
// primary's stale original response in that case (indistinguishable from
// failover being disabled), handleStandard now recognizes "failover was
// attempted but never retried" and returns a clear 502 - see
// writeFailoverExhaustedResponse in proxy.go and task-b3-5-report.md for
// the full writeup of this fix.
func TestE2EFailover_SkipsUnmappableBackend_AllUnavailable(t *testing.T) {
	primaryHit := false
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryHit = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer primary.Close()

	fallbackHit := false
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer fallback.Close()

	backends := map[string]config.BackendConfig{
		"primary":  {URL: primary.URL, Type: "openai", Default: true, Models: []string{"gemma"}},
		"fallback": {URL: fallback.URL, Type: "openai", Models: []string{"zzz-*"}},
	}
	rt := newE2EFailoverRouter(t, backends, map[string]*httptest.Server{"primary": primary, "fallback": fallback})

	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	cfg := &config.Config{
		Backend: primary.URL,
		Session: config.SessionConfig{
			Timeout:           5 * time.Minute,
			Header:            "X-Session-ID",
			GenerateIfMissing: true,
		},
	}

	p, err := New(cfg, store, manager, WithRouter(rt))
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	fc := NewFailoverController(FailoverConfig{
		Enabled:       true,
		MaxRetries:    3,
		FallbackOrder: []string{"primary", "fallback"},
		RetryDelay:    0,
	})
	fc.RegisterBackend("primary", primary.URL, "openai", 0)
	fc.RegisterBackend("fallback", fallback.URL, "openai", 1)
	p.SetFailoverController(fc)

	const sessionID = "e2e-failover-skip-exhausted"
	reqBody := `{"model":"gemma","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Session-ID", sessionID)

	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if !primaryHit {
		t.Error("expected primary to be hit (it fails, triggering failover)")
	}
	if fallbackHit {
		t.Error("fallback backend must never be hit: its model is unmappable and it must be skipped")
	}

	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502 (all backends unavailable) after skip-exhaustion, got %d (body=%q)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "All backends unavailable") {
		t.Errorf("expected body to contain \"All backends unavailable\", got %q", w.Body.String())
	}
}
