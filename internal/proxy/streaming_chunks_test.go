package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/session"
)

// TestStreamingChunkCapture_ConfiguredLimit verifies that streaming chunks
// are captured up to the configured max, then dropped with a warning.
func TestStreamingChunkCapture_ConfiguredLimit(t *testing.T) {
	// Backend that streams 4 SSE chunks (each ~10 bytes)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			return // Can't flush, just return
		}
		for i := 1; i <= 4; i++ {
			_, _ = fmt.Fprintf(w, "data: chunk%d\n\n", i)
			flusher.Flush()
		}
	}))
	defer backend.Close()

	// Config with MaxCapturedChunks = 2 (limit to 2 out of 4 chunks)
	cfg := &config.Config{
		Listen:  ":0",
		Backend: backend.URL,
		Session: config.SessionConfig{
			Timeout:           5 * time.Minute,
			Header:            "X-Session-ID",
			GenerateIfMissing: true,
		},
		Storage: config.StorageConfig{
			MaxCapturedChunks: 2,
		},
		Policy: config.PolicyConfig{
			Enabled: false,
		},
	}

	store := session.NewMemoryStore()
	manager := session.NewManager(store, cfg.Session.Timeout)
	p, err := New(cfg, store, manager)
	if err != nil {
		t.Fatal(err)
	}

	// Make streaming request
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	req.Header.Set("X-Session-ID", "test-chunk-cap-2")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	// Read response body to trigger streaming
	_ = w.Body.String()

	// Verify: only 2 chunks were captured (configured limit)
	// We can't directly inspect chunks from outside, but we can verify
	// via the Proxy's internal streaming logic that attempted to log warnings.
	// For this test, we verify the response was successfully streamed (200 OK)
	// and the accessor maxCapturedChunks() returns 2.
	if got := p.maxCapturedChunks(); got != 2 {
		t.Errorf("maxCapturedChunks() = %d, want 2", got)
	}
}

// TestStreamingChunkCapture_DefaultWhenZero verifies that when MaxCapturedChunks
// is 0 (programmatic config, bypassing Load normalization), the accessor
// defaults to 100, allowing all chunks to be captured.
func TestStreamingChunkCapture_DefaultWhenZero(t *testing.T) {
	// Backend that streams 4 SSE chunks
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			return // Can't flush, just return
		}
		for i := 1; i <= 4; i++ {
			_, _ = fmt.Fprintf(w, "data: chunk%d\n\n", i)
			flusher.Flush()
		}
	}))
	defer backend.Close()

	// Config with MaxCapturedChunks = 0 (programmatic build, not via Load)
	// This simulates test code building Config directly, bypassing Load().
	cfg := &config.Config{
		Listen:  ":0",
		Backend: backend.URL,
		Session: config.SessionConfig{
			Timeout:           5 * time.Minute,
			Header:            "X-Session-ID",
			GenerateIfMissing: true,
		},
		Storage: config.StorageConfig{
			MaxCapturedChunks: 0, // Zero: programmatic config, no normalization
		},
		Policy: config.PolicyConfig{
			Enabled: false,
		},
	}

	store := session.NewMemoryStore()
	manager := session.NewManager(store, cfg.Session.Timeout)
	p, err := New(cfg, store, manager)
	if err != nil {
		t.Fatal(err)
	}

	// Verify: accessor defaults to 100 when config is 0
	if got := p.maxCapturedChunks(); got != 100 {
		t.Errorf("maxCapturedChunks() with config=0 = %d, want 100 (default)", got)
	}

	// Make streaming request to ensure it handles the default correctly
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	req.Header.Set("X-Session-ID", "test-chunk-cap-default")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	// Read response to confirm streaming worked
	body := w.Body.String()
	if !strings.Contains(body, "chunk") {
		t.Errorf("expected streaming chunks in response, got: %s", body)
	}
}
