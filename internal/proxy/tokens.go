package proxy

import (
	"encoding/json"
	"strings"
)

// TokenUsage represents extracted token usage from an LLM API response
type TokenUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// ToolCallInfo represents extracted tool/function call information
type ToolCallInfo struct {
	Name string `json:"name"`
	Type string `json:"type"` // "function", "code_interpreter", etc.
	ID   string `json:"id"`
}

// ExtractTokenUsage extracts token usage from an LLM API response body.
// Supports OpenAI, Anthropic, and other common formats.
func ExtractTokenUsage(body []byte) *TokenUsage {
	if len(body) == 0 {
		return nil
	}

	// Try OpenAI format
	var openaiResp struct {
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			TotalTokens      int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &openaiResp) == nil && openaiResp.Usage.TotalTokens > 0 {
		return &TokenUsage{
			PromptTokens:     openaiResp.Usage.PromptTokens,
			CompletionTokens: openaiResp.Usage.CompletionTokens,
			TotalTokens:      openaiResp.Usage.TotalTokens,
		}
	}

	// Try Anthropic format
	var anthropicResp struct {
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &anthropicResp) == nil {
		if anthropicResp.Usage.InputTokens > 0 || anthropicResp.Usage.OutputTokens > 0 {
			return &TokenUsage{
				PromptTokens:     anthropicResp.Usage.InputTokens,
				CompletionTokens: anthropicResp.Usage.OutputTokens,
				TotalTokens:      anthropicResp.Usage.InputTokens + anthropicResp.Usage.OutputTokens,
			}
		}
	}

	// Try Ollama format
	var ollamaResp struct {
		PromptEvalCount int64 `json:"prompt_eval_count"`
		EvalCount       int64 `json:"eval_count"`
	}
	if json.Unmarshal(body, &ollamaResp) == nil {
		if ollamaResp.PromptEvalCount > 0 || ollamaResp.EvalCount > 0 {
			return &TokenUsage{
				PromptTokens:     ollamaResp.PromptEvalCount,
				CompletionTokens: ollamaResp.EvalCount,
				TotalTokens:      ollamaResp.PromptEvalCount + ollamaResp.EvalCount,
			}
		}
	}

	return nil
}

// ExtractToolCalls extracts tool/function calls from a request body.
// Supports OpenAI function calling and tool_calls format.
func ExtractToolCalls(body []byte) []ToolCallInfo {
	if len(body) == 0 {
		return nil
	}

	var result []ToolCallInfo

	// Try OpenAI messages format with tool_calls
	var openaiToolReq struct {
		Messages []struct {
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &openaiToolReq) == nil {
		for _, msg := range openaiToolReq.Messages {
			for _, tc := range msg.ToolCalls {
				if tc.Function.Name != "" {
					result = append(result, ToolCallInfo{
						Name: tc.Function.Name,
						Type: tc.Type,
						ID:   tc.ID,
					})
				}
			}
		}
		if len(result) > 0 {
			return result
		}
	}

	// Try OpenAI tools definition format (for tracking what tools are available)
	var openaiToolsDef struct {
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if json.Unmarshal(body, &openaiToolsDef) == nil {
		for _, tool := range openaiToolsDef.Tools {
			if tool.Function.Name != "" {
				result = append(result, ToolCallInfo{
					Name: tool.Function.Name,
					Type: tool.Type,
				})
			}
		}
		if len(result) > 0 {
			return result
		}
	}

	// Try Anthropic tool_use format
	var anthropicReq struct {
		Messages []struct {
			Content interface{} `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &anthropicReq) == nil {
		for _, msg := range anthropicReq.Messages {
			// Content can be array of content blocks
			if contentArr, ok := msg.Content.([]interface{}); ok {
				for _, block := range contentArr {
					if blockMap, ok := block.(map[string]interface{}); ok {
						if blockType, ok := blockMap["type"].(string); ok && blockType == "tool_use" {
							if name, ok := blockMap["name"].(string); ok {
								id, _ := blockMap["id"].(string)
								result = append(result, ToolCallInfo{
									Name: name,
									Type: "tool_use",
									ID:   id,
								})
							}
						}
					}
				}
			}
		}
		if len(result) > 0 {
			return result
		}
	}

	// Try to find function calls in JSON using simple pattern matching
	// This catches cases where the format isn't exactly OpenAI or Anthropic
	bodyStr := string(body)
	if strings.Contains(bodyStr, `"function"`) || strings.Contains(bodyStr, `"tool_calls"`) {
		// Generic extraction for function names
		var generic map[string]interface{}
		if json.Unmarshal(body, &generic) == nil {
			extractToolsRecursive(generic, &result)
		}
	}

	return result
}

// extractToolsRecursive recursively searches for tool/function names in a JSON structure
func extractToolsRecursive(data interface{}, result *[]ToolCallInfo) {
	switch v := data.(type) {
	case map[string]interface{}:
		// Check if this is a function/tool definition
		if name, ok := v["name"].(string); ok {
			// Check if this looks like a function/tool
			if _, hasFunction := v["function"]; hasFunction || strings.Contains(name, "_") {
				*result = append(*result, ToolCallInfo{
					Name: name,
					Type: "function",
				})
			}
		}
		if funcDef, ok := v["function"].(map[string]interface{}); ok {
			if name, ok := funcDef["name"].(string); ok {
				*result = append(*result, ToolCallInfo{
					Name: name,
					Type: "function",
				})
			}
		}
		// Recurse into children
		for _, child := range v {
			extractToolsRecursive(child, result)
		}
	case []interface{}:
		for _, item := range v {
			extractToolsRecursive(item, result)
		}
	}
}

// ExtractToolCallsFromResponse extracts tool calls from an LLM response.
// This is for when the model decides to call tools.
func ExtractToolCallsFromResponse(body []byte) []ToolCallInfo {
	if len(body) == 0 {
		return nil
	}

	var result []ToolCallInfo

	// OpenAI response format with tool_calls
	var openaiResp struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &openaiResp) == nil {
		for _, choice := range openaiResp.Choices {
			for _, tc := range choice.Message.ToolCalls {
				if tc.Function.Name != "" {
					result = append(result, ToolCallInfo{
						Name: tc.Function.Name,
						Type: tc.Type,
						ID:   tc.ID,
					})
				}
			}
		}
		if len(result) > 0 {
			return result
		}
	}

	// Anthropic response format with tool_use
	var anthropicResp struct {
		Content []struct {
			Type  string      `json:"type"`
			ID    string      `json:"id"`
			Name  string      `json:"name"`
			Input interface{} `json:"input"`
		} `json:"content"`
	}
	if json.Unmarshal(body, &anthropicResp) == nil {
		for _, block := range anthropicResp.Content {
			if block.Type == "tool_use" && block.Name != "" {
				result = append(result, ToolCallInfo{
					Name: block.Name,
					Type: "tool_use",
					ID:   block.ID,
				})
			}
		}
	}

	return result
}
