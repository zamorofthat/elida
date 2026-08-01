package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"elida/internal/router"
	"elida/internal/session"
)

// Rehydrator converts session state to a backend-specific request format
type Rehydrator interface {
	// Rehydrate creates a new request with full conversation history.
	// targetModel, when non-empty, is the model the caller has already
	// resolved (via ResolveFailoverModel) for the destination backend and
	// must be used verbatim instead of re-deriving one internally. When
	// empty, the rehydrator falls back to its own original-request-derived
	// model selection (used outside the failover path).
	Rehydrate(state *session.SessionState, originalReq *http.Request, targetModel string) (*http.Request, error)

	// BackendType returns the target backend type (e.g., "openai", "anthropic")
	BackendType() string
}

// OpenAIRehydrator converts session state to OpenAI API format
type OpenAIRehydrator struct{}

func (r *OpenAIRehydrator) BackendType() string {
	return "openai"
}

func (r *OpenAIRehydrator) Rehydrate(state *session.SessionState, originalReq *http.Request, targetModel string) (*http.Request, error) {
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

	// Build messages array. state.Messages/state.SystemPrompt only ever get
	// populated by Session.RecordMessage/SetSystemPrompt, which today have no
	// production callers - so on the real failover path the session history
	// is always empty. Overwriting the outgoing request with that empty
	// history would silently drop the user's conversation, so when there's
	// no recorded history, fall back to the original request's own messages
	// (chat-completions requests are stateless: the full conversation,
	// including any system message, already lives in the original body).
	var messages any
	if len(state.Messages) > 0 {
		msgs := make([]map[string]string, 0, len(state.Messages)+1)
		if state.SystemPrompt != "" {
			msgs = append(msgs, map[string]string{
				"role":    "system",
				"content": state.SystemPrompt,
			})
		}
		for _, msg := range state.Messages {
			msgs = append(msgs, map[string]string{
				"role":    msg.Role,
				"content": msg.Content,
			})
		}
		messages = msgs
	} else if orig, ok := originalBody["messages"]; ok {
		messages = orig
	} else {
		messages = []map[string]string{}
	}

	// Build new request body
	body := map[string]any{
		"messages": messages,
		"stream":   true,
	}

	// Select model - use the caller-resolved target model when given
	// (failover path), otherwise fall back to prior best-effort behavior.
	if targetModel != "" {
		body["model"] = targetModel
	} else if model, ok := originalBody["model"].(string); ok {
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

func (r *AnthropicRehydrator) Rehydrate(state *session.SessionState, originalReq *http.Request, targetModel string) (*http.Request, error) {
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

	// Build messages array (Anthropic doesn't include system in messages).
	// As in the OpenAI rehydrator, state.Messages/state.SystemPrompt are
	// always empty in production (no caller ever records session history),
	// so fall back to the original request's own messages/system fields
	// instead of replaying an empty conversation to the fallback backend.
	var messages any
	if len(state.Messages) > 0 {
		msgs := make([]map[string]string, 0, len(state.Messages))
		for _, msg := range state.Messages {
			// Skip system messages - Anthropic uses separate system field
			if msg.Role == "system" {
				continue
			}
			msgs = append(msgs, map[string]string{
				"role":    msg.Role,
				"content": msg.Content,
			})
		}
		messages = msgs
	} else if orig, ok := originalBody["messages"]; ok {
		messages = orig
	} else {
		messages = []map[string]string{}
	}

	// Build new request body
	body := map[string]any{
		"messages": messages,
		"stream":   true,
	}

	// Add system prompt as separate field
	if len(state.Messages) > 0 {
		if state.SystemPrompt != "" {
			body["system"] = state.SystemPrompt
		}
	} else if system, ok := originalBody["system"]; ok {
		body["system"] = system
	}

	// Select model - use the caller-resolved target model when given
	// (failover path), otherwise fall back to prior best-effort behavior.
	if targetModel != "" {
		body["model"] = targetModel
	} else if model, ok := originalBody["model"].(string); ok {
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

func (r *OllamaRehydrator) Rehydrate(state *session.SessionState, originalReq *http.Request, targetModel string) (*http.Request, error) {
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

	// Build messages array for Ollama chat format. As in the other
	// rehydrators, state.Messages/state.SystemPrompt are always empty in
	// production (no caller ever records session history), so fall back to
	// the original request's own messages instead of replaying an empty
	// conversation to the fallback backend.
	var messages any
	if len(state.Messages) > 0 {
		msgs := make([]map[string]string, 0, len(state.Messages)+1)
		if state.SystemPrompt != "" {
			msgs = append(msgs, map[string]string{
				"role":    "system",
				"content": state.SystemPrompt,
			})
		}
		for _, msg := range state.Messages {
			msgs = append(msgs, map[string]string{
				"role":    msg.Role,
				"content": msg.Content,
			})
		}
		messages = msgs
	} else if orig, ok := originalBody["messages"]; ok {
		messages = orig
	} else {
		messages = []map[string]string{}
	}

	// Build request body
	body := map[string]any{
		"messages": messages,
		"stream":   true,
	}

	// Get model - use the caller-resolved target model when given
	// (failover path), otherwise fall back to prior best-effort behavior.
	if targetModel != "" {
		body["model"] = targetModel
	} else if model, ok := originalBody["model"].(string); ok {
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

// ResolveFailoverModel decides which model string to send to target when a
// request fails over to it, per the binding decision order (feedback #8):
//
//  1. target.Model (explicit substitution) always wins.
//  2. If target declares Models globs and originalModel matches one, keep it
//     unchanged - it's already known-compatible with this backend.
//  3. Otherwise consult the remap table (SelectCompatibleModel); the result
//     is accepted only if it matches target's Models globs, or target has no
//     globs to validate against.
//  4. If none of the above produce a model we can trust for this backend,
//     the model is unmappable: the caller must skip this backend rather than
//     send a request with a model the backend doesn't understand.
//
// A target with neither an explicit Model nor Models globs is the trickiest
// case: there's nothing to validate against, so the remap table's result is
// normally accepted at face value. The one exception is when the remap table
// couldn't do anything for this target type (it has no known family mapping
// or default, so SelectCompatibleModel returns originalModel unchanged as a
// last resort) - blindly forwarding an untranslated model name to a backend
// of a different type is exactly how a "gemma" request ends up 400ing
// against a Mistral endpoint. In that situation we treat the model as
// unmappable instead of guessing.
func ResolveFailoverModel(originalModel string, target *router.Backend) (string, bool) {
	// Step 1: explicit substitution always wins.
	if target.Model != "" {
		return target.Model, true
	}

	remapped := SelectCompatibleModel(originalModel, target.Type)

	if len(target.Models) > 0 {
		// Step 2: original already matches this backend's declared models.
		if router.ModelMatches(target.Models, originalModel) {
			return originalModel, true
		}
		// Step 3: accept the remap only if it matches the declared globs.
		if router.ModelMatches(target.Models, remapped) {
			return remapped, true
		}
		// Step 4: nothing validates - unmappable.
		return "", false
	}

	// No globs to validate against. Accept the remap table's result unless
	// it's an unchanged pass-through for a target type the remap table has
	// no real knowledge of (see doc comment above).
	if remapped != originalModel {
		return remapped, true
	}
	if _, knownTargetType := defaultModels[target.Type]; knownTargetType {
		return remapped, true
	}
	return "", false
}

// originalModelFromRequest extracts the "model" field from originalReq's
// JSON body without consuming it - the body is restored via a fresh reader
// so later callers (e.g. Rehydrate) can still read it in full.
//
// In the failover path, originalReq is the request that was just sent to
// the failed backend via Transport.RoundTrip, which drains and closes its
// Body. When originalReq.GetBody is available (net/http populates it
// automatically for bodies built from *bytes.Reader/*bytes.Buffer/
// *strings.Reader, which is exactly how createBackendRequest builds it),
// prefer it to get a fresh, undrained copy of the real original bytes
// rather than reading the now-empty drained Body.
func originalModelFromRequest(originalReq *http.Request) (string, error) {
	if originalReq == nil {
		return "", nil
	}

	var bodyBytes []byte
	switch {
	case originalReq.GetBody != nil:
		rc, err := originalReq.GetBody()
		if err != nil {
			return "", fmt.Errorf("failed to get original request body: %w", err)
		}
		bodyBytes, err = io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return "", fmt.Errorf("failed to read original request: %w", err)
		}
	case originalReq.Body != nil:
		var err error
		bodyBytes, err = io.ReadAll(originalReq.Body)
		if err != nil {
			return "", fmt.Errorf("failed to read original request: %w", err)
		}
	default:
		return "", nil
	}
	originalReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		// Malformed/absent JSON body - treat as "no model specified" rather
		// than failing the whole failover attempt.
		return "", nil
	}

	return payload.Model, nil
}
