package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"elida/internal/config"
)

// modelListBackend returns an httptest server that, if hit, answers a
// canned OpenAI-style /v1/models list and counts how many requests it
// received. Used to prove multi-backend mode never reaches a backend and
// single-backend mode still passes the real backend response through
// byte-identical.
func modelListBackend(t *testing.T, hits *int64, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
}

// TestModelsEndpointMultiBackendAggregates exercises the real ServeHTTP
// path for GET /v1/models with 2+ configured backends: ELIDA must answer
// directly from configuration in OpenAI list format and must never reach
// either backend.
func TestModelsEndpointMultiBackendAggregates(t *testing.T) {
	var hitsA, hitsB int64
	backendA := modelListBackend(t, &hitsA, `{"object":"list","data":[]}`)
	defer backendA.Close()
	backendB := modelListBackend(t, &hitsB, `{"object":"list","data":[]}`)
	defer backendB.Close()

	cfg := config.DefaultConfig()
	cfg.Backends = map[string]config.BackendConfig{
		"local": {URL: backendA.URL, Type: "openai", Models: []string{"gemma", "qwen"}, Default: true},
		"mistral": {
			URL:    backendB.URL,
			Type:   "mistral",
			Models: []string{"mistral-*"},
			Model:  "mistral-small-latest",
		},
	}

	p, _ := newE2EProxy(t, "", func(c *config.Config) {
		c.Backends = cfg.Backends
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to unmarshal response: %v (body=%s)", err, rec.Body.String())
	}
	if got.Object != "list" {
		t.Errorf("object = %q, want %q", got.Object, "list")
	}

	wantIDs := map[string]string{ // id -> owned_by
		"gemma":                "local",
		"qwen":                 "local",
		"mistral-small-latest": "mistral",
	}
	if len(got.Data) != len(wantIDs) {
		t.Fatalf("got %d models, want %d: %+v", len(got.Data), len(wantIDs), got.Data)
	}
	for _, m := range got.Data {
		if m.Object != "model" {
			t.Errorf("model %q object = %q, want %q", m.ID, m.Object, "model")
		}
		if wantIDs[m.ID] != m.OwnedBy {
			t.Errorf("model %q owned_by %q, want %q", m.ID, m.OwnedBy, wantIDs[m.ID])
		}
	}

	if h := atomic.LoadInt64(&hitsA); h != 0 {
		t.Errorf("backend A hit %d times, want 0 (aggregated response must come from config)", h)
	}
	if h := atomic.LoadInt64(&hitsB); h != 0 {
		t.Errorf("backend B hit %d times, want 0 (aggregated response must come from config)", h)
	}
}

// TestModelsEndpointMultiBackendRequiresAuth proves the /v1/models
// intercept runs AFTER proxy auth: an unauthenticated caller must still
// get 401, never the aggregated model list.
func TestModelsEndpointMultiBackendRequiresAuth(t *testing.T) {
	var hitsA, hitsB int64
	backendA := modelListBackend(t, &hitsA, `{"object":"list","data":[]}`)
	defer backendA.Close()
	backendB := modelListBackend(t, &hitsB, `{"object":"list","data":[]}`)
	defer backendB.Close()

	p, _ := newE2EProxy(t, "", func(c *config.Config) {
		c.Backends = map[string]config.BackendConfig{
			"local":   {URL: backendA.URL, Type: "openai", Models: []string{"gemma"}, Default: true},
			"mistral": {URL: backendB.URL, Type: "mistral", Models: []string{"mistral-*"}, Model: "mistral-small-latest"},
		}
		c.Proxy.Auth.Enabled = true
		c.Proxy.Auth.APIKey = "sekret"
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.RemoteAddr = "192.0.2.10:1000"
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d (body=%s)", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
	if h := atomic.LoadInt64(&hitsA); h != 0 {
		t.Errorf("backend A hit %d times, want 0 (unauthenticated request must not proxy)", h)
	}
	if h := atomic.LoadInt64(&hitsB); h != 0 {
		t.Errorf("backend B hit %d times, want 0 (unauthenticated request must not proxy)", h)
	}
}

// TestModelsEndpointSingleBackendPassthrough exercises the real ServeHTTP
// path for GET /v1/models with exactly one configured backend: the
// request must reach the backend and its response must come back
// byte-identical (passthrough preserved, never break real llama.cpp model
// lists).
func TestModelsEndpointSingleBackendPassthrough(t *testing.T) {
	var hits int64
	const canned = `{"object":"list","data":[{"id":"real-backend-model","object":"model","owned_by":"llamacpp"}]}`
	backend := modelListBackend(t, &hits, canned)
	defer backend.Close()

	p, _ := newE2EProxy(t, backend.URL, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != canned {
		t.Errorf("body = %q, want verbatim canned response %q", rec.Body.String(), canned)
	}
	if h := atomic.LoadInt64(&hits); h != 1 {
		t.Errorf("backend hit %d times, want exactly 1 (passthrough must reach the single backend)", h)
	}
}
