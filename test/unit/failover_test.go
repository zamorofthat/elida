package unit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"elida/internal/proxy"
	"elida/internal/session"
)

// =============================================================================
// Session State Tests
// =============================================================================

func TestSession_RecordMessage(t *testing.T) {
	sess := session.NewSession("test-123", "anthropic", "127.0.0.1")

	before := time.Now()
	sess.RecordMessage("user", "Hello", "anthropic")
	sess.RecordMessage("assistant", "Hi there!", "anthropic")
	sess.RecordMessage("user", "How are you?", "anthropic")
	after := time.Now()

	messages := sess.GetMessages()
	if len(messages) != 3 {
		t.Errorf("expected 3 messages, got %d", len(messages))
	}

	if messages[0].Role != "user" || messages[0].Content != "Hello" {
		t.Errorf("unexpected first message: %+v", messages[0])
	}
	if messages[1].Role != "assistant" || messages[1].Content != "Hi there!" {
		t.Errorf("unexpected second message: %+v", messages[1])
	}

	// Verify timestamps are set correctly
	for i, msg := range messages {
		if msg.Timestamp.Before(before) || msg.Timestamp.After(after) {
			t.Errorf("message %d has invalid timestamp: %v (expected between %v and %v)",
				i, msg.Timestamp, before, after)
		}
	}
}

func TestSession_RecordMessage_LimitsTo100(t *testing.T) {
	sess := session.NewSession("test-123", "anthropic", "127.0.0.1")

	// Add 105 messages
	for i := 0; i < 105; i++ {
		sess.RecordMessage("user", "message", "anthropic")
	}

	messages := sess.GetMessages()
	if len(messages) != 100 {
		t.Errorf("expected 100 messages (limited), got %d", len(messages))
	}
}

func TestSession_SetSystemPrompt(t *testing.T) {
	sess := session.NewSession("test-123", "anthropic", "127.0.0.1")

	sess.SetSystemPrompt("You are a helpful assistant.")

	state := sess.Serialize()
	if state.SystemPrompt != "You are a helpful assistant." {
		t.Errorf("unexpected system prompt: %s", state.SystemPrompt)
	}
}

func TestSession_AddFailedBackend(t *testing.T) {
	sess := session.NewSession("test-123", "anthropic", "127.0.0.1")

	sess.AddFailedBackend("anthropic")
	sess.AddFailedBackend("openai")
	sess.AddFailedBackend("anthropic") // Duplicate, should be ignored

	failed := sess.GetFailedBackends()
	if len(failed) != 2 {
		t.Errorf("expected 2 failed backends, got %d", len(failed))
	}
}

func TestSession_Serialize(t *testing.T) {
	sess := session.NewSession("test-123", "anthropic", "127.0.0.1")
	sess.SetSystemPrompt("You are helpful.")
	sess.RecordMessage("user", "Hello", "anthropic")
	sess.RecordMessage("assistant", "Hi!", "anthropic")
	sess.AddTokens(100, 50)
	sess.AddFailedBackend("openai")

	state := sess.Serialize()

	if state.ID != "test-123" {
		t.Errorf("unexpected ID: %s", state.ID)
	}
	if state.SystemPrompt != "You are helpful." {
		t.Errorf("unexpected system prompt: %s", state.SystemPrompt)
	}
	if len(state.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(state.Messages))
	}
	if state.TokensIn != 100 || state.TokensOut != 50 {
		t.Errorf("unexpected tokens: in=%d, out=%d", state.TokensIn, state.TokensOut)
	}
	if len(state.FailedBackends) != 1 || state.FailedBackends[0] != "openai" {
		t.Errorf("unexpected failed backends: %v", state.FailedBackends)
	}
	if state.CurrentBackend != "anthropic" {
		t.Errorf("unexpected current backend: %s", state.CurrentBackend)
	}
}

// =============================================================================
// Failure Detection Tests
// =============================================================================

func TestDetectFailure_NoError(t *testing.T) {
	resp := &http.Response{StatusCode: 200}
	failure := proxy.DetectFailure(resp, nil)
	if failure != proxy.FailureNone {
		t.Errorf("expected FailureNone, got %v", failure)
	}
}

func TestDetectFailure_ServerError(t *testing.T) {
	tests := []struct {
		statusCode int
		expected   proxy.FailureType
	}{
		{500, proxy.FailureServerError},
		{502, proxy.FailureServerError},
		{503, proxy.FailureServerError},
		{504, proxy.FailureServerError},
	}

	for _, tt := range tests {
		resp := &http.Response{StatusCode: tt.statusCode}
		failure := proxy.DetectFailure(resp, nil)
		if failure != tt.expected {
			t.Errorf("status %d: expected %v, got %v", tt.statusCode, tt.expected, failure)
		}
	}
}

func TestDetectFailure_RateLimit(t *testing.T) {
	// 429 without Retry-After
	resp := &http.Response{
		StatusCode: 429,
		Header:     http.Header{},
	}
	failure := proxy.DetectFailure(resp, nil)
	if failure != proxy.FailureRateLimit {
		t.Errorf("expected FailureRateLimit, got %v", failure)
	}

	// 429 with Retry-After should not be treated as hard failure
	resp.Header.Set("Retry-After", "60")
	failure = proxy.DetectFailure(resp, nil)
	if failure != proxy.FailureNone {
		t.Errorf("expected FailureNone (retryable), got %v", failure)
	}
}

func TestDetectFailure_Timeout(t *testing.T) {
	err := context.DeadlineExceeded
	failure := proxy.DetectFailure(nil, err)
	if failure != proxy.FailureTimeout {
		t.Errorf("expected FailureTimeout, got %v", failure)
	}
}

func TestDetectFailure_ConnectionRefused(t *testing.T) {
	err := errors.New("dial tcp: connection refused")
	failure := proxy.DetectFailure(nil, err)
	if failure != proxy.FailureConnectionRefused {
		t.Errorf("expected FailureConnectionRefused, got %v", failure)
	}
}

func TestDetectFailure_EOF(t *testing.T) {
	err := errors.New("unexpected EOF")
	failure := proxy.DetectFailure(nil, err)
	if failure != proxy.FailureStreamInterrupt {
		t.Errorf("expected FailureStreamInterrupt, got %v", failure)
	}
}

// =============================================================================
// Failover Controller Tests
// =============================================================================

func TestFailoverController_SelectFallback(t *testing.T) {
	cfg := proxy.FailoverConfig{
		Enabled:    true,
		MaxRetries: 2,
	}
	fc := proxy.NewFailoverController(cfg)
	fc.RegisterBackend("anthropic", "https://api.anthropic.com", "anthropic", 1)
	fc.RegisterBackend("openai", "https://api.openai.com", "openai", 2)
	fc.RegisterBackend("ollama", "http://localhost:11434", "ollama", 3)

	sess := session.NewSession("test-123", "anthropic", "127.0.0.1")

	// First failover: anthropic fails, should select openai (priority 2)
	// Note: In real usage, HandleFailover calls AddFailedBackend before SelectFallback
	sess.AddFailedBackend("anthropic")
	fallback, err := fc.SelectFallback(sess, "anthropic")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fallback.Name != "openai" {
		t.Errorf("expected openai, got %s", fallback.Name)
	}

	// Second failover: openai also fails
	sess.AddFailedBackend("openai")
	fallback, err = fc.SelectFallback(sess, "openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fallback.Name != "ollama" {
		t.Errorf("expected ollama (only remaining), got %s", fallback.Name)
	}
}

func TestFailoverController_SelectFallback_NoBackends(t *testing.T) {
	cfg := proxy.FailoverConfig{Enabled: true}
	fc := proxy.NewFailoverController(cfg)
	fc.RegisterBackend("anthropic", "https://api.anthropic.com", "anthropic", 1)

	sess := session.NewSession("test-123", "anthropic", "127.0.0.1")

	// Only backend fails, no fallback available
	_, err := fc.SelectFallback(sess, "anthropic")
	if err == nil {
		t.Error("expected error when no fallback available")
	}
}

func TestFailoverController_SelectFallback_ExplicitOrder(t *testing.T) {
	cfg := proxy.FailoverConfig{
		Enabled:       true,
		FallbackOrder: []string{"ollama", "openai"}, // Explicit order
	}
	fc := proxy.NewFailoverController(cfg)
	fc.RegisterBackend("anthropic", "https://api.anthropic.com", "anthropic", 1)
	fc.RegisterBackend("openai", "https://api.openai.com", "openai", 2)
	fc.RegisterBackend("ollama", "http://localhost:11434", "ollama", 3)

	sess := session.NewSession("test-123", "anthropic", "127.0.0.1")

	// Should follow explicit order: ollama first
	fallback, err := fc.SelectFallback(sess, "anthropic")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fallback.Name != "ollama" {
		t.Errorf("expected ollama (explicit order), got %s", fallback.Name)
	}
}

func TestFailoverController_HandleFailover(t *testing.T) {
	cfg := proxy.FailoverConfig{
		Enabled:    true,
		MaxRetries: 2,
		RetryDelay: 0, // No delay for tests
	}
	fc := proxy.NewFailoverController(cfg)
	fc.RegisterBackend("anthropic", "https://api.anthropic.com", "anthropic", 1)
	fc.RegisterBackend("openai", "https://api.openai.com", "openai", 2)

	sess := session.NewSession("test-123", "anthropic", "127.0.0.1")
	sess.RecordMessage("user", "Hello", "anthropic")

	ctx := context.Background()
	fallback, err := fc.HandleFailover(ctx, sess, "anthropic", proxy.FailureServerError)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fallback.Name != "openai" {
		t.Errorf("expected openai, got %s", fallback.Name)
	}

	// Verify failed backend was recorded
	failed := sess.GetFailedBackends()
	if len(failed) != 1 || failed[0] != "anthropic" {
		t.Errorf("unexpected failed backends: %v", failed)
	}
}

func TestFailoverController_HandleFailover_MaxRetries(t *testing.T) {
	cfg := proxy.FailoverConfig{
		Enabled:    true,
		MaxRetries: 1,
	}
	fc := proxy.NewFailoverController(cfg)
	fc.RegisterBackend("anthropic", "https://api.anthropic.com", "anthropic", 1)
	fc.RegisterBackend("openai", "https://api.openai.com", "openai", 2)
	fc.RegisterBackend("ollama", "http://localhost:11434", "ollama", 3)

	sess := session.NewSession("test-123", "anthropic", "127.0.0.1")
	sess.AddFailedBackend("anthropic")
	sess.AddFailedBackend("openai") // Already at max retries

	ctx := context.Background()
	_, err := fc.HandleFailover(ctx, sess, "ollama", proxy.FailureServerError)
	if err == nil {
		t.Error("expected error when max retries exceeded")
	}
}

func TestFailoverController_Disabled(t *testing.T) {
	cfg := proxy.FailoverConfig{Enabled: false}
	fc := proxy.NewFailoverController(cfg)

	sess := session.NewSession("test-123", "anthropic", "127.0.0.1")

	ctx := context.Background()
	_, err := fc.HandleFailover(ctx, sess, "anthropic", proxy.FailureServerError)
	if err == nil {
		t.Error("expected error when failover disabled")
	}
}

// =============================================================================
// Rehydrator Tests
// =============================================================================

func TestOpenAIRehydrator(t *testing.T) {
	state := &session.SessionState{
		ID:           "test-123",
		SystemPrompt: "You are helpful.",
		Messages: []session.Message{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi!"},
			{Role: "user", Content: "How are you?"},
		},
	}

	originalBody := `{"model":"gpt-4","messages":[{"role":"user","content":"How are you?"}]}`
	originalReq := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(originalBody))
	originalReq.Header.Set("Content-Type", "application/json")

	rehydrator := &proxy.OpenAIRehydrator{}
	req, err := rehydrator.Rehydrate(state, originalReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read and verify body
	body, _ := io.ReadAll(req.Body)
	bodyStr := string(body)

	// Should contain system message + all conversation messages
	if !bytes.Contains(body, []byte(`"role":"system"`)) {
		t.Error("expected system message in rehydrated request")
	}
	if !bytes.Contains(body, []byte(`"content":"Hello"`)) {
		t.Error("expected first user message")
	}
	if !bytes.Contains(body, []byte(`"content":"Hi!"`)) {
		t.Error("expected assistant message")
	}
	if !bytes.Contains(body, []byte(`"stream":true`)) {
		t.Error("expected stream:true")
	}
	t.Logf("Rehydrated body: %s", bodyStr)
}

func TestAnthropicRehydrator(t *testing.T) {
	state := &session.SessionState{
		ID:           "test-123",
		SystemPrompt: "You are helpful.",
		Messages: []session.Message{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi!"},
		},
	}

	originalBody := `{"model":"claude-3-sonnet-20240229","max_tokens":1024}`
	originalReq := httptest.NewRequest("POST", "/v1/messages", bytes.NewBufferString(originalBody))
	originalReq.Header.Set("Content-Type", "application/json")

	rehydrator := &proxy.AnthropicRehydrator{}
	req, err := rehydrator.Rehydrate(state, originalReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	body, _ := io.ReadAll(req.Body)
	bodyStr := string(body)

	// System should be separate field, not in messages
	if !bytes.Contains(body, []byte(`"system":"You are helpful."`)) {
		t.Error("expected system as separate field")
	}
	if !bytes.Contains(body, []byte(`"max_tokens"`)) {
		t.Error("expected max_tokens")
	}
	t.Logf("Rehydrated body: %s", bodyStr)
}

func TestSelectCompatibleModel(t *testing.T) {
	tests := []struct {
		original string
		target   string
		expected string
	}{
		{"claude-3-opus-20240229", "openai", "gpt-4"},
		{"gpt-4", "anthropic", "claude-3-opus-20240229"},
		{"gpt-3.5-turbo", "anthropic", "claude-3-haiku-20240307"},
		{"unknown-model", "openai", "gpt-4"}, // Falls back to default
		{"unknown-model", "anthropic", "claude-3-sonnet-20240229"},
	}

	for _, tt := range tests {
		result := proxy.SelectCompatibleModel(tt.original, tt.target)
		if result != tt.expected {
			t.Errorf("%s → %s: expected %s, got %s", tt.original, tt.target, tt.expected, result)
		}
	}
}

func TestGetRehydrator(t *testing.T) {
	tests := []struct {
		backendType string
		expected    string
	}{
		{"openai", "openai"},
		{"anthropic", "anthropic"},
		{"ollama", "ollama"},
		{"unknown", "openai"}, // Defaults to OpenAI
	}

	for _, tt := range tests {
		rehydrator := proxy.GetRehydrator(tt.backendType)
		if rehydrator.BackendType() != tt.expected {
			t.Errorf("GetRehydrator(%s): expected %s, got %s", tt.backendType, tt.expected, rehydrator.BackendType())
		}
	}
}

// =============================================================================
// Integration-style Tests
// =============================================================================

func TestFailover_FullFlow(t *testing.T) {
	// Simulate full failover flow:
	// 1. Session starts on Anthropic
	// 2. User sends messages
	// 3. Anthropic fails
	// 4. Failover to OpenAI with context

	sess := session.NewSession("flow-test", "anthropic", "127.0.0.1")
	sess.SetSystemPrompt("You are a helpful coding assistant.")
	sess.RecordMessage("user", "Write a function to add two numbers", "anthropic")
	sess.RecordMessage("assistant", "Here's a Python function:\n```python\ndef add(a, b):\n    return a + b\n```", "anthropic")
	sess.RecordMessage("user", "Now make it handle floats too", "anthropic")

	// Simulate failure
	cfg := proxy.FailoverConfig{
		Enabled:    true,
		MaxRetries: 2,
	}
	fc := proxy.NewFailoverController(cfg)
	fc.RegisterBackend("anthropic", "https://api.anthropic.com", "anthropic", 1)
	fc.RegisterBackend("openai", "https://api.openai.com", "openai", 2)

	ctx := context.Background()
	fallback, err := fc.HandleFailover(ctx, sess, "anthropic", proxy.FailureServerError)
	if err != nil {
		t.Fatalf("failover failed: %v", err)
	}
	if fallback.Name != "openai" {
		t.Errorf("expected openai, got %s", fallback.Name)
	}

	// Verify session state is ready for rehydration
	state := sess.Serialize()
	if len(state.Messages) != 3 {
		t.Errorf("expected 3 messages preserved, got %d", len(state.Messages))
	}
	if state.SystemPrompt == "" {
		t.Error("system prompt should be preserved")
	}

	// Rehydrate for OpenAI
	originalReq := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-4"}`))
	rehydrator := proxy.GetRehydrator(fallback.Type)
	req, err := rehydrator.Rehydrate(state, originalReq)
	if err != nil {
		t.Fatalf("rehydration failed: %v", err)
	}

	body, _ := io.ReadAll(req.Body)
	// Verify all context is in the rehydrated request
	if !bytes.Contains(body, []byte("Write a function")) {
		t.Error("first user message not in rehydrated request")
	}
	if !bytes.Contains(body, []byte("def add")) {
		t.Error("assistant response not in rehydrated request")
	}
	if !bytes.Contains(body, []byte("handle floats")) {
		t.Error("latest user message not in rehydrated request")
	}
}

func TestSession_Snapshot_IncludesNewFields(t *testing.T) {
	sess := session.NewSession("snap-test", "anthropic", "127.0.0.1")
	sess.SetSystemPrompt("System prompt")
	sess.RecordMessage("user", "Hello", "anthropic")
	sess.AddFailedBackend("openai")

	snap := sess.Snapshot()

	if snap.SystemPrompt != "System prompt" {
		t.Errorf("snapshot missing SystemPrompt: %s", snap.SystemPrompt)
	}
	if len(snap.Messages) != 1 {
		t.Errorf("snapshot missing Messages: %d", len(snap.Messages))
	}
	if len(snap.FailedBackends) != 1 {
		t.Errorf("snapshot missing FailedBackends: %d", len(snap.FailedBackends))
	}
}

func TestFailureType_String(t *testing.T) {
	tests := []struct {
		failure  proxy.FailureType
		expected string
	}{
		{proxy.FailureNone, "none"},
		{proxy.FailureTimeout, "timeout"},
		{proxy.FailureServerError, "server_error"},
		{proxy.FailureConnectionRefused, "connection_refused"},
	}

	for _, tt := range tests {
		if tt.failure.String() != tt.expected {
			t.Errorf("FailureType %d: expected %s, got %s", tt.failure, tt.expected, tt.failure.String())
		}
	}
}
