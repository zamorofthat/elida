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
	Type      string   // ollama, openai, anthropic, mistral
	Models    []string // glob patterns for model matching
	Default   bool
	Transport *http.Transport
}

// Router handles routing requests to multiple backends
type Router struct {
	backends       map[string]*Backend
	defaultBackend *Backend
	methods        []string // routing method priority: header, model, path, default
}

// NewRouter creates a new multi-backend router from configuration
func NewRouter(backends map[string]config.BackendConfig, routing config.RoutingConfig) (*Router, error) {
	if len(backends) == 0 {
		return nil, fmt.Errorf("no backends configured")
	}

	r := &Router{
		backends: make(map[string]*Backend),
		methods:  routing.Methods,
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

		backend := &Backend{
			Name:    name,
			URL:     backendURL,
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
	)

	return r, nil
}

// NewSingleBackendRouter creates a router with a single backend (backward compatibility)
func NewSingleBackendRouter(backendURL string) (*Router, error) {
	parsed, err := url.Parse(backendURL)
	if err != nil {
		return nil, fmt.Errorf("invalid backend URL: %w", err)
	}

	backend := &Backend{
		Name:    "default",
		URL:     parsed,
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

// Select chooses the appropriate backend for a request
func (r *Router) Select(req *http.Request, body []byte) (*Backend, error) {
	for _, method := range r.methods {
		var backend *Backend

		switch method {
		case "header":
			backend = r.matchByHeader(req)
		case "model":
			backend = r.matchByModel(body)
		case "path":
			backend = r.matchByPath(req)
		case "default":
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
