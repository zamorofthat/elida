package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"elida/internal/session"
)

// newTestAPIWithAuth creates a test Handler with auth enabled
func newTestAPIWithAuth(_ *testing.T, apiKey string) *Handler {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 30*time.Second)
	return New(store, manager, WithAuth(apiKey))
}

// TestHealthExemptFromControlAuth verifies that /control/health is accessible
// without credentials while other /control/* paths remain protected.
// Feedback #5: liveness/readiness probes can't carry credentials — the
// health endpoint must answer without auth while every other /control/*
// path stays locked. Health response must not disclose version/capture_mode
// (use authenticated endpoints for that).
func TestHealthExemptFromControlAuth(t *testing.T) {
	h := newTestAPIWithAuth(t, "control-key")

	// Health: no credentials -> 200 with minimal payload.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/control/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/control/health without auth = %d, want 200", rec.Code)
	}

	// Verify minimal payload (status, timestamp only; no version/capture_mode).
	var healthResp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &healthResp); err != nil {
		t.Fatalf("failed to unmarshal health response: %v", err)
	}
	if status, ok := healthResp["status"]; !ok || status != "ok" {
		t.Errorf("health response missing status=ok")
	}
	if _, ok := healthResp["timestamp"]; !ok {
		t.Errorf("health response missing timestamp")
	}
	if _, ok := healthResp["version"]; ok {
		t.Errorf("health response should not include version (use authenticated endpoint)")
	}
	if _, ok := healthResp["capture_mode"]; ok {
		t.Errorf("health response should not include capture_mode (use authenticated endpoint)")
	}

	// Any other control path: still 401 without credentials.
	for _, path := range []string{"/control/sessions", "/control/flagged", "/control/healthz-lookalike"} {
		protectedRec := httptest.NewRecorder()
		h.ServeHTTP(protectedRec, httptest.NewRequest("GET", path, nil))
		if protectedRec.Code != http.StatusUnauthorized {
			t.Errorf("%s without auth = %d, want 401", path, protectedRec.Code)
		}
	}

	// Prefix tricks don't widen the hole.
	pathTraversalRec := httptest.NewRecorder()
	h.ServeHTTP(pathTraversalRec, httptest.NewRequest("GET", "/control/health/../sessions", nil))
	if pathTraversalRec.Code == http.StatusOK && strings.Contains(pathTraversalRec.Body.String(), "sessions") {
		t.Error("path traversal past the health exemption")
	}
}
