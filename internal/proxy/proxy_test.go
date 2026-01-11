package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/session"
)

func newTestProxy(t *testing.T, backend *httptest.Server) (*Proxy, *session.Manager) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	cfg := &config.Config{
		Backend: backend.URL,
		Session: config.SessionConfig{
			Header:            "X-Session-ID",
			GenerateIfMissing: true,
			Timeout:           5 * time.Minute,
		},
	}

	proxy, err := New(cfg, store, manager)
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	return proxy, manager
}

func TestProxy_BasicRequest(t *testing.T) {
	// Mock backend
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":"ok"}`))
	}))
	defer backend.Close()

	proxy, _ := newTestProxy(t, backend)

	// Make request through proxy
	req := httptest.NewRequest("POST", "/api/test", strings.NewReader(`{"test":"data"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"response":"ok"}` {
		t.Errorf("unexpected response body: %s", body)
	}

	// Check session ID header is set
	sessionID := resp.Header.Get("X-Session-ID")
	if sessionID == "" {
		t.Error("expected X-Session-ID header to be set")
	}
}

func TestProxy_CustomSessionID(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`ok`))
	}))
	defer backend.Close()

	proxy, manager := newTestProxy(t, backend)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Session-ID", "my-custom-session")
	w := httptest.NewRecorder()

	proxy.ServeHTTP(w, req)

	// Verify custom session ID was used
	resp := w.Result()
	if resp.Header.Get("X-Session-ID") != "my-custom-session" {
		t.Error("expected custom session ID to be returned")
	}

	// Verify session exists in manager
	sess, ok := manager.Get("my-custom-session")
	if !ok {
		t.Fatal("expected session to exist")
	}
	if sess.ID != "my-custom-session" {
		t.Errorf("expected session ID 'my-custom-session', got %s", sess.ID)
	}
}

func TestProxy_KilledSessionRejected(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`ok`))
	}))
	defer backend.Close()

	proxy, manager := newTestProxy(t, backend)

	// Create and kill a session
	req1 := httptest.NewRequest("GET", "/test", nil)
	req1.Header.Set("X-Session-ID", "kill-me")
	w1 := httptest.NewRecorder()
	proxy.ServeHTTP(w1, req1)

	manager.Kill("kill-me")

	// Try to use killed session
	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.Header.Set("X-Session-ID", "kill-me")
	w2 := httptest.NewRecorder()
	proxy.ServeHTTP(w2, req2)

	resp := w2.Result()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "session_terminated") {
		t.Errorf("expected session_terminated error, got: %s", body)
	}
}

func TestProxy_BackendError(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"backend error"}`))
	}))
	defer backend.Close()

	proxy, _ := newTestProxy(t, backend)

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", resp.StatusCode)
	}
}

func TestProxy_StreamingDetection(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		accept    string
		streaming bool
	}{
		{"stream true", `{"stream":true}`, "", true},
		{"stream true with space", `{"stream": true}`, "", true},
		{"stream false", `{"stream":false}`, "", false},
		{"no stream", `{"model":"test"}`, "", false},
		{"SSE accept header", `{}`, "text/event-stream", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`ok`))
			}))
			defer backend.Close()

			proxy, _ := newTestProxy(t, backend)

			req := httptest.NewRequest("POST", "/test", strings.NewReader(tt.body))
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}

			isStreaming := proxy.isStreamingRequest(req, []byte(tt.body))
			if isStreaming != tt.streaming {
				t.Errorf("expected streaming=%v, got %v", tt.streaming, isStreaming)
			}
		})
	}
}

func TestProxy_SessionBytesTracking(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"response":"test response data"}`))
	}))
	defer backend.Close()

	proxy, manager := newTestProxy(t, backend)

	req := httptest.NewRequest("POST", "/test", strings.NewReader(`{"input":"test"}`))
	req.Header.Set("X-Session-ID", "bytes-test")
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	sess, _ := manager.Get("bytes-test")
	if sess.BytesIn == 0 {
		t.Error("expected BytesIn > 0")
	}
	if sess.BytesOut == 0 {
		t.Error("expected BytesOut > 0")
	}
}

func TestProxy_HeadersForwarded(t *testing.T) {
	var receivedHeaders http.Header
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		w.Write([]byte(`ok`))
	}))
	defer backend.Close()

	proxy, _ := newTestProxy(t, backend)

	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("X-Custom-Header", "custom-value")
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)

	if receivedHeaders.Get("Authorization") != "Bearer test-token" {
		t.Error("expected Authorization header to be forwarded")
	}
	if receivedHeaders.Get("X-Custom-Header") != "custom-value" {
		t.Error("expected custom header to be forwarded")
	}
}
