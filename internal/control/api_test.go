package control

import (
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
// path stays locked.
func TestHealthExemptFromControlAuth(t *testing.T) {
	h := newTestAPIWithAuth(t, "control-key")

	// Health: no credentials -> 200.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/control/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/control/health without auth = %d, want 200", rec.Code)
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
