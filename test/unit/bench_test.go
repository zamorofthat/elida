package unit

import (
	"fmt"
	"testing"

	"elida/internal/policy"
	"elida/internal/proxy"
)

// ============================================================
// Tool Call Policy Evaluation Benchmarks
// ============================================================

func BenchmarkEvaluateToolCalls_SingleRule(b *testing.B) {
	engine := policy.NewEngine(policy.Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []policy.Rule{
			{
				Name:     "block_dangerous",
				Type:     "tool_blocked",
				Patterns: []string{"exec_*", "shell_*", "rm_*", "sudo_*", "eval_*"},
				Severity: "critical",
				Action:   "block",
			},
		},
	})

	toolCalls := []policy.ToolCall{
		{Name: "read_file", Arguments: `{"path": "/tmp/test.txt"}`},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.EvaluateToolCalls("bench-session", toolCalls)
	}
}

func BenchmarkEvaluateToolCalls_Match(b *testing.B) {
	engine := policy.NewEngine(policy.Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []policy.Rule{
			{
				Name:     "block_dangerous",
				Type:     "tool_blocked",
				Patterns: []string{"exec_*", "shell_*", "rm_*", "sudo_*", "eval_*"},
				Severity: "critical",
				Action:   "block",
			},
		},
	})

	toolCalls := []policy.ToolCall{
		{Name: "exec_command", Arguments: `{"cmd": "ls"}`},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.EvaluateToolCalls(fmt.Sprintf("bench-%d", i), toolCalls)
	}
}

func BenchmarkEvaluateToolCalls_ArgumentPattern(b *testing.B) {
	engine := policy.NewEngine(policy.Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []policy.Rule{
			{
				Name:     "dangerous_args",
				Type:     "tool_argument_pattern",
				Patterns: []string{`rm\s+-rf`, `chmod\s+777`, `curl.*\|.*sh`},
				Severity: "critical",
				Action:   "terminate",
			},
		},
	})

	b.Run("no_match", func(b *testing.B) {
		toolCalls := []policy.ToolCall{
			{Name: "bash", Arguments: `{"command": "ls -la /tmp"}`},
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			engine.EvaluateToolCalls("bench-session", toolCalls)
		}
	})

	b.Run("match", func(b *testing.B) {
		toolCalls := []policy.ToolCall{
			{Name: "bash", Arguments: `{"command": "rm -rf /tmp/data"}`},
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			engine.EvaluateToolCalls(fmt.Sprintf("bench-%d", i), toolCalls)
		}
	})
}

func BenchmarkEvaluateToolCalls_MultipleRules(b *testing.B) {
	engine := policy.NewEngine(policy.Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []policy.Rule{
			{
				Name:     "block_tools",
				Type:     "tool_blocked",
				Patterns: []string{"exec_*", "shell_*", "rm_*", "sudo_*", "eval_*"},
				Severity: "critical",
				Action:   "block",
			},
			{
				Name:     "dangerous_args",
				Type:     "tool_argument_pattern",
				Patterns: []string{`rm\s+-rf`, `chmod\s+777`, `curl.*\|.*sh`},
				Severity: "critical",
				Action:   "terminate",
			},
		},
	})

	toolCalls := []policy.ToolCall{
		{Name: "read_file", Arguments: `{"path": "/tmp/test.txt"}`},
		{Name: "search", Arguments: `{"query": "hello world"}`},
		{Name: "write_file", Arguments: `{"path": "/tmp/out.txt", "content": "data"}`},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.EvaluateToolCalls("bench-session", toolCalls)
	}
}

func BenchmarkEvaluateToolCalls_StandardPresetToolRules(b *testing.B) {
	// Simulates the standard preset: both tool_blocked and tool_argument_pattern
	engine := policy.NewEngine(policy.Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []policy.Rule{
			{
				Name:     "block_dangerous_tools",
				Type:     "tool_blocked",
				Patterns: []string{"exec_*", "shell_*", "rm_*", "sudo_*", "eval_*"},
				Severity: "critical",
				Action:   "block",
			},
			{
				Name:     "dangerous_tool_arguments",
				Type:     "tool_argument_pattern",
				Patterns: []string{`rm\s+-rf`, `chmod\s+777`, `curl.*\|.*sh`},
				Severity: "critical",
				Action:   "terminate",
			},
		},
	})

	// Realistic 3-tool-call response (safe tools)
	safeCalls := []policy.ToolCall{
		{Name: "get_weather", Arguments: `{"city": "San Francisco", "units": "fahrenheit"}`},
		{Name: "search_web", Arguments: `{"query": "latest AI research papers 2025"}`},
		{Name: "read_file", Arguments: `{"path": "/home/user/notes.txt"}`},
	}

	b.Run("safe_tools", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			engine.EvaluateToolCalls("bench-session", safeCalls)
		}
	})

	// Single dangerous tool
	dangerousCalls := []policy.ToolCall{
		{Name: "exec_bash", Arguments: `{"command": "rm -rf /var/data"}`},
	}

	b.Run("blocked_tool", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			engine.EvaluateToolCalls(fmt.Sprintf("bench-%d", i), dangerousCalls)
		}
	})
}

// ============================================================
// Token Extraction Benchmarks
// ============================================================

var openAIResponseWithTools = []byte(`{
	"id": "chatcmpl-abc123",
	"object": "chat.completion",
	"choices": [{
		"index": 0,
		"message": {
			"role": "assistant",
			"tool_calls": [
				{
					"id": "call_abc123",
					"type": "function",
					"function": {
						"name": "get_weather",
						"arguments": "{\"location\": \"San Francisco\", \"unit\": \"fahrenheit\"}"
					}
				},
				{
					"id": "call_def456",
					"type": "function",
					"function": {
						"name": "search_database",
						"arguments": "{\"query\": \"recent orders\", \"limit\": 10, \"filters\": {\"status\": \"pending\"}}"
					}
				}
			]
		},
		"finish_reason": "tool_calls"
	}],
	"usage": {
		"prompt_tokens": 150,
		"completion_tokens": 50,
		"total_tokens": 200
	}
}`)

var anthropicResponseWithTools = []byte(`{
	"id": "msg_abc123",
	"type": "message",
	"role": "assistant",
	"content": [
		{
			"type": "text",
			"text": "I'll look up the weather for you."
		},
		{
			"type": "tool_use",
			"id": "toolu_abc123",
			"name": "get_weather",
			"input": {
				"location": "San Francisco",
				"unit": "fahrenheit"
			}
		},
		{
			"type": "tool_use",
			"id": "toolu_def456",
			"name": "search_database",
			"input": {
				"query": "recent orders",
				"limit": 10,
				"filters": {"status": "pending"}
			}
		}
	],
	"usage": {
		"input_tokens": 150,
		"output_tokens": 50
	}
}`)

func BenchmarkExtractToolCallsFromResponse_OpenAI(b *testing.B) {
	for i := 0; i < b.N; i++ {
		proxy.ExtractToolCallsFromResponse(openAIResponseWithTools)
	}
}

func BenchmarkExtractToolCallsFromResponse_Anthropic(b *testing.B) {
	for i := 0; i < b.N; i++ {
		proxy.ExtractToolCallsFromResponse(anthropicResponseWithTools)
	}
}

// ============================================================
// End-to-End: Extract + Evaluate
// ============================================================

func BenchmarkExtractAndEvaluate_OpenAI(b *testing.B) {
	engine := policy.NewEngine(policy.Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []policy.Rule{
			{
				Name:     "block_tools",
				Type:     "tool_blocked",
				Patterns: []string{"exec_*", "shell_*", "rm_*", "sudo_*", "eval_*"},
				Severity: "critical",
				Action:   "block",
			},
			{
				Name:     "dangerous_args",
				Type:     "tool_argument_pattern",
				Patterns: []string{`rm\s+-rf`, `chmod\s+777`, `curl.*\|.*sh`},
				Severity: "critical",
				Action:   "terminate",
			},
		},
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		toolCalls := proxy.ExtractToolCallsFromResponse(openAIResponseWithTools)
		policyToolCalls := make([]policy.ToolCall, len(toolCalls))
		for j, tc := range toolCalls {
			policyToolCalls[j] = policy.ToolCall{Name: tc.Name, Arguments: tc.Arguments}
		}
		engine.EvaluateToolCalls("bench-session", policyToolCalls)
	}
}

func BenchmarkExtractAndEvaluate_Anthropic(b *testing.B) {
	engine := policy.NewEngine(policy.Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []policy.Rule{
			{
				Name:     "block_tools",
				Type:     "tool_blocked",
				Patterns: []string{"exec_*", "shell_*", "rm_*", "sudo_*", "eval_*"},
				Severity: "critical",
				Action:   "block",
			},
			{
				Name:     "dangerous_args",
				Type:     "tool_argument_pattern",
				Patterns: []string{`rm\s+-rf`, `chmod\s+777`, `curl.*\|.*sh`},
				Severity: "critical",
				Action:   "terminate",
			},
		},
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		toolCalls := proxy.ExtractToolCallsFromResponse(anthropicResponseWithTools)
		policyToolCalls := make([]policy.ToolCall, len(toolCalls))
		for j, tc := range toolCalls {
			policyToolCalls[j] = policy.ToolCall{Name: tc.Name, Arguments: tc.Arguments}
		}
		engine.EvaluateToolCalls("bench-session", policyToolCalls)
	}
}
