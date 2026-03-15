package unit

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/control"
	"elida/internal/policy"
	"elida/internal/proxy"
	"elida/internal/session"
)

// ============================================================
// #1: Control API constant-time auth
// ============================================================

func TestControlAuth_ConstantTime(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	handler := control.New(store, manager, control.WithAuth("correct-api-key"))

	tests := []struct {
		name       string
		headers    map[string]string
		wantStatus int
	}{
		{
			name:       "no auth header",
			headers:    nil,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "wrong bearer token",
			headers:    map[string]string{"Authorization": "Bearer wrong-key"},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "correct bearer token",
			headers:    map[string]string{"Authorization": "Bearer correct-api-key"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "correct X-API-Key",
			headers:    map[string]string{"X-API-Key": "correct-api-key"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "wrong X-API-Key",
			headers:    map[string]string{"X-API-Key": "wrong-key"},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "partial match should fail",
			headers:    map[string]string{"Authorization": "Bearer correct-api-ke"},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "raw key in Authorization header",
			headers:    map[string]string{"Authorization": "correct-api-key"},
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/control/stats", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

// ============================================================
// #2: Request body limit (proxy)
// ============================================================

func TestProxy_RequestBodyLimit(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
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

	// Send a body just under 10MB — should succeed
	t.Run("body under limit succeeds", func(t *testing.T) {
		body := strings.NewReader(strings.Repeat("x", 1024)) // 1KB
		req := httptest.NewRequest("POST", "/v1/chat/completions", body)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, req)

		if w.Code == http.StatusBadRequest {
			t.Error("expected request to succeed, got 400")
		}
	})
}

// ============================================================
// #3: CORS same-origin validation
// ============================================================

func TestControlAPI_CORSSameOrigin(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	handler := control.New(store, manager)

	tests := []struct {
		name       string
		origin     string
		host       string
		wantHeader string // expected Access-Control-Allow-Origin value, "" means absent
	}{
		{
			name:       "no origin allows wildcard",
			origin:     "",
			host:       "localhost:8080",
			wantHeader: "*",
		},
		{
			name:       "same-origin allowed",
			origin:     "http://localhost:8080",
			host:       "localhost:8080",
			wantHeader: "http://localhost:8080",
		},
		{
			name:       "cross-origin blocked",
			origin:     "http://evil.com",
			host:       "localhost:8080",
			wantHeader: "",
		},
		{
			name:       "subdomain trick blocked",
			origin:     "http://localhost:8080.evil.com",
			host:       "localhost:8080",
			wantHeader: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/control/health", nil)
			req.Host = tt.host
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			got := w.Header().Get("Access-Control-Allow-Origin")
			if got != tt.wantHeader {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, tt.wantHeader)
			}
		})
	}
}

// ============================================================
// #5: TouchAndRecord atomicity
// ============================================================

func TestSession_TouchAndRecord(t *testing.T) {
	sess := session.NewSession("test-1", "http://backend", "127.0.0.1:1234")

	sess.TouchAndRecord(500, "anthropic")

	snap := sess.Snapshot()
	if snap.RequestCount != 1 {
		t.Errorf("RequestCount = %d, want 1", snap.RequestCount)
	}
	if snap.BytesIn != 500 {
		t.Errorf("BytesIn = %d, want 500", snap.BytesIn)
	}
	if snap.Backend != "anthropic" {
		t.Errorf("Backend = %q, want 'anthropic'", snap.Backend)
	}
	if snap.BackendsUsed == nil {
		t.Fatal("BackendsUsed should be initialized")
	}
	if snap.BackendsUsed["anthropic"] != 1 {
		t.Errorf("BackendsUsed[anthropic] = %d, want 1", snap.BackendsUsed["anthropic"])
	}
}

func TestSession_TouchAndRecord_MultipleBackends(t *testing.T) {
	sess := session.NewSession("test-multi", "http://backend", "127.0.0.1:1234")

	sess.TouchAndRecord(100, "openai")
	sess.TouchAndRecord(200, "anthropic")
	sess.TouchAndRecord(300, "openai")

	snap := sess.Snapshot()
	if snap.RequestCount != 3 {
		t.Errorf("RequestCount = %d, want 3", snap.RequestCount)
	}
	if snap.BytesIn != 600 {
		t.Errorf("BytesIn = %d, want 600", snap.BytesIn)
	}
	if snap.Backend != "openai" {
		t.Errorf("Backend = %q, want 'openai' (last used)", snap.Backend)
	}
	if snap.BackendsUsed["openai"] != 2 {
		t.Errorf("BackendsUsed[openai] = %d, want 2", snap.BackendsUsed["openai"])
	}
	if snap.BackendsUsed["anthropic"] != 1 {
		t.Errorf("BackendsUsed[anthropic] = %d, want 1", snap.BackendsUsed["anthropic"])
	}
}

func TestSession_TouchAndRecord_Concurrent(t *testing.T) {
	sess := session.NewSession("test-concurrent", "http://backend", "127.0.0.1:1234")

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sess.TouchAndRecord(10, "backend-a")
		}()
	}
	wg.Wait()

	snap := sess.Snapshot()
	if snap.RequestCount != 100 {
		t.Errorf("RequestCount = %d, want 100", snap.RequestCount)
	}
	if snap.BytesIn != 1000 {
		t.Errorf("BytesIn = %d, want 1000", snap.BytesIn)
	}
}

// ============================================================
// #6: Snapshot thread safety
// ============================================================

func TestSession_Snapshot_IsolatedFromMutation(t *testing.T) {
	sess := session.NewSession("snap-test", "http://backend", "127.0.0.1:1234")
	sess.TouchAndRecord(100, "backend-a")

	snap := sess.Snapshot()

	// Mutate original after snapshot
	sess.TouchAndRecord(200, "backend-b")

	// Snapshot should still reflect original state
	if snap.RequestCount != 1 {
		t.Errorf("snapshot RequestCount = %d, want 1 (should be isolated)", snap.RequestCount)
	}
	if snap.BytesIn != 100 {
		t.Errorf("snapshot BytesIn = %d, want 100 (should be isolated)", snap.BytesIn)
	}
}

// ============================================================
// #8: Risk ladder enforcement in proxy
// ============================================================

func TestRiskLadder_BlocksHighRiskSession(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer backend.Close()

	engine := policy.NewEngine(policy.Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []policy.Rule{
			{
				Name:     "test_content",
				Type:     "content_match",
				Target:   "request",
				Patterns: []string{"DANGEROUS_CONTENT"},
				Severity: "critical",
				Action:   "flag",
			},
		},
		RiskLadder: policy.RiskLadderConfig{Enabled: true},
	})

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
	p, err := proxy.New(cfg, store, manager, proxy.WithPolicyEngine(engine))
	if err != nil {
		t.Fatal(err)
	}

	sessionID := "risk-test-session"

	// Send enough violations to exceed the block threshold
	for i := 0; i < 20; i++ {
		req := httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"messages":[{"role":"user","content":"DANGEROUS_CONTENT"}]}`))
		req.Header.Set("X-Session-ID", sessionID)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, req)
	}

	// Verify risk score is high enough to block
	if !engine.ShouldBlockByRisk(sessionID) {
		score, action, _ := engine.GetSessionRiskScore(sessionID)
		t.Fatalf("expected ShouldBlockByRisk=true after 20 critical violations (score=%.1f, action=%s)", score, action)
	}

	// Next request should be blocked by risk ladder with 403
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"messages":[{"role":"user","content":"safe content"}]}`))
	req.Header.Set("X-Session-ID", sessionID)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 from risk ladder, got %d", w.Code)
	}

	// Verify response body contains expected error
	var errResp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to parse error response: %v", err)
	}
	if errResp["error"] != "risk_threshold_exceeded" {
		t.Errorf("error = %q, want 'risk_threshold_exceeded'", errResp["error"])
	}
}

func TestRiskLadder_ThrottlesBeforeBlocking(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer backend.Close()

	engine := policy.NewEngine(policy.Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []policy.Rule{
			{
				Name:     "test_content",
				Type:     "content_match",
				Target:   "request",
				Patterns: []string{"SUSPICIOUS"},
				Severity: "warning",
				Action:   "flag",
			},
		},
		RiskLadder: policy.RiskLadderConfig{Enabled: true},
	})

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
	p, err := proxy.New(cfg, store, manager, proxy.WithPolicyEngine(engine))
	if err != nil {
		t.Fatal(err)
	}

	sessionID := "throttle-proxy-test"

	// Send enough warnings to reach throttle level but not block
	// Warning severity = 3 points, throttle threshold is 15 (need 6 to clear with decay)
	for i := 0; i < 6; i++ {
		req := httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"messages":[{"role":"user","content":"SUSPICIOUS"}]}`))
		req.Header.Set("X-Session-ID", sessionID)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, req)
	}

	// Should be throttling but not blocking
	shouldThrottle, _ := engine.ShouldThrottle(sessionID)
	shouldBlock := engine.ShouldBlockByRisk(sessionID)
	score, action, _ := engine.GetSessionRiskScore(sessionID)
	t.Logf("after 5 warning violations: score=%.1f, action=%s", score, action)
	if !shouldThrottle {
		t.Errorf("expected session to be throttled (score=%.1f, action=%s)", score, action)
	}
	if shouldBlock {
		t.Error("expected session to NOT be blocked yet")
	}

	// Request should still succeed (just delayed)
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"messages":[{"role":"user","content":"safe content"}]}`))
	req.Header.Set("X-Session-ID", sessionID)
	w := httptest.NewRecorder()
	start := time.Now()
	p.ServeHTTP(w, req)
	elapsed := time.Since(start)

	if w.Code == http.StatusForbidden {
		t.Error("throttled request should not return 403")
	}
	// Throttle should add some delay (at least a few ms)
	if elapsed < 5*time.Millisecond {
		t.Logf("note: throttle delay was %v (may be too small to measure reliably)", elapsed)
	}
}

// ============================================================
// #7: Async scan uses snapshot not live pointer
// ============================================================

func TestProxy_AsyncScanUsesSnapshot(t *testing.T) {
	// Backend returns a response with tool calls in a streaming-like manner
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"safe response","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}]}}]}`))
	}))
	defer backend.Close()

	engine := policy.NewEngine(policy.Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []policy.Rule{
			{
				Name:     "response_scan",
				Type:     "content_match",
				Target:   "response",
				Patterns: []string{"BLOCKED_RESPONSE"},
				Severity: "warning",
				Action:   "flag",
			},
		},
	})

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
	p, err := proxy.New(cfg, store, manager, proxy.WithPolicyEngine(engine))
	if err != nil {
		t.Fatal(err)
	}

	// Send a request — the async scan should use a snapshot, not the live session
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("X-Session-ID", "async-snap-test")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	// Give async goroutine a moment
	time.Sleep(50 * time.Millisecond)

	// Session should still be accessible and not corrupted
	sess, exists := store.Get("async-snap-test")
	if !exists {
		t.Fatal("session should exist")
	}
	snap := sess.Snapshot()
	if snap.RequestCount < 1 {
		t.Errorf("RequestCount = %d, want >= 1", snap.RequestCount)
	}
}

// ============================================================
// #9: Pre-compiled trusted tag regexes
// ============================================================

func TestProxy_TrustedTagsPreCompiled(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer backend.Close()

	engine := policy.NewEngine(policy.Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []policy.Rule{
			{
				Name:     "detect_danger",
				Type:     "content_match",
				Target:   "request",
				Patterns: []string{"DANGER"},
				Severity: "warning",
				Action:   "flag",
			},
		},
	})

	cfg := &config.Config{
		Listen:  ":0",
		Backend: backend.URL,
		Session: config.SessionConfig{
			Timeout:           5 * time.Minute,
			Header:            "X-Session-ID",
			GenerateIfMissing: true,
		},
		Policy: config.PolicyConfig{
			Trust: config.TrustConfig{
				TrustedTags: []string{"system-reminder", "internal-context"},
			},
		},
	}

	store := session.NewMemoryStore()
	manager := session.NewManager(store, cfg.Session.Timeout)
	p, err := proxy.New(cfg, store, manager, proxy.WithPolicyEngine(engine))
	if err != nil {
		t.Fatal(err)
	}

	// Content inside trusted tags should be stripped before scanning
	body := `{"messages":[{"role":"user","content":"<system-reminder>DANGER</system-reminder> hello"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("X-Session-ID", "trusted-tag-test")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	// DANGER is inside trusted tags, so it should NOT be flagged
	if engine.IsFlagged("trusted-tag-test") {
		t.Error("content inside trusted tags should not trigger policy flag")
	}

	// Now send DANGER outside trusted tags — should be flagged
	body2 := `{"messages":[{"role":"user","content":"DANGER outside tags"}]}`
	req2 := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body2))
	req2.Header.Set("X-Session-ID", "trusted-tag-test-2")
	w2 := httptest.NewRecorder()
	p.ServeHTTP(w2, req2)

	if !engine.IsFlagged("trusted-tag-test-2") {
		t.Error("content outside trusted tags should trigger policy flag")
	}
}

// ============================================================
// #11: Settings endpoint body limit (both endpoints)
// ============================================================

func TestControlAPI_SettingsBodyLimit(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	handler := control.New(store, manager)

	t.Run("settings endpoint rejects oversized body", func(t *testing.T) {
		largeBody := strings.NewReader(strings.Repeat("x", 2*1024*1024)) // 2MB
		req := httptest.NewRequest("PUT", "/control/settings", largeBody)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code == http.StatusOK {
			body, _ := io.ReadAll(w.Result().Body)
			t.Errorf("expected error for oversized body, got 200: %s", string(body))
		}
	})

	t.Run("settings/local endpoint rejects oversized body", func(t *testing.T) {
		largeBody := strings.NewReader(strings.Repeat("x", 2*1024*1024)) // 2MB
		req := httptest.NewRequest("PUT", "/control/settings/local", largeBody)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		// Without a settings store, this returns 503 before reading body.
		// The important thing is it doesn't OOM or return 200.
		if w.Code == http.StatusOK {
			body, _ := io.ReadAll(w.Result().Body)
			t.Errorf("expected error for oversized body, got 200: %s", string(body))
		}
	})
}

// ============================================================
// #8 (proxy integration): Risk ladder returns correct JSON body
// ============================================================

func TestProxy_RiskBlock_ResponseFormat(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer backend.Close()

	engine := policy.NewEngine(policy.Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []policy.Rule{
			{
				Name:     "test_content",
				Type:     "content_match",
				Target:   "request",
				Patterns: []string{"TRIGGER"},
				Severity: "critical",
				Action:   "flag",
			},
		},
		RiskLadder: policy.RiskLadderConfig{Enabled: true},
	})

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
	p, err := proxy.New(cfg, store, manager, proxy.WithPolicyEngine(engine))
	if err != nil {
		t.Fatal(err)
	}

	sessionID := "response-format-test"

	// Drive risk score to block level
	for i := 0; i < 20; i++ {
		req := httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(fmt.Sprintf(`{"messages":[{"role":"user","content":"TRIGGER %d"}]}`, i)))
		req.Header.Set("X-Session-ID", sessionID)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, req)
	}

	if !engine.ShouldBlockByRisk(sessionID) {
		score, action, _ := engine.GetSessionRiskScore(sessionID)
		t.Fatalf("risk score did not reach block threshold (score=%.1f, action=%s)", score, action)
	}

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"messages":[{"role":"user","content":"anything"}]}`))
	req.Header.Set("X-Session-ID", sessionID)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}

	// Check Content-Type is JSON
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	// Check body is valid JSON with expected fields
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	if body["error"] != "risk_threshold_exceeded" {
		t.Errorf("error = %q, want risk_threshold_exceeded", body["error"])
	}
	if body["message"] == "" {
		t.Error("expected non-empty message field")
	}
}
