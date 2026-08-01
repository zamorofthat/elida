package proxy

import (
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

func rb(model string, globs []string, typ string) *router.Backend {
	return &router.Backend{Name: "t", Model: model, Models: globs, Type: typ}
}

func TestResolveFailoverModel(t *testing.T) {
	tests := []struct {
		name     string
		original string
		target   *router.Backend
		want     string
		ok       bool
	}{
		{"explicit model wins", "gemma", rb("nvidia/llama-3.3-nemotron-super-49b-v1", []string{"nvidia/*"}, "openai"), "nvidia/llama-3.3-nemotron-super-49b-v1", true},
		{"glob match keeps original", "gpt-4o", rb("", []string{"gpt-*"}, "openai"), "gpt-4o", true},
		{"remap table accepted when it matches globs", "claude-3-5-sonnet-20241022", rb("", []string{"gpt-*"}, "openai"), "gpt-4", true}, // SelectCompatibleModel falls to defaultModels["openai"] = "gpt-4", which matches the gpt-* glob
		{"unmappable cross-type skips", "gemma", rb("", []string{"mistral-*"}, "mistral"), "", false},
		{"no globs no model cross-type unmappable original", "gemma", rb("", nil, "mistral"), "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ResolveFailoverModel(tt.original, tt.target)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v (got model %q)", ok, tt.ok, got)
			}
			if tt.ok && tt.want != "" && got != tt.want {
				t.Errorf("model = %q, want %q", got, tt.want)
			}
			if ok && got == "" {
				t.Error("ok with empty model")
			}
		})
	}
}

// TestFailover_SkipsUnmappableBackend_ThenSucceeds reproduces feedback #8
// end-to-end through attemptFailoverWithDepth: a "gemma" request fails over
// past a Mistral-type backend that has no Model/Models configured (so its
// model can't be validated or remapped) and lands on a compatible OpenAI
// backend instead. The Mistral backend must never receive a request.
func TestFailover_SkipsUnmappableBackend_ThenSucceeds(t *testing.T) {
	primaryHit := false
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryHit = true
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer primary.Close()

	mistralHit := false
	mistral := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mistralHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer mistral.Close()

	openaiHit := false
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openaiHit = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi from openai fallback"}}]}`))
	}))
	defer openai.Close()

	backends := map[string]config.BackendConfig{
		"primary": {URL: primary.URL, Type: "openai", Default: true},
		"mistral": {URL: mistral.URL, Type: "mistral"}, // no Model, no Models: can't validate or remap "gemma"
		"openai":  {URL: openai.URL, Type: "openai"},
	}
	rt, err := router.NewRouter(backends, config.RoutingConfig{Methods: []string{"default"}})
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	for name, srv := range map[string]*httptest.Server{"primary": primary, "mistral": mistral, "openai": openai} {
		b, _ := rt.GetBackend(name)
		u, _ := url.Parse(srv.URL)
		b.URL = u
		b.Transport = srv.Client().Transport.(*http.Transport) //nolint:errcheck
	}

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
		FallbackOrder: []string{"mistral", "openai"}, // mistral must be tried (and skipped) before openai
		RetryDelay:    0,
	})
	fc.RegisterBackend("primary", primary.URL, "openai", 1)
	fc.RegisterBackend("mistral", mistral.URL, "mistral", 2)
	fc.RegisterBackend("openai", openai.URL, "openai", 3)
	p.SetFailoverController(fc)

	reqBody := `{"model":"gemma","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Session-ID", "skip-unmappable-test")

	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if !primaryHit {
		t.Error("expected primary backend to be hit (it fails, triggering failover)")
	}
	if mistralHit {
		t.Error("mistral backend must never be hit: its model is unmappable and it must be skipped")
	}
	if !openaiHit {
		t.Error("expected the openai fallback to be hit after mistral was skipped")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 from the eventual openai fallback, got %d", w.Code)
	}
}
