package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"elida/internal/panel"
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

// stubPanel is a minimal PanelProvider for testing handlePanel.
type stubPanel struct{ members []panel.MemberInfo }

func (s stubPanel) Members() []panel.MemberInfo { return s.members }

// TestHandlePanel_RequiresAuth verifies GET /control/panel is auth-gated like
// every other /control/* endpoint (except /control/health).
func TestHandlePanel_RequiresAuth(t *testing.T) {
	h := newTestAPIWithAuth(t, "control-key")

	req := httptest.NewRequest(http.MethodGet, "/control/panel", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without auth", rec.Code)
	}
}

// TestHandlePanel_ReturnsMembers verifies GET /control/panel returns the
// seated panel members as JSON when authenticated.
func TestHandlePanel_ReturnsMembers(t *testing.T) {
	h := newTestAPIWithAuth(t, "control-key")
	h.SetPanel(stubPanel{members: []panel.MemberInfo{{Name: "m3-lite", Version: "1", Shadow: false, Weight: 1}}})

	req := httptest.NewRequest(http.MethodGet, "/control/panel", nil)
	req.Header.Set("Authorization", "Bearer control-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body struct {
		Members []struct {
			Name    string  `json:"name"`
			Version string  `json:"version"`
			Shadow  bool    `json:"shadow"`
			Weight  float64 `json:"weight"`
		} `json:"members"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if len(body.Members) != 1 || body.Members[0].Name != "m3-lite" {
		t.Fatalf("members = %+v, want one m3-lite", body.Members)
	}
}

// TestHandlePanel_NilProviderReturnsEmpty verifies the endpoint degrades
// gracefully to an empty roster when no panel has been wired in.
func TestHandlePanel_NilProviderReturnsEmpty(t *testing.T) {
	h := newTestAPIWithAuth(t, "control-key")

	req := httptest.NewRequest(http.MethodGet, "/control/panel", nil)
	req.Header.Set("Authorization", "Bearer control-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body struct {
		Members []struct{} `json:"members"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if len(body.Members) != 0 {
		t.Fatalf("members = %+v, want empty", body.Members)
	}
}

// TestHandlePanel_MethodNotAllowed verifies non-GET requests are rejected.
func TestHandlePanel_MethodNotAllowed(t *testing.T) {
	h := newTestAPIWithAuth(t, "control-key")

	req := httptest.NewRequest(http.MethodPost, "/control/panel", nil)
	req.Header.Set("Authorization", "Bearer control-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
