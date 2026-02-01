package router

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"elida/internal/config"
)

// Backend represents a configured backend with its routing rules
type Backend struct {
	Name      string
	URL       *url.URL
	WSURL     *url.URL // Derived WebSocket URL: ws:// or wss://
	Type      string   // ollama, openai, anthropic, mistral
	Models    []string // glob patterns for model matching
	Default   bool
	Transport *http.Transport
}

// Router handles routing requests to multiple backends
type Router struct {
	backends            map[string]*Backend
	defaultBackend      *Backend
	methods             []string // routing method priority: header, model, path, default
	strictModelMatching bool     // reject if model doesn't match any backend pattern
	blockedModels       []string // models to always reject (glob patterns)
}

// NewRouter creates a new multi-backend router from configuration
func NewRouter(backends map[string]config.BackendConfig, routing config.RoutingConfig) (*Router, error) {
	if len(backends) == 0 {
		return nil, fmt.Errorf("no backends configured")
	}

	r := &Router{
		backends:            make(map[string]*Backend),
		methods:             routing.Methods,
		strictModelMatching: routing.StrictModelMatching,
		blockedModels:       routing.BlockedModels,
	}

	// Use default methods if not specified
	if len(r.methods) == 0 {
		r.methods = config.GetDefaultRoutingMethods()
	}

	// Initialize backends
	for name, bcfg := range backends {
		backendURL, err := url.Parse(bcfg.URL)
		if err != nil {
			return nil, fmt.Errorf("invalid URL for backend %q: %w", name, err)
		}

		// Derive WebSocket URL from HTTP URL
		wsURL := deriveWebSocketURL(backendURL)

		backend := &Backend{
			Name:    name,
			URL:     backendURL,
			WSURL:   wsURL,
			Type:    bcfg.Type,
			Models:  bcfg.Models,
			Default: bcfg.Default,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
				DisableCompression:  true,
			},
		}

		r.backends[name] = backend

		if bcfg.Default {
			r.defaultBackend = backend
		}

		slog.Info("backend configured",
			"name", name,
			"url", bcfg.URL,
			"ws_url", wsURL.String(),
			"type", bcfg.Type,
			"models", bcfg.Models,
			"default", bcfg.Default,
		)
	}

	if r.defaultBackend == nil {
		return nil, fmt.Errorf("no default backend configured")
	}

	slog.Info("router initialized",
		"backends", len(r.backends),
		"methods", r.methods,
		"default", r.defaultBackend.Name,
		"strict_model_matching", r.strictModelMatching,
		"blocked_models", len(r.blockedModels),
	)

	return r, nil
}

// ErrModelBlocked is returned when a model is on the blocklist
var ErrModelBlocked = fmt.Errorf("model is blocked by policy")

// ErrModelNotAllowed is returned when strict mode is enabled and model doesn't match any backend
var ErrModelNotAllowed = fmt.Errorf("model not allowed (strict mode enabled)")

// NewSingleBackendRouter creates a router with a single backend (backward compatibility)
func NewSingleBackendRouter(backendURL string) (*Router, error) {
	parsed, err := url.Parse(backendURL)
	if err != nil {
		return nil, fmt.Errorf("invalid backend URL: %w", err)
	}

	backend := &Backend{
		Name:    "default",
		URL:     parsed,
		WSURL:   deriveWebSocketURL(parsed),
		Type:    "unknown",
		Default: true,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true,
		},
	}

	return &Router{
		backends:       map[string]*Backend{"default": backend},
		defaultBackend: backend,
		methods:        []string{"default"},
	}, nil
}

// deriveWebSocketURL converts an HTTP URL to a WebSocket URL
// http:// -> ws://, https:// -> wss://
func deriveWebSocketURL(httpURL *url.URL) *url.URL {
	wsURL := *httpURL // Copy the URL
	switch wsURL.Scheme {
	case "http":
		wsURL.Scheme = "ws"
	case "https":
		wsURL.Scheme = "wss"
	}
	return &wsURL
}

// Select chooses the appropriate backend for a request
func (r *Router) Select(req *http.Request, body []byte) (*Backend, error) {
	// Extract model from request body for blocklist/strict checking
	model := extractModel(body)

	// Check blocklist first (LLM05 - Supply Chain)
	if model != "" && r.isModelBlocked(model) {
		slog.Warn("model blocked by policy",
			"model", model,
			"blocked_models", r.blockedModels,
		)
		return nil, ErrModelBlocked
	}

	var matchedByModel bool
	for _, method := range r.methods {
		var backend *Backend

		switch method {
		case "header":
			backend = r.matchByHeader(req)
		case "model":
			backend = r.matchByModel(body)
			if backend != nil {
				matchedByModel = true
			}
		case "path":
			backend = r.matchByPath(req)
		case "default":
			// In strict mode, only use default if model matched or no model specified
			if r.strictModelMatching && model != "" && !matchedByModel {
				slog.Warn("model not allowed in strict mode",
					"model", model,
				)
				return nil, ErrModelNotAllowed
			}
			backend = r.defaultBackend
		}

		if backend != nil {
			slog.Debug("backend selected",
				"method", method,
				"backend", backend.Name,
				"url", backend.URL.String(),
			)
			return backend, nil
		}
	}

	// Should never reach here if default is in methods
	return r.defaultBackend, nil
}

// isModelBlocked checks if a model matches any blocked pattern
func (r *Router) isModelBlocked(model string) bool {
	for _, pattern := range r.blockedModels {
		matched, err := filepath.Match(pattern, model)
		if err != nil {
			slog.Warn("invalid blocked model pattern", "pattern", pattern, "error", err)
			continue
		}
		if matched {
			return true
		}
	}
	return false
}

// matchByHeader checks for X-Backend header
func (r *Router) matchByHeader(req *http.Request) *Backend {
	backendName := req.Header.Get("X-Backend")
	if backendName == "" {
		return nil
	}

	backend, ok := r.backends[backendName]
	if !ok {
		slog.Warn("unknown backend in header",
			"header", backendName,
			"available", r.backendNames(),
		)
		return nil
	}

	return backend
}

// matchByModel extracts model from request body and matches against backend patterns
func (r *Router) matchByModel(body []byte) *Backend {
	if len(body) == 0 {
		return nil
	}

	model := extractModel(body)
	if model == "" {
		return nil
	}

	for _, backend := range r.backends {
		for _, pattern := range backend.Models {
			matched, err := filepath.Match(pattern, model)
			if err != nil {
				slog.Warn("invalid model pattern", "pattern", pattern, "error", err)
				continue
			}
			if matched {
				slog.Debug("model matched",
					"model", model,
					"pattern", pattern,
					"backend", backend.Name,
				)
				return backend
			}
		}
	}

	return nil
}

// matchByPath matches request path against backend types
func (r *Router) matchByPath(req *http.Request) *Backend {
	path := req.URL.Path

	// Check for path prefix matching backend names
	// e.g., /openai/v1/chat/completions -> openai backend
	for name, backend := range r.backends {
		prefix := "/" + name + "/"
		if strings.HasPrefix(path, prefix) {
			return backend
		}
	}

	// Also check by type
	// e.g., /v1/chat/completions with type matching
	for _, backend := range r.backends {
		if backend.Type != "" {
			prefix := "/" + backend.Type + "/"
			if strings.HasPrefix(path, prefix) {
				return backend
			}
		}
	}

	return nil
}

// extractModel parses the request body JSON to extract the model field
func extractModel(body []byte) string {
	var payload struct {
		Model string `json:"model"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}

	return payload.Model
}

// backendNames returns a list of available backend names
func (r *Router) backendNames() []string {
	names := make([]string, 0, len(r.backends))
	for name := range r.backends {
		names = append(names, name)
	}
	return names
}

// GetBackend returns a backend by name
func (r *Router) GetBackend(name string) (*Backend, bool) {
	b, ok := r.backends[name]
	return b, ok
}

// GetDefaultBackend returns the default backend
func (r *Router) GetDefaultBackend() *Backend {
	return r.defaultBackend
}

// Backends returns all configured backends
func (r *Router) Backends() map[string]*Backend {
	return r.backends
}
