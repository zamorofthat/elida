package unit

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/proxy"
	"elida/internal/session"
)

func TestProxyAuth(t *testing.T) {
	// Create a mock backend
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer backend.Close()

	tests := []struct {
		name           string
		authEnabled    bool
		apiKey         string
		requestHeaders map[string]string
		wantStatus     int
	}{
		{
			name:        "auth disabled - no header",
			authEnabled: false,
			apiKey:      "",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "auth enabled - no header",
			authEnabled: true,
			apiKey:      "test-secret-key",
			wantStatus:  http.StatusUnauthorized,
		},
		{
			name:        "auth enabled - wrong key",
			authEnabled: true,
			apiKey:      "test-secret-key",
			requestHeaders: map[string]string{
				"X-Elida-API-Key": "wrong-key",
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:        "auth enabled - correct X-Elida-API-Key",
			authEnabled: true,
			apiKey:      "test-secret-key",
			requestHeaders: map[string]string{
				"X-Elida-API-Key": "test-secret-key",
			},
			wantStatus: http.StatusOK,
		},
		{
			name:        "auth enabled - correct Bearer token",
			authEnabled: true,
			apiKey:      "test-secret-key",
			requestHeaders: map[string]string{
				"Authorization": "Bearer test-secret-key",
			},
			wantStatus: http.StatusOK,
		},
		{
			name:        "auth enabled - health endpoint bypasses auth",
			authEnabled: true,
			apiKey:      "test-secret-key",
			wantStatus:  http.StatusOK, // Health endpoints bypass auth
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Backend: backend.URL,
				Session: config.SessionConfig{
					Timeout:           5 * time.Minute,
					Header:            "X-Session-ID",
					GenerateIfMissing: true,
				},
				Proxy: config.ProxyConfig{
					Auth: config.ProxyAuthConfig{
						Enabled: tt.authEnabled,
						APIKey:  tt.apiKey,
					},
				},
			}

			store := session.NewMemoryStore()
			manager := session.NewManager(store, cfg.Session.Timeout)
			p, err := proxy.New(cfg, store, manager)
			if err != nil {
				t.Fatalf("failed to create proxy: %v", err)
			}

			// Determine path - use /health for health endpoint test
			path := "/v1/chat/completions"
			if strings.Contains(tt.name, "health endpoint") {
				path = "/health"
			}

			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			for k, v := range tt.requestHeaders {
				req.Header.Set(k, v)
			}

			rr := httptest.NewRecorder()
			p.ServeHTTP(rr, req)

			// For health endpoint test, we expect it to pass through (not 401)
			// even though it might 404 on the backend
			if strings.Contains(tt.name, "health endpoint") {
				if rr.Code == http.StatusUnauthorized {
					t.Errorf("health endpoint should bypass auth, got status %d", rr.Code)
				}
				return
			}

			if rr.Code != tt.wantStatus {
				body, _ := io.ReadAll(rr.Body)
				t.Errorf("got status %d, want %d. Body: %s", rr.Code, tt.wantStatus, string(body))
			}
		})
	}
}

func TestProxyAuthHeaderNotLeaked(t *testing.T) {
	// Verify that X-Elida-API-Key is not forwarded to backend
	var receivedHeaders http.Header
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer backend.Close()

	cfg := &config.Config{
		Backend: backend.URL,
		Session: config.SessionConfig{
			Timeout:           5 * time.Minute,
			Header:            "X-Session-ID",
			GenerateIfMissing: true,
		},
		Proxy: config.ProxyConfig{
			Auth: config.ProxyAuthConfig{
				Enabled: true,
				APIKey:  "test-secret-key",
			},
		},
	}

	store := session.NewMemoryStore()
	manager := session.NewManager(store, cfg.Session.Timeout)
	p, err := proxy.New(cfg, store, manager)
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Elida-API-Key", "test-secret-key")

	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)

	// Verify backend did NOT receive the ELIDA auth header (security requirement)
	if receivedHeaders.Get("X-Elida-API-Key") != "" {
		t.Errorf("X-Elida-API-Key should be stripped before forwarding to backend")
	}
}
