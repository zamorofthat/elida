package unit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"elida/internal/control"
	"elida/internal/session"
)

func newTestHandler() (*control.Handler, *session.Manager) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	handler := control.New(store, manager)
	return handler, manager
}

func TestHandler_Health(t *testing.T) {
	handler, _ := newTestHandler()

	req := httptest.NewRequest("GET", "/control/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var health control.HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if health.Status != "ok" {
		t.Errorf("expected status 'ok', got %s", health.Status)
	}
	if health.Version != "0.1.0" {
		t.Errorf("expected version '0.1.0', got %s", health.Version)
	}
}

func TestHandler_Health_MethodNotAllowed(t *testing.T) {
	handler, _ := newTestHandler()

	req := httptest.NewRequest("POST", "/control/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}

func TestHandler_Stats(t *testing.T) {
	handler, manager := newTestHandler()

	// Create some sessions
	manager.GetOrCreate("active-1", "http://backend", "127.0.0.1")
	manager.GetOrCreate("active-2", "http://backend", "127.0.0.1")
	manager.GetOrCreate("killed", "http://backend", "127.0.0.1")
	manager.Kill("killed")

	req := httptest.NewRequest("GET", "/control/stats", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var stats session.Stats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if stats.Total != 3 {
		t.Errorf("expected total 3, got %d", stats.Total)
	}
	if stats.Active != 2 {
		t.Errorf("expected active 2, got %d", stats.Active)
	}
	if stats.Killed != 1 {
		t.Errorf("expected killed 1, got %d", stats.Killed)
	}
}

func TestHandler_Sessions_ListAll(t *testing.T) {
	handler, manager := newTestHandler()

	manager.GetOrCreate("sess-1", "http://backend", "127.0.0.1")
	manager.GetOrCreate("sess-2", "http://backend", "127.0.0.1")

	req := httptest.NewRequest("GET", "/control/sessions", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var sessResp control.SessionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&sessResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if sessResp.Total != 2 {
		t.Errorf("expected total 2, got %d", sessResp.Total)
	}
	if len(sessResp.Sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(sessResp.Sessions))
	}
}

func TestHandler_Sessions_ActiveOnly(t *testing.T) {
	handler, manager := newTestHandler()

	manager.GetOrCreate("active", "http://backend", "127.0.0.1")
	manager.GetOrCreate("killed", "http://backend", "127.0.0.1")
	manager.Kill("killed")

	req := httptest.NewRequest("GET", "/control/sessions?active=true", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	var sessResp control.SessionsResponse
	json.NewDecoder(w.Result().Body).Decode(&sessResp)

	if sessResp.Total != 1 {
		t.Errorf("expected total 1, got %d", sessResp.Total)
	}
}

func TestHandler_Session_GetByID(t *testing.T) {
	handler, manager := newTestHandler()

	sess := manager.GetOrCreate("test-session", "http://backend", "127.0.0.1")
	sess.AddBytes(100, 200)

	req := httptest.NewRequest("GET", "/control/sessions/test-session", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var sessInfo control.SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&sessInfo); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if sessInfo.ID != "test-session" {
		t.Errorf("expected ID 'test-session', got %s", sessInfo.ID)
	}
	if sessInfo.BytesIn != 100 {
		t.Errorf("expected BytesIn 100, got %d", sessInfo.BytesIn)
	}
	if sessInfo.BytesOut != 200 {
		t.Errorf("expected BytesOut 200, got %d", sessInfo.BytesOut)
	}
}

func TestHandler_Session_NotFound(t *testing.T) {
	handler, _ := newTestHandler()

	req := httptest.NewRequest("GET", "/control/sessions/nonexistent", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

func TestHandler_Session_Kill(t *testing.T) {
	handler, manager := newTestHandler()

	manager.GetOrCreate("kill-me", "http://backend", "127.0.0.1")

	req := httptest.NewRequest("POST", "/control/sessions/kill-me/kill", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// Verify session is killed
	sess, _ := manager.Get("kill-me")
	if sess.GetState() != session.Killed {
		t.Error("expected session to be killed")
	}

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	if result["status"] != "killed" {
		t.Errorf("expected status 'killed', got %s", result["status"])
	}
}

func TestHandler_Session_Kill_NotFound(t *testing.T) {
	handler, _ := newTestHandler()

	req := httptest.NewRequest("POST", "/control/sessions/nonexistent/kill", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

func TestHandler_Session_Kill_AlreadyKilled(t *testing.T) {
	handler, manager := newTestHandler()

	manager.GetOrCreate("already-killed", "http://backend", "127.0.0.1")
	manager.Kill("already-killed")

	req := httptest.NewRequest("POST", "/control/sessions/already-killed/kill", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404 for already killed session, got %d", w.Code)
	}
}

func TestHandler_Session_Delete(t *testing.T) {
	handler, manager := newTestHandler()

	manager.GetOrCreate("delete-me", "http://backend", "127.0.0.1")

	// DELETE should also kill
	req := httptest.NewRequest("DELETE", "/control/sessions/delete-me", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	sess, _ := manager.Get("delete-me")
	if sess.GetState() != session.Killed {
		t.Error("expected session to be killed via DELETE")
	}
}

func TestHandler_CORS(t *testing.T) {
	handler, _ := newTestHandler()

	req := httptest.NewRequest("OPTIONS", "/control/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("expected CORS header to be set")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected status 200 for OPTIONS, got %d", w.Code)
	}
}

func TestHandler_VoiceSessions_NoWebSocketHandler(t *testing.T) {
	handler, _ := newTestHandler()

	// When WebSocket handler is not set, should return 503
	req := httptest.NewRequest("GET", "/control/voice", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503 when WebSocket handler not set, got %d", w.Code)
	}
}

func TestHandler_VoiceSession_NoWebSocketHandler(t *testing.T) {
	handler, _ := newTestHandler()

	// When WebSocket handler is not set, should return 503
	req := httptest.NewRequest("GET", "/control/voice/session-123", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503 when WebSocket handler not set, got %d", w.Code)
	}
}

func TestHandler_VoiceSession_Actions_NoWebSocketHandler(t *testing.T) {
	handler, _ := newTestHandler()

	// POST actions should also return 503 when WebSocket handler is not set
	tests := []struct {
		path string
	}{
		{"/control/voice/session-123/voice-1/bye"},
		{"/control/voice/session-123/voice-1/hold"},
		{"/control/voice/session-123/voice-1/resume"},
	}

	for _, tt := range tests {
		req := httptest.NewRequest("POST", tt.path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: expected status 503 when WebSocket handler not set, got %d", tt.path, w.Code)
		}
	}
}
