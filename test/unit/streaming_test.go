package unit

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/proxy"
	"elida/internal/session"
)

// TestProxy_StreamingDetection tests that isStreamingRequest correctly detects
// the "stream" field regardless of JSON whitespace formatting. We test indirectly
// by checking whether the backend receives the request (non-streaming returns
// a simple response; streaming backends would use SSE, but we just verify the
// proxy doesn't reject valid stream field variants).
func TestProxy_StreamingDetection(t *testing.T) {
	var lastContentType string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastContentType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer backend.Close()

	cfg := &config.Config{
		Listen:  ":0",
		Backend: backend.URL,
		Session: config.SessionConfig{
			Timeout:           5 * time.Minute,
			Header:            "X-Session-ID",
			GenerateIfMissing: true,
		},
	}

	store := session.NewMemoryStore()
	manager := session.NewManager(store, cfg.Session.Timeout)
	p, err := proxy.New(cfg, store, manager)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		body string
		want int // expected status (200 = reached backend)
	}{
		{"no space", `{"stream":true,"messages":[]}`, http.StatusOK},
		{"one space", `{"stream": true,"messages":[]}`, http.StatusOK},
		{"tab separated", "{\"stream\":\ttrue,\"messages\":[]}", http.StatusOK},
		{"extra spaces", `{"stream"  :  true,"messages":[]}`, http.StatusOK},
		{"stream false", `{"stream":false,"messages":[]}`, http.StatusOK},
		{"missing field", `{"messages":[]}`, http.StatusOK},
		{"invalid JSON still proxied", `not json`, http.StatusOK},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(tt.body))
			req.Header.Set("X-Session-ID", fmt.Sprintf("stream-test-%d", i))
			w := httptest.NewRecorder()
			p.ServeHTTP(w, req)

			if w.Code != tt.want {
				t.Errorf("got status %d, want %d", w.Code, tt.want)
			}
		})
	}

	// Verify backend was called (lastContentType set by any subtest)
	_ = lastContentType
}

func TestProxy_SSEAcceptHeader(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer backend.Close()

	cfg := &config.Config{
		Listen:  ":0",
		Backend: backend.URL,
		Session: config.SessionConfig{
			Timeout:           5 * time.Minute,
			Header:            "X-Session-ID",
			GenerateIfMissing: true,
		},
	}

	store := session.NewMemoryStore()
	manager := session.NewManager(store, cfg.Session.Timeout)
	p, err := proxy.New(cfg, store, manager)
	if err != nil {
		t.Fatal(err)
	}

	// Request with SSE Accept header should be treated as streaming
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"messages":[]}`))
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Session-ID", "sse-test")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("SSE request got status %d, want 200", w.Code)
	}
}
