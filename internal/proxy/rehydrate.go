package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"elida/internal/session"
)

// Rehydrator converts session state to a backend-specific request format
type Rehydrator interface {
	// Rehydrate creates a new request with full conversation history
	Rehydrate(state *session.SessionState, originalReq *http.Request) (*http.Request, error)

	// BackendType returns the target backend type (e.g., "openai", "anthropic")
	BackendType() string
}

// OpenAIRehydrator converts session state to OpenAI API format
type OpenAIRehydrator struct{}

func (r *OpenAIRehydrator) BackendType() string {
	return "openai"
}

func (r *OpenAIRehydrator) Rehydrate(state *session.SessionState, originalReq *http.Request) (*http.Request, error) {
	// Parse original request to get model and other params
	var originalBody map[string]any
	if originalReq.Body != nil {
		bodyBytes, err := io.ReadAll(originalReq.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read original request: %w", err)
		}
		originalReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		if err := json.Unmarshal(bodyBytes, &originalBody); err != nil {
			originalBody = make(map[string]any)
		}
	}

	// Build messages array
	messages := make([]map[string]string, 0, len(state.Messages)+1)

	// Add system prompt if exists
	if state.SystemPrompt != "" {
		messages = append(messages, map[string]string{
			"role":    "system",
			"content": state.SystemPrompt,
		})
	}

	// Add conversation history
	for _, msg := range state.Messages {
		messages = append(messages, map[string]string{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}

	// Build new request body
	body := map[string]any{
		"messages": messages,
		"stream":   true,
	}

	// Select model - try to preserve compatibility
	if model, ok := originalBody["model"].(string); ok {
		body["model"] = SelectCompatibleModel(model, "openai")
	} else {
		body["model"] = "gpt-4"
	}

	// Copy other parameters from original request
	for k, v := range originalBody {
		if k != "messages" && k != "model" {
			body[k] = v
		}
	}

	return buildRequest(originalReq, body)
}

// AnthropicRehydrator converts session state to Anthropic API format
type AnthropicRehydrator struct{}

func (r *AnthropicRehydrator) BackendType() string {
	return "anthropic"
}

func (r *AnthropicRehydrator) Rehydrate(state *session.SessionState, originalReq *http.Request) (*http.Request, error) {
	// Parse original request
	var originalBody map[string]any
	if originalReq.Body != nil {
		bodyBytes, err := io.ReadAll(originalReq.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read original request: %w", err)
		}
		originalReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		if err := json.Unmarshal(bodyBytes, &originalBody); err != nil {
			originalBody = make(map[string]any)
		}
	}

	// Build messages array (Anthropic doesn't include system in messages)
	messages := make([]map[string]string, 0, len(state.Messages))

	for _, msg := range state.Messages {
		// Skip system messages - Anthropic uses separate system field
		if msg.Role == "system" {
			continue
		}
		messages = append(messages, map[string]string{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}

	// Build new request body
	body := map[string]any{
		"messages": messages,
		"stream":   true,
	}

	// Add system prompt as separate field
	if state.SystemPrompt != "" {
		body["system"] = state.SystemPrompt
	}

	// Select model
	if model, ok := originalBody["model"].(string); ok {
		body["model"] = SelectCompatibleModel(model, "anthropic")
	} else {
		body["model"] = "claude-3-sonnet-20240229"
	}

	// Add max_tokens (required for Anthropic)
	if maxTokens, ok := originalBody["max_tokens"]; ok {
		body["max_tokens"] = maxTokens
	} else {
		body["max_tokens"] = 4096
	}

	// Copy other parameters
	for k, v := range originalBody {
		if k != "messages" && k != "model" && k != "system" && k != "max_tokens" {
			body[k] = v
		}
	}

	return buildRequest(originalReq, body)
}

// OllamaRehydrator converts session state to Ollama API format
type OllamaRehydrator struct{}

func (r *OllamaRehydrator) BackendType() string {
	return "ollama"
}

func (r *OllamaRehydrator) Rehydrate(state *session.SessionState, originalReq *http.Request) (*http.Request, error) {
	// Parse original request
	var originalBody map[string]any
	if originalReq.Body != nil {
		bodyBytes, err := io.ReadAll(originalReq.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read original request: %w", err)
		}
		originalReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		if err := json.Unmarshal(bodyBytes, &originalBody); err != nil {
			originalBody = make(map[string]any)
		}
	}

	// Build messages array for Ollama chat format
	messages := make([]map[string]string, 0, len(state.Messages)+1)

	// Add system prompt
	if state.SystemPrompt != "" {
		messages = append(messages, map[string]string{
			"role":    "system",
			"content": state.SystemPrompt,
		})
	}

	// Add conversation history
	for _, msg := range state.Messages {
		messages = append(messages, map[string]string{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}

	// Build request body
	body := map[string]any{
		"messages": messages,
		"stream":   true,
	}

	// Get model from original request or use default
	if model, ok := originalBody["model"].(string); ok {
		body["model"] = model
	} else {
		body["model"] = "llama3.2"
	}

	// Copy other parameters
	for k, v := range originalBody {
		if k != "messages" && k != "model" && k != "prompt" {
			body[k] = v
		}
	}

	return buildRequest(originalReq, body)
}

// buildRequest creates a new HTTP request with the given body
func buildRequest(originalReq *http.Request, body map[string]any) (*http.Request, error) {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(
		originalReq.Context(),
		originalReq.Method,
		originalReq.URL.String(),
		bytes.NewReader(bodyBytes),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Copy headers from original request
	for k, v := range originalReq.Header {
		req.Header[k] = v
	}

	// Update content-length
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(bodyBytes)))
	req.ContentLength = int64(len(bodyBytes))

	return req, nil
}

// GetRehydrator returns the appropriate rehydrator for a backend type
func GetRehydrator(backendType string) Rehydrator {
	switch backendType {
	case "openai":
		return &OpenAIRehydrator{}
	case "anthropic":
		return &AnthropicRehydrator{}
	case "ollama":
		return &OllamaRehydrator{}
	default:
		// Default to OpenAI format (most common)
		return &OpenAIRehydrator{}
	}
}

// Model compatibility mapping for cross-provider failover
var modelFamilyMap = map[string]map[string]string{
	// Anthropic models → OpenAI equivalents
	"claude-3-opus-20240229":   {"openai": "gpt-4", "ollama": "llama3.2"},
	"claude-3-sonnet-20240229": {"openai": "gpt-4", "ollama": "llama3.2"},
	"claude-3-haiku-20240307":  {"openai": "gpt-3.5-turbo", "ollama": "llama3.2"},
	"claude-3-5-sonnet-latest": {"openai": "gpt-4-turbo", "ollama": "llama3.2"},

	// OpenAI models → Anthropic equivalents
	"gpt-4":         {"anthropic": "claude-3-opus-20240229", "ollama": "llama3.2"},
	"gpt-4-turbo":   {"anthropic": "claude-3-5-sonnet-latest", "ollama": "llama3.2"},
	"gpt-3.5-turbo": {"anthropic": "claude-3-haiku-20240307", "ollama": "llama3.2"},
	"o1":            {"anthropic": "claude-3-opus-20240229", "ollama": "llama3.2"},
	"o1-mini":       {"anthropic": "claude-3-sonnet-20240229", "ollama": "llama3.2"},
}

// Default models per provider
var defaultModels = map[string]string{
	"openai":    "gpt-4",
	"anthropic": "claude-3-sonnet-20240229",
	"ollama":    "llama3.2",
}

// SelectCompatibleModel finds an equivalent model on the target provider
func SelectCompatibleModel(originalModel, targetProvider string) string {
	// Check if we have a direct mapping
	if mappings, ok := modelFamilyMap[originalModel]; ok {
		if target, ok := mappings[targetProvider]; ok {
			return target
		}
	}

	// Check for partial matches (e.g., "gpt-4-0613" → "gpt-4")
	for model, mappings := range modelFamilyMap {
		if len(originalModel) >= len(model) && originalModel[:len(model)] == model {
			if target, ok := mappings[targetProvider]; ok {
				return target
			}
		}
	}

	// Return default for target provider
	if defaultModel, ok := defaultModels[targetProvider]; ok {
		return defaultModel
	}

	// Last resort: return original model
	return originalModel
}
