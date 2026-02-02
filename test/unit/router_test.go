package unit

import (
	"net/http/httptest"
	"testing"

	"elida/internal/config"
	"elida/internal/router"
)

func TestRouter_NewRouter(t *testing.T) {
	backends := map[string]config.BackendConfig{
		"ollama": {
			URL:     "http://localhost:11434",
			Type:    "ollama",
			Default: true,
		},
		"openai": {
			URL:    "https://api.openai.com",
			Type:   "openai",
			Models: []string{"gpt-*", "o1-*"},
		},
	}
	routing := config.RoutingConfig{
		Methods: []string{"header", "model", "path", "default"},
	}

	r, err := router.NewRouter(backends, routing)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	if len(r.Backends()) != 2 {
		t.Errorf("expected 2 backends, got %d", len(r.Backends()))
	}

	defaultBackend := r.GetDefaultBackend()
	if defaultBackend == nil {
		t.Fatal("expected default backend to be set")
	}
	if defaultBackend.Name != "ollama" {
		t.Errorf("expected default backend 'ollama', got %q", defaultBackend.Name)
	}
}

func TestRouter_NewRouter_NoDefault(t *testing.T) {
	backends := map[string]config.BackendConfig{
		"ollama": {
			URL:  "http://localhost:11434",
			Type: "ollama",
			// No default set
		},
	}

	_, err := router.NewRouter(backends, config.RoutingConfig{})
	if err == nil {
		t.Fatal("expected error for missing default backend")
	}
}

func TestRouter_NewSingleBackendRouter(t *testing.T) {
	r, err := router.NewSingleBackendRouter("http://localhost:11434")
	if err != nil {
		t.Fatalf("failed to create single backend router: %v", err)
	}

	backend := r.GetDefaultBackend()
	if backend == nil {
		t.Fatal("expected default backend to be set")
	}
	if backend.URL.String() != "http://localhost:11434" {
		t.Errorf("expected URL 'http://localhost:11434', got %q", backend.URL.String())
	}
}

func TestRouter_HeaderPriority(t *testing.T) {
	backends := map[string]config.BackendConfig{
		"ollama": {
			URL:     "http://localhost:11434",
			Type:    "ollama",
			Default: true,
		},
		"openai": {
			URL:    "https://api.openai.com",
			Type:   "openai",
			Models: []string{"gpt-*"},
		},
	}
	routing := config.RoutingConfig{
		Methods: []string{"header", "model", "default"},
	}

	r, err := router.NewRouter(backends, routing)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	// Request with X-Backend header should use that backend
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("X-Backend", "openai")

	// Body has model that would match ollama, but header takes priority
	body := []byte(`{"model": "llama2"}`)

	backend, err := r.Select(req, body)
	if err != nil {
		t.Fatalf("select failed: %v", err)
	}

	if backend.Name != "openai" {
		t.Errorf("expected backend 'openai' (from header), got %q", backend.Name)
	}
}

func TestRouter_ModelMatching(t *testing.T) {
	backends := map[string]config.BackendConfig{
		"ollama": {
			URL:     "http://localhost:11434",
			Type:    "ollama",
			Default: true,
		},
		"openai": {
			URL:    "https://api.openai.com",
			Type:   "openai",
			Models: []string{"gpt-*", "o1-*"},
		},
		"anthropic": {
			URL:    "https://api.anthropic.com",
			Type:   "anthropic",
			Models: []string{"claude-*"},
		},
	}
	routing := config.RoutingConfig{
		Methods: []string{"model", "default"},
	}

	r, err := router.NewRouter(backends, routing)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	tests := []struct {
		name            string
		body            []byte
		expectedBackend string
	}{
		{
			name:            "gpt-4 matches openai",
			body:            []byte(`{"model": "gpt-4"}`),
			expectedBackend: "openai",
		},
		{
			name:            "gpt-4-turbo matches openai",
			body:            []byte(`{"model": "gpt-4-turbo"}`),
			expectedBackend: "openai",
		},
		{
			name:            "claude-3-opus matches anthropic",
			body:            []byte(`{"model": "claude-3-opus"}`),
			expectedBackend: "anthropic",
		},
		{
			name:            "o1-preview matches openai",
			body:            []byte(`{"model": "o1-preview"}`),
			expectedBackend: "openai",
		},
		{
			name:            "unknown model uses default",
			body:            []byte(`{"model": "llama2"}`),
			expectedBackend: "ollama",
		},
		{
			name:            "no model uses default",
			body:            []byte(`{"prompt": "hello"}`),
			expectedBackend: "ollama",
		},
		{
			name:            "empty body uses default",
			body:            []byte{},
			expectedBackend: "ollama",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			backend, err := r.Select(req, tc.body)
			if err != nil {
				t.Fatalf("select failed: %v", err)
			}
			if backend.Name != tc.expectedBackend {
				t.Errorf("expected backend %q, got %q", tc.expectedBackend, backend.Name)
			}
		})
	}
}

func TestRouter_PathRouting(t *testing.T) {
	backends := map[string]config.BackendConfig{
		"ollama": {
			URL:     "http://localhost:11434",
			Type:    "ollama",
			Default: true,
		},
		"openai": {
			URL:  "https://api.openai.com",
			Type: "openai",
		},
		"anthropic": {
			URL:  "https://api.anthropic.com",
			Type: "anthropic",
		},
	}
	routing := config.RoutingConfig{
		Methods: []string{"path", "default"},
	}

	r, err := router.NewRouter(backends, routing)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	tests := []struct {
		name            string
		path            string
		expectedBackend string
	}{
		{
			name:            "/openai/ prefix routes to openai",
			path:            "/openai/v1/chat/completions",
			expectedBackend: "openai",
		},
		{
			name:            "/anthropic/ prefix routes to anthropic",
			path:            "/anthropic/v1/messages",
			expectedBackend: "anthropic",
		},
		{
			name:            "no prefix uses default",
			path:            "/v1/chat/completions",
			expectedBackend: "ollama",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", tc.path, nil)
			backend, err := r.Select(req, nil)
			if err != nil {
				t.Fatalf("select failed: %v", err)
			}
			if backend.Name != tc.expectedBackend {
				t.Errorf("expected backend %q, got %q", tc.expectedBackend, backend.Name)
			}
		})
	}
}

func TestRouter_DefaultFallback(t *testing.T) {
	backends := map[string]config.BackendConfig{
		"ollama": {
			URL:     "http://localhost:11434",
			Type:    "ollama",
			Default: true,
		},
		"openai": {
			URL:    "https://api.openai.com",
			Type:   "openai",
			Models: []string{"gpt-*"},
		},
	}
	routing := config.RoutingConfig{
		Methods: []string{"header", "model", "path", "default"},
	}

	r, err := router.NewRouter(backends, routing)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	// Request with no matching criteria should use default
	req := httptest.NewRequest("POST", "/api/generate", nil)
	body := []byte(`{"prompt": "hello"}`)

	backend, err := r.Select(req, body)
	if err != nil {
		t.Fatalf("select failed: %v", err)
	}

	if backend.Name != "ollama" {
		t.Errorf("expected default backend 'ollama', got %q", backend.Name)
	}
}

func TestRouter_UnknownHeaderBackend(t *testing.T) {
	backends := map[string]config.BackendConfig{
		"ollama": {
			URL:     "http://localhost:11434",
			Type:    "ollama",
			Default: true,
		},
	}
	routing := config.RoutingConfig{
		Methods: []string{"header", "default"},
	}

	r, err := router.NewRouter(backends, routing)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	// Request with unknown backend in header should fall back to default
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("X-Backend", "nonexistent")

	backend, err := r.Select(req, nil)
	if err != nil {
		t.Fatalf("select failed: %v", err)
	}

	// Should fall through to default
	if backend.Name != "ollama" {
		t.Errorf("expected default backend 'ollama', got %q", backend.Name)
	}
}

func TestRouter_GetBackend(t *testing.T) {
	backends := map[string]config.BackendConfig{
		"ollama": {
			URL:     "http://localhost:11434",
			Default: true,
		},
		"openai": {
			URL: "https://api.openai.com",
		},
	}

	r, err := router.NewRouter(backends, config.RoutingConfig{})
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	// Get existing backend
	b, ok := r.GetBackend("openai")
	if !ok {
		t.Fatal("expected to find backend 'openai'")
	}
	if b.URL.String() != "https://api.openai.com" {
		t.Errorf("unexpected URL: %s", b.URL.String())
	}

	// Get non-existent backend
	_, ok = r.GetBackend("nonexistent")
	if ok {
		t.Error("expected backend 'nonexistent' to not exist")
	}
}

func TestRouter_InvalidURL(t *testing.T) {
	backends := map[string]config.BackendConfig{
		"invalid": {
			URL:     "://invalid-url",
			Default: true,
		},
	}

	_, err := router.NewRouter(backends, config.RoutingConfig{})
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestRouter_EmptyBackends(t *testing.T) {
	backends := map[string]config.BackendConfig{}

	_, err := router.NewRouter(backends, config.RoutingConfig{})
	if err == nil {
		t.Fatal("expected error for empty backends")
	}
}
