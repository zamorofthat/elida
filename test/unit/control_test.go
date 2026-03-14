package unit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"elida/internal/control"
	"elida/internal/session"
	"elida/internal/storage"
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
	if health.Version != "0.2.1" {
		t.Errorf("expected version '0.2.1', got %s", health.Version)
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
	_ = json.NewDecoder(w.Result().Body).Decode(&sessResp)

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
	_ = json.NewDecoder(resp.Body).Decode(&result)
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

	t.Run("no origin header allows wildcard", func(t *testing.T) {
		req := httptest.NewRequest("OPTIONS", "/control/health", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("expected wildcard CORS for no-origin request, got %q", w.Header().Get("Access-Control-Allow-Origin"))
		}
		if w.Code != http.StatusOK {
			t.Errorf("expected status 200 for OPTIONS, got %d", w.Code)
		}
	})

	t.Run("same-origin allowed", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/control/health", nil)
		req.Host = "localhost:9090"
		req.Header.Set("Origin", "http://localhost:9090")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:9090" {
			t.Errorf("expected same-origin CORS, got %q", w.Header().Get("Access-Control-Allow-Origin"))
		}
	})

	t.Run("cross-origin blocked", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/control/health", nil)
		req.Host = "localhost:9090"
		req.Header.Set("Origin", "http://evil.com")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("expected no CORS header for cross-origin, got %q", w.Header().Get("Access-Control-Allow-Origin"))
		}
	})

	t.Run("subdomain trick blocked", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/control/health", nil)
		req.Host = "api.example.com"
		req.Header.Set("Origin", "https://evil-api.example.com.attacker.net")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("expected subdomain trick to be blocked, got %q", w.Header().Get("Access-Control-Allow-Origin"))
		}
	})
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

// Auth tests
func newTestHandlerWithAuth(apiKey string) *control.Handler { //nolint:unparam // test helper with constant value
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	return control.New(store, manager, control.WithAuth(apiKey))
}

func TestHandler_Auth_Unauthorized(t *testing.T) {
	handler := newTestHandlerWithAuth("secret-key-123")

	// Request without auth header should return 401
	req := httptest.NewRequest("GET", "/control/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}

	// Check WWW-Authenticate header
	if w.Header().Get("WWW-Authenticate") == "" {
		t.Error("expected WWW-Authenticate header")
	}
}

func TestHandler_Auth_WrongKey(t *testing.T) {
	handler := newTestHandlerWithAuth("secret-key-123")

	// Request with wrong key should return 401
	req := httptest.NewRequest("GET", "/control/health", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w.Code)
	}
}

func TestHandler_Auth_BearerToken(t *testing.T) {
	handler := newTestHandlerWithAuth("secret-key-123")

	// Request with correct Bearer token should succeed
	req := httptest.NewRequest("GET", "/control/health", nil)
	req.Header.Set("Authorization", "Bearer secret-key-123")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestHandler_Auth_XAPIKey(t *testing.T) {
	handler := newTestHandlerWithAuth("secret-key-123")

	// Request with X-API-Key header should succeed
	req := httptest.NewRequest("GET", "/control/health", nil)
	req.Header.Set("X-API-Key", "secret-key-123")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestHandler_Auth_DashboardNoAuth(t *testing.T) {
	handler := newTestHandlerWithAuth("secret-key-123")

	// Dashboard (root path) should NOT require auth
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Should get 200 (dashboard) or 404, but NOT 401
	if w.Code == http.StatusUnauthorized {
		t.Error("dashboard should not require auth")
	}
}

func TestHandler_Auth_AllControlEndpoints(t *testing.T) {
	handler := newTestHandlerWithAuth("secret-key-123")

	// All /control/* endpoints should require auth
	endpoints := []string{
		"/control/health",
		"/control/stats",
		"/control/sessions",
		"/control/sessions/test-123",
		"/control/history",
		"/control/flagged",
		"/control/voice",
	}

	for _, ep := range endpoints {
		req := httptest.NewRequest("GET", ep, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: expected status 401 without auth, got %d", ep, w.Code)
		}
	}
}

func TestHandler_History_Pagination(t *testing.T) {
	// Create a SQLite store with test data
	tmpFile, err := os.CreateTemp("", "elida-control-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	sqliteStore, err := storage.NewSQLiteStore(tmpFile.Name())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer sqliteStore.Close()

	// Insert test sessions
	now := time.Now()
	for i := 0; i < 5; i++ {
		record := storage.SessionRecord{
			ID:        "hist-sess-" + string(rune('a'+i)),
			State:     "completed",
			StartTime: now.Add(-time.Duration(i) * time.Minute),
			EndTime:   now,
			Backend:   "ollama",
		}
		if err := sqliteStore.SaveSession(record); err != nil {
			t.Fatalf("failed to save session: %v", err)
		}
	}

	memStore := session.NewMemoryStore()
	manager := session.NewManager(memStore, 5*time.Minute)
	handler := control.New(memStore, manager, control.WithHistory(sqliteStore))

	// Request first page with limit=2
	req := httptest.NewRequest("GET", "/control/history?limit=2&offset=0", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Verify pagination fields
	getFloat := func(key string) float64 {
		v, ok := resp[key].(float64)
		if !ok {
			t.Fatalf("expected %s to be a number, got %T", key, resp[key])
		}
		return v
	}

	if int(getFloat("total_count")) != 5 {
		t.Errorf("expected total_count 5, got %v", resp["total_count"])
	}
	if int(getFloat("count")) != 2 {
		t.Errorf("expected count 2, got %v", resp["count"])
	}
	if int(getFloat("offset")) != 0 {
		t.Errorf("expected offset 0, got %v", resp["offset"])
	}
	if int(getFloat("limit")) != 2 {
		t.Errorf("expected limit 2, got %v", resp["limit"])
	}

	sessions, ok := resp["sessions"].([]interface{})
	if !ok {
		t.Fatalf("expected sessions to be an array, got %T", resp["sessions"])
	}
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions in page, got %d", len(sessions))
	}
}

func TestHandler_WithPolicyOption(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	// WithPolicy should not panic and handler should work
	handler := control.New(store, manager, control.WithPolicy(nil))
	req := httptest.NewRequest("GET", "/control/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}
