package unit

import (
	"testing"

	"elida/internal/proxy"
)

func TestExtractToolCallsFromResponse_OpenAI_Arguments(t *testing.T) {
	body := []byte(`{
		"choices": [{
			"message": {
				"tool_calls": [{
					"id": "call_abc123",
					"type": "function",
					"function": {
						"name": "rm_file",
						"arguments": "{\"path\": \"/tmp/test.txt\"}"
					}
				}]
			}
		}]
	}`)

	toolCalls := proxy.ExtractToolCallsFromResponse(body)
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	if toolCalls[0].Name != "rm_file" {
		t.Errorf("expected name 'rm_file', got %q", toolCalls[0].Name)
	}
	if toolCalls[0].Arguments != `{"path": "/tmp/test.txt"}` {
		t.Errorf("expected arguments to be captured, got %q", toolCalls[0].Arguments)
	}
}

func TestExtractToolCallsFromResponse_OpenAI_MultipleToolCalls(t *testing.T) {
	body := []byte(`{
		"choices": [{
			"message": {
				"tool_calls": [
					{
						"id": "call_1",
						"type": "function",
						"function": {
							"name": "read_file",
							"arguments": "{\"path\": \"/etc/passwd\"}"
						}
					},
					{
						"id": "call_2",
						"type": "function",
						"function": {
							"name": "exec_command",
							"arguments": "{\"cmd\": \"ls -la\"}"
						}
					}
				]
			}
		}]
	}`)

	toolCalls := proxy.ExtractToolCallsFromResponse(body)
	if len(toolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(toolCalls))
	}
	if toolCalls[0].Arguments == "" || toolCalls[1].Arguments == "" {
		t.Error("expected arguments to be captured for both tool calls")
	}
}

func TestExtractToolCallsFromResponse_Anthropic_Arguments(t *testing.T) {
	body := []byte(`{
		"content": [
			{
				"type": "text",
				"text": "I'll delete that file for you."
			},
			{
				"type": "tool_use",
				"id": "toolu_abc123",
				"name": "shell_exec",
				"input": {
					"command": "rm -rf /tmp/data",
					"sudo": true
				}
			}
		]
	}`)

	toolCalls := proxy.ExtractToolCallsFromResponse(body)
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	if toolCalls[0].Name != "shell_exec" {
		t.Errorf("expected name 'shell_exec', got %q", toolCalls[0].Name)
	}
	if toolCalls[0].Arguments == "" {
		t.Error("expected arguments to be captured for Anthropic tool_use")
	}
	// The input should be JSON-encoded
	if toolCalls[0].Arguments == "{}" {
		t.Error("expected non-empty arguments")
	}
}

func TestExtractToolCallsFromResponse_NoArguments(t *testing.T) {
	// OpenAI with empty arguments
	body := []byte(`{
		"choices": [{
			"message": {
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {
						"name": "get_time",
						"arguments": ""
					}
				}]
			}
		}]
	}`)

	toolCalls := proxy.ExtractToolCallsFromResponse(body)
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	if toolCalls[0].Arguments != "" {
		t.Errorf("expected empty arguments, got %q", toolCalls[0].Arguments)
	}
}

func TestExtractToolCalls_Request_OpenAI_Arguments(t *testing.T) {
	body := []byte(`{
		"messages": [{
			"tool_calls": [{
				"id": "call_1",
				"type": "function",
				"function": {
					"name": "search",
					"arguments": "{\"query\": \"test\"}"
				}
			}]
		}]
	}`)

	toolCalls := proxy.ExtractToolCalls(body)
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	if toolCalls[0].Arguments != `{"query": "test"}` {
		t.Errorf("expected arguments captured, got %q", toolCalls[0].Arguments)
	}
}

func TestExtractToolCalls_Request_Anthropic_Arguments(t *testing.T) {
	body := []byte(`{
		"messages": [{
			"role": "assistant",
			"content": [
				{
					"type": "tool_use",
					"id": "toolu_1",
					"name": "bash",
					"input": {"command": "echo hello"}
				}
			]
		}]
	}`)

	toolCalls := proxy.ExtractToolCalls(body)
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	if toolCalls[0].Arguments == "" {
		t.Error("expected arguments captured for Anthropic request-side tool call")
	}
}
