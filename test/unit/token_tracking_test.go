package unit

import (
	"testing"
	"time"

	"elida/internal/proxy"
	"elida/internal/session"
)

func TestSession_AddTokens(t *testing.T) {
	sess := session.NewSession("test-1", "http://backend", "127.0.0.1")

	// Add tokens
	sess.AddTokens(100, 50)
	in, out := sess.GetTokens()
	if in != 100 || out != 50 {
		t.Errorf("expected (100, 50), got (%d, %d)", in, out)
	}

	// Add more tokens
	sess.AddTokens(200, 100)
	in, out = sess.GetTokens()
	if in != 300 || out != 150 {
		t.Errorf("expected (300, 150), got (%d, %d)", in, out)
	}
}

func TestSession_RecordToolCall(t *testing.T) {
	sess := session.NewSession("test-2", "http://backend", "127.0.0.1")

	// Record tool calls
	sess.RecordToolCall("get_weather", "function", "req-1")
	sess.RecordToolCall("search_web", "function", "req-2")
	sess.RecordToolCall("get_weather", "function", "req-3") // Duplicate

	// Check total count
	if sess.GetToolCalls() != 3 {
		t.Errorf("expected 3 tool calls, got %d", sess.GetToolCalls())
	}

	// Check fanout (distinct tools)
	if sess.GetToolFanout() != 2 {
		t.Errorf("expected fanout of 2, got %d", sess.GetToolFanout())
	}

	// Check per-tool counts
	counts := sess.GetToolCallCounts()
	if counts["get_weather"] != 2 {
		t.Errorf("expected 2 get_weather calls, got %d", counts["get_weather"])
	}
	if counts["search_web"] != 1 {
		t.Errorf("expected 1 search_web call, got %d", counts["search_web"])
	}
}

func TestSession_ToolCallHistory(t *testing.T) {
	sess := session.NewSession("test-3", "http://backend", "127.0.0.1")

	sess.RecordToolCall("tool_a", "function", "req-1")
	time.Sleep(10 * time.Millisecond) // Ensure different timestamps
	sess.RecordToolCall("tool_b", "code_interpreter", "req-2")

	history := sess.GetToolCallHistory()
	if len(history) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(history))
	}

	if history[0].ToolName != "tool_a" {
		t.Errorf("expected first tool to be tool_a, got %s", history[0].ToolName)
	}
	if history[1].ToolType != "code_interpreter" {
		t.Errorf("expected second tool type to be code_interpreter, got %s", history[1].ToolType)
	}

	// Verify timestamps are ordered
	if !history[0].Timestamp.Before(history[1].Timestamp) {
		t.Error("expected timestamps to be in order")
	}
}

func TestSession_ToolCallHistoryLimit(t *testing.T) {
	sess := session.NewSession("test-4", "http://backend", "127.0.0.1")

	// Record 150 tool calls (should keep only last 100)
	for i := 0; i < 150; i++ {
		sess.RecordToolCall("tool", "function", "")
	}

	history := sess.GetToolCallHistory()
	if len(history) != 100 {
		t.Errorf("expected history limited to 100, got %d", len(history))
	}
}

func TestExtractTokenUsage_OpenAI(t *testing.T) {
	body := []byte(`{
		"id": "chatcmpl-123",
		"object": "chat.completion",
		"usage": {
			"prompt_tokens": 100,
			"completion_tokens": 50,
			"total_tokens": 150
		}
	}`)

	usage := proxy.ExtractTokenUsage(body)
	if usage == nil {
		t.Fatal("expected token usage, got nil")
	}
	if usage.PromptTokens != 100 {
		t.Errorf("expected prompt_tokens=100, got %d", usage.PromptTokens)
	}
	if usage.CompletionTokens != 50 {
		t.Errorf("expected completion_tokens=50, got %d", usage.CompletionTokens)
	}
	if usage.TotalTokens != 150 {
		t.Errorf("expected total_tokens=150, got %d", usage.TotalTokens)
	}
}

func TestExtractTokenUsage_Anthropic(t *testing.T) {
	body := []byte(`{
		"id": "msg_123",
		"type": "message",
		"usage": {
			"input_tokens": 200,
			"output_tokens": 100
		}
	}`)

	usage := proxy.ExtractTokenUsage(body)
	if usage == nil {
		t.Fatal("expected token usage, got nil")
	}
	if usage.PromptTokens != 200 {
		t.Errorf("expected prompt_tokens=200, got %d", usage.PromptTokens)
	}
	if usage.CompletionTokens != 100 {
		t.Errorf("expected completion_tokens=100, got %d", usage.CompletionTokens)
	}
	if usage.TotalTokens != 300 {
		t.Errorf("expected total_tokens=300, got %d", usage.TotalTokens)
	}
}

func TestExtractTokenUsage_Ollama(t *testing.T) {
	body := []byte(`{
		"model": "llama2",
		"prompt_eval_count": 50,
		"eval_count": 75
	}`)

	usage := proxy.ExtractTokenUsage(body)
	if usage == nil {
		t.Fatal("expected token usage, got nil")
	}
	if usage.PromptTokens != 50 {
		t.Errorf("expected prompt_tokens=50, got %d", usage.PromptTokens)
	}
	if usage.CompletionTokens != 75 {
		t.Errorf("expected completion_tokens=75, got %d", usage.CompletionTokens)
	}
	if usage.TotalTokens != 125 {
		t.Errorf("expected total_tokens=125, got %d", usage.TotalTokens)
	}
}

func TestExtractTokenUsage_NoUsage(t *testing.T) {
	body := []byte(`{"message": "Hello"}`)
	usage := proxy.ExtractTokenUsage(body)
	if usage != nil {
		t.Errorf("expected nil for response without usage, got %+v", usage)
	}
}

func TestExtractTokenUsage_Empty(t *testing.T) {
	usage := proxy.ExtractTokenUsage([]byte{})
	if usage != nil {
		t.Error("expected nil for empty body")
	}
}

func TestExtractToolCalls_OpenAIRequest(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4",
		"messages": [
			{
				"role": "assistant",
				"tool_calls": [
					{
						"id": "call_123",
						"type": "function",
						"function": {
							"name": "get_weather",
							"arguments": "{\"city\": \"London\"}"
						}
					}
				]
			}
		]
	}`)

	tools := proxy.ExtractToolCalls(body)
	if len(tools) == 0 {
		t.Fatal("expected tool calls, got none")
	}
	if tools[0].Name != "get_weather" {
		t.Errorf("expected get_weather, got %s", tools[0].Name)
	}
	if tools[0].Type != "function" {
		t.Errorf("expected function type, got %s", tools[0].Type)
	}
}

func TestExtractToolCalls_OpenAIToolsDef(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4",
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "search_database",
					"description": "Search the database"
				}
			},
			{
				"type": "function",
				"function": {
					"name": "send_email",
					"description": "Send an email"
				}
			}
		]
	}`)

	tools := proxy.ExtractToolCalls(body)
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	names := make(map[string]bool)
	for _, tc := range tools {
		names[tc.Name] = true
	}
	if !names["search_database"] || !names["send_email"] {
		t.Errorf("expected search_database and send_email, got %v", names)
	}
}

func TestExtractToolCallsFromResponse_OpenAI(t *testing.T) {
	body := []byte(`{
		"id": "chatcmpl-123",
		"choices": [
			{
				"message": {
					"role": "assistant",
					"content": null,
					"tool_calls": [
						{
							"id": "call_abc",
							"type": "function",
							"function": {
								"name": "calculate",
								"arguments": "{\"x\": 5}"
							}
						}
					]
				}
			}
		]
	}`)

	tools := proxy.ExtractToolCallsFromResponse(body)
	if len(tools) == 0 {
		t.Fatal("expected tool calls in response, got none")
	}
	if tools[0].Name != "calculate" {
		t.Errorf("expected calculate, got %s", tools[0].Name)
	}
	if tools[0].ID != "call_abc" {
		t.Errorf("expected call_abc, got %s", tools[0].ID)
	}
}

func TestExtractToolCallsFromResponse_Anthropic(t *testing.T) {
	body := []byte(`{
		"id": "msg_123",
		"type": "message",
		"content": [
			{
				"type": "text",
				"text": "Let me search for that."
			},
			{
				"type": "tool_use",
				"id": "tool_use_123",
				"name": "web_search",
				"input": {"query": "weather"}
			}
		]
	}`)

	tools := proxy.ExtractToolCallsFromResponse(body)
	if len(tools) == 0 {
		t.Fatal("expected tool calls in response, got none")
	}
	if tools[0].Name != "web_search" {
		t.Errorf("expected web_search, got %s", tools[0].Name)
	}
	if tools[0].Type != "tool_use" {
		t.Errorf("expected tool_use type, got %s", tools[0].Type)
	}
}

func TestExtractToolCalls_NoTools(t *testing.T) {
	body := []byte(`{"model": "gpt-4", "messages": [{"role": "user", "content": "Hello"}]}`)
	tools := proxy.ExtractToolCalls(body)
	if len(tools) != 0 {
		t.Errorf("expected no tools, got %d", len(tools))
	}
}

func TestSession_Snapshot_IncludesTokensAndTools(t *testing.T) {
	sess := session.NewSession("test-snapshot", "http://backend", "127.0.0.1")

	sess.AddTokens(500, 200)
	sess.RecordToolCall("tool1", "function", "req-1")
	sess.RecordToolCall("tool2", "function", "req-2")

	snap := sess.Snapshot()

	if snap.TokensIn != 500 {
		t.Errorf("expected TokensIn=500, got %d", snap.TokensIn)
	}
	if snap.TokensOut != 200 {
		t.Errorf("expected TokensOut=200, got %d", snap.TokensOut)
	}
	if snap.ToolCalls != 2 {
		t.Errorf("expected ToolCalls=2, got %d", snap.ToolCalls)
	}
	if len(snap.ToolCallCounts) != 2 {
		t.Errorf("expected 2 tools in counts, got %d", len(snap.ToolCallCounts))
	}
	if len(snap.ToolCallHistory) != 2 {
		t.Errorf("expected 2 history entries, got %d", len(snap.ToolCallHistory))
	}
}
