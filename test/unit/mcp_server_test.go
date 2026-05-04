package unit

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/mcp"
	"elida/internal/policy"
	"elida/internal/session"
)

func newMCPServer() (*mcp.Server, *session.Manager) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	cfg := config.MCPConfig{
		Enabled:      true,
		AntiSelfKill: true,
		RateLimit:    config.MCPRateLimitConfig{RequestsPerMinute: 60},
		Approval:     config.MCPApprovalConfig{Enabled: false},
		Format:       "json",
		Audit:        true,
		Auth: config.MCPAuthConfig{
			Tokens: []config.MCPTokenConfig{
				{Name: "reader", Key: "read-token-123", Scope: "read"},
				{Name: "writer", Key: "write-token-456", Scope: "write"},
				{Name: "admin", Key: "admin-token-789", Scope: "admin"},
			},
		},
	}
	server := mcp.New(cfg, store, manager)
	return server, manager
}

func mcpCall(server *mcp.Server, token, method string, params any) *httptest.ResponseRecorder {
	var body bytes.Buffer
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"id":      1,
	}
	if params != nil {
		p, _ := json.Marshal(params)
		req["params"] = json.RawMessage(p)
	}
	json.NewEncoder(&body).Encode(req)

	r := httptest.NewRequest("POST", "/mcp", &body)
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	return w
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
	ID any `json:"id"`
}

func decodeRPC(t *testing.T, w *httptest.ResponseRecorder) rpcResponse {
	t.Helper()
	var resp rpcResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode RPC response: %v", err)
	}
	return resp
}

func TestMCP_Initialize(t *testing.T) {
	server, _ := newMCPServer()
	w := mcpCall(server, "", "initialize", nil)
	resp := decodeRPC(t, w)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error.Message)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("unexpected protocol version: %v", result["protocolVersion"])
	}
}

func TestMCP_ToolsList_RequiresAuth(t *testing.T) {
	server, _ := newMCPServer()
	w := mcpCall(server, "", "tools/list", nil)
	resp := decodeRPC(t, w)

	if resp.Error == nil {
		t.Fatal("expected auth error")
	}
	if resp.Error.Code != -32001 {
		t.Errorf("expected unauthorized error code, got %d", resp.Error.Code)
	}
}

func TestMCP_ToolsList_ReadToken(t *testing.T) {
	server, _ := newMCPServer()
	w := mcpCall(server, "read-token-123", "tools/list", nil)
	resp := decodeRPC(t, w)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error.Message)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	tools, ok := result["tools"].([]any)
	if !ok {
		t.Fatal("expected tools array")
	}

	// Read token should only see read-scoped tools
	for _, tool := range tools {
		tm := tool.(map[string]any)
		name := tm["name"].(string)
		// Should not see write/admin tools
		if name == "elida_kill_session" || name == "elida_terminate_session" || name == "elida_update_settings" {
			t.Errorf("read token should not see tool %s", name)
		}
	}
}

func TestMCP_ToolsList_AdminToken(t *testing.T) {
	server, _ := newMCPServer()
	w := mcpCall(server, "admin-token-789", "tools/list", nil)
	resp := decodeRPC(t, w)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error.Message)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	tools := result["tools"].([]any)

	// Admin should see all tools
	if len(tools) < 10 {
		t.Errorf("admin should see all tools, got %d", len(tools))
	}
}

func TestMCP_GetStats(t *testing.T) {
	server, manager := newMCPServer()

	// Create some sessions
	manager.GetOrCreate("sess-1", "backend1", "10.0.0.1:1234")
	manager.GetOrCreate("sess-2", "backend1", "10.0.0.2:1234")

	w := mcpCall(server, "read-token-123", "tools/call", map[string]any{
		"name":      "elida_get_stats",
		"arguments": map[string]any{},
	})
	resp := decodeRPC(t, w)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error.Message)
	}
}

func TestMCP_ListSessions(t *testing.T) {
	server, manager := newMCPServer()
	manager.GetOrCreate("sess-a", "backend1", "10.0.0.1:1234")
	manager.GetOrCreate("sess-b", "backend1", "10.0.0.2:1234")

	w := mcpCall(server, "read-token-123", "tools/call", map[string]any{
		"name":      "elida_list_sessions",
		"arguments": map[string]any{"state": "active", "limit": 10},
	})
	resp := decodeRPC(t, w)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error.Message)
	}
}

func TestMCP_GetSession(t *testing.T) {
	server, manager := newMCPServer()
	manager.GetOrCreate("sess-detail", "backend1", "10.0.0.1:1234")

	w := mcpCall(server, "read-token-123", "tools/call", map[string]any{
		"name":      "elida_get_session",
		"arguments": map[string]any{"session_id": "sess-detail"},
	})
	resp := decodeRPC(t, w)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error.Message)
	}
}

func TestMCP_GetSession_NotFound(t *testing.T) {
	server, _ := newMCPServer()

	w := mcpCall(server, "read-token-123", "tools/call", map[string]any{
		"name":      "elida_get_session",
		"arguments": map[string]any{"session_id": "nonexistent"},
	})
	resp := decodeRPC(t, w)
	if resp.Error == nil {
		t.Fatal("expected error for nonexistent session")
	}
	if resp.Error.Code != -32004 {
		t.Errorf("expected not found error code, got %d", resp.Error.Code)
	}
}

func TestMCP_KillSession_ReadTokenDenied(t *testing.T) {
	server, manager := newMCPServer()
	manager.GetOrCreate("sess-kill", "backend1", "10.0.0.1:1234")

	w := mcpCall(server, "read-token-123", "tools/call", map[string]any{
		"name":      "elida_kill_session",
		"arguments": map[string]any{"session_id": "sess-kill"},
	})
	resp := decodeRPC(t, w)
	if resp.Error == nil {
		t.Fatal("expected permission error")
	}
	if resp.Error.Code != -32003 {
		t.Errorf("expected forbidden error code, got %d", resp.Error.Code)
	}
}

func TestMCP_KillSession_WriteToken(t *testing.T) {
	server, manager := newMCPServer()
	manager.GetOrCreate("sess-kill2", "backend1", "10.0.0.1:1234")

	w := mcpCall(server, "write-token-456", "tools/call", map[string]any{
		"name":      "elida_kill_session",
		"arguments": map[string]any{"session_id": "sess-kill2", "reason": "test"},
	})
	resp := decodeRPC(t, w)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error.Message)
	}

	// Verify session is killed
	sess, ok := manager.Get("sess-kill2")
	if !ok {
		t.Fatal("session should still exist")
	}
	if sess.GetState() != session.Killed {
		t.Errorf("expected killed state, got %v", sess.GetState())
	}
}

func TestMCP_ResumeSession(t *testing.T) {
	server, manager := newMCPServer()
	manager.GetOrCreate("sess-resume", "backend1", "10.0.0.1:1234")
	manager.Kill("sess-resume")

	w := mcpCall(server, "write-token-456", "tools/call", map[string]any{
		"name":      "elida_resume_session",
		"arguments": map[string]any{"session_id": "sess-resume"},
	})
	resp := decodeRPC(t, w)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error.Message)
	}

	sess, _ := manager.Get("sess-resume")
	if sess.GetState() != session.Active {
		t.Errorf("expected active state after resume, got %v", sess.GetState())
	}
}

func TestMCP_TerminateSession_WriteTokenDenied(t *testing.T) {
	server, manager := newMCPServer()
	manager.GetOrCreate("sess-term", "backend1", "10.0.0.1:1234")

	w := mcpCall(server, "write-token-456", "tools/call", map[string]any{
		"name":      "elida_terminate_session",
		"arguments": map[string]any{"session_id": "sess-term"},
	})
	resp := decodeRPC(t, w)
	if resp.Error == nil {
		t.Fatal("expected permission error for write token calling admin tool")
	}
	if resp.Error.Code != -32003 {
		t.Errorf("expected forbidden, got %d", resp.Error.Code)
	}
}

func TestMCP_TerminateSession_AdminToken(t *testing.T) {
	server, manager := newMCPServer()
	manager.GetOrCreate("sess-term2", "backend1", "10.0.0.1:1234")

	w := mcpCall(server, "admin-token-789", "tools/call", map[string]any{
		"name":      "elida_terminate_session",
		"arguments": map[string]any{"session_id": "sess-term2"},
	})
	resp := decodeRPC(t, w)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error.Message)
	}

	sess, ok := manager.Get("sess-term2")
	if !ok {
		t.Fatal("session should still exist")
	}
	if !sess.IsTerminated() {
		t.Error("expected session to be terminated")
	}
}

func TestMCP_AntiSelfKill_ExplicitHeader(t *testing.T) {
	server, manager := newMCPServer()
	manager.GetOrCreate("my-session", "backend1", "10.0.0.1:1234")

	var body bytes.Buffer
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"id":      1,
		"params": map[string]any{
			"name":      "elida_kill_session",
			"arguments": map[string]any{"session_id": "my-session"},
		},
	}
	json.NewEncoder(&body).Encode(req)

	r := httptest.NewRequest("POST", "/mcp", &body)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer write-token-456")
	r.Header.Set("X-Elida-Session-ID", "my-session")

	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)

	resp := decodeRPC(t, w)
	if resp.Error == nil {
		t.Fatal("expected anti-self-kill error")
	}
	if resp.Error.Code != -32006 {
		t.Errorf("expected anti-self-kill error code, got %d", resp.Error.Code)
	}
}

func TestMCP_InvalidToken(t *testing.T) {
	server, _ := newMCPServer()
	w := mcpCall(server, "bad-token", "tools/list", nil)
	resp := decodeRPC(t, w)
	if resp.Error == nil {
		t.Fatal("expected auth error")
	}
	if resp.Error.Code != -32001 {
		t.Errorf("expected unauthorized error, got %d", resp.Error.Code)
	}
}

func TestMCP_RateLimit(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	cfg := config.MCPConfig{
		Enabled:   true,
		RateLimit: config.MCPRateLimitConfig{RequestsPerMinute: 3},
		Format:    "json",
		Auth: config.MCPAuthConfig{
			Tokens: []config.MCPTokenConfig{
				{Name: "limited", Key: "limit-token", Scope: "read"},
			},
		},
	}
	server := mcp.New(cfg, store, manager)

	for i := 0; i < 3; i++ {
		w := mcpCall(server, "limit-token", "tools/list", nil)
		resp := decodeRPC(t, w)
		if resp.Error != nil {
			t.Fatalf("request %d should succeed: %v", i+1, resp.Error.Message)
		}
	}

	// 4th should be rate limited
	w := mcpCall(server, "limit-token", "tools/list", nil)
	resp := decodeRPC(t, w)
	if resp.Error == nil {
		t.Fatal("expected rate limit error")
	}
	if resp.Error.Code != -32005 {
		t.Errorf("expected rate limit error code, got %d", resp.Error.Code)
	}
}

func TestMCP_GetViolations_WithPolicy(t *testing.T) {
	server, manager := newMCPServer()

	engine := policy.NewEngine(policy.Config{
		Enabled:        true,
		Mode:           "enforce",
		CaptureContent: true,
		Rules: []policy.Rule{
			{
				Name:      "test-rate",
				Type:      policy.RuleTypeRequestCount,
				Threshold: 1,
				Severity:  policy.SeverityWarning,
			},
		},
	})
	server.SetPolicyEngine(engine)

	sess := manager.GetOrCreate("violated-sess", "backend1", "10.0.0.1:1234")
	for i := 0; i < 100; i++ {
		sess.Touch()
	}

	// Evaluate to trigger violation
	engine.Evaluate(policy.SessionMetrics{
		SessionID:    "violated-sess",
		RequestCount: 100,
		Duration:     time.Minute,
		StartTime:    time.Now().Add(-time.Minute),
	})

	w := mcpCall(server, "read-token-123", "tools/call", map[string]any{
		"name":      "elida_get_violations",
		"arguments": map[string]any{},
	})
	resp := decodeRPC(t, w)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error.Message)
	}
}

func TestMCP_MethodNotAllowed(t *testing.T) {
	server, _ := newMCPServer()

	r := httptest.NewRequest("GET", "/mcp", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)

	resp := decodeRPC(t, w)
	if resp.Error == nil {
		t.Fatal("expected error for GET method")
	}
}

func TestMCP_UnknownMethod(t *testing.T) {
	server, _ := newMCPServer()
	w := mcpCall(server, "read-token-123", "unknown/method", nil)
	resp := decodeRPC(t, w)
	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("expected method not found error, got %d", resp.Error.Code)
	}
}

func TestMCP_Approval_Enabled(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	cfg := config.MCPConfig{
		Enabled:      true,
		AntiSelfKill: false,
		Format:       "json",
		Approval: config.MCPApprovalConfig{
			Enabled:    true,
			RequireFor: []string{"terminate"},
		},
		Auth: config.MCPAuthConfig{
			Tokens: []config.MCPTokenConfig{
				{Name: "admin", Key: "admin-tok", Scope: "admin"},
			},
		},
	}
	server := mcp.New(cfg, store, manager)
	manager.GetOrCreate("approve-sess", "backend1", "10.0.0.1:1234")

	// Terminate should return pending_approval
	w := mcpCall(server, "admin-tok", "tools/call", map[string]any{
		"name":      "elida_terminate_session",
		"arguments": map[string]any{"session_id": "approve-sess"},
	})
	resp := decodeRPC(t, w)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error.Message)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)

	// Check content[0].text contains pending_approval
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatal("expected content in response")
	}
	textObj := content[0].(map[string]any)
	text := textObj["text"].(string)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("failed to parse result text: %v", err)
	}
	if parsed["status"] != "pending_approval" {
		t.Errorf("expected pending_approval status, got %v", parsed["status"])
	}
	if parsed["approval_id"] == nil || parsed["approval_id"] == "" {
		t.Error("expected approval_id in response")
	}

	// Session should still be active
	sess, _ := manager.Get("approve-sess")
	if sess.GetState() != session.Active {
		t.Errorf("session should still be active, got %v", sess.GetState())
	}
}

func TestMCP_GetOutliers(t *testing.T) {
	server, manager := newMCPServer()
	s1 := manager.GetOrCreate("out-1", "backend1", "10.0.0.1:1234")
	for i := 0; i < 50; i++ {
		s1.Touch()
	}
	s2 := manager.GetOrCreate("out-2", "backend1", "10.0.0.2:1234")
	for i := 0; i < 200; i++ {
		s2.Touch()
	}

	w := mcpCall(server, "read-token-123", "tools/call", map[string]any{
		"name":      "elida_get_outliers",
		"arguments": map[string]any{"top_n": 1, "metric": "requests"},
	})
	resp := decodeRPC(t, w)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error.Message)
	}
}

func TestMCP_GetTimeline(t *testing.T) {
	server, manager := newMCPServer()
	sess := manager.GetOrCreate("timeline-sess", "backend1", "10.0.0.1:1234")
	sess.RecordToolCall("read_file", "function", "req-1", `{"path":"/tmp/a"}`)

	w := mcpCall(server, "read-token-123", "tools/call", map[string]any{
		"name":      "elida_get_timeline",
		"arguments": map[string]any{"session_id": "timeline-sess"},
	})
	resp := decodeRPC(t, w)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error.Message)
	}
}
