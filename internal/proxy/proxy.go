package proxy

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"elida/internal/config"
	"elida/internal/policy"
	"elida/internal/router"
	"elida/internal/session"
	"elida/internal/telemetry"
)

// Proxy handles proxying requests to the backend
type Proxy struct {
	config    *config.Config
	store     session.Store
	manager   *session.Manager
	router    *router.Router
	telemetry *telemetry.Provider
	policy    *policy.Engine
}

// New creates a new proxy handler
func New(cfg *config.Config, store session.Store, manager *session.Manager) (*Proxy, error) {
	return NewWithPolicy(cfg, store, manager, nil, nil)
}

// NewWithTelemetry creates a new proxy handler with telemetry support
func NewWithTelemetry(cfg *config.Config, store session.Store, manager *session.Manager, tp *telemetry.Provider) (*Proxy, error) {
	return NewWithPolicy(cfg, store, manager, tp, nil)
}

// NewWithPolicy creates a new proxy handler with telemetry and policy support
func NewWithPolicy(cfg *config.Config, store session.Store, manager *session.Manager, tp *telemetry.Provider, pe *policy.Engine) (*Proxy, error) {
	// Create router based on config
	var r *router.Router
	var err error

	if cfg.HasMultiBackend() {
		// Multi-backend configuration
		r, err = router.NewRouter(cfg.Backends, cfg.Routing)
		if err != nil {
			return nil, err
		}
	} else {
		// Single backend (backward compatibility)
		r, err = router.NewSingleBackendRouter(cfg.Backend)
		if err != nil {
			return nil, err
		}
	}

	return NewWithRouter(cfg, store, manager, tp, pe, r)
}

// NewWithRouter creates a new proxy handler with a custom router
func NewWithRouter(cfg *config.Config, store session.Store, manager *session.Manager, tp *telemetry.Provider, pe *policy.Engine, r *router.Router) (*Proxy, error) {
	// Use noop provider if none provided
	if tp == nil {
		tp = telemetry.NoopProvider()
	}

	return &Proxy{
		config:    cfg,
		store:     store,
		manager:   manager,
		router:    r,
		telemetry: tp,
		policy:    pe,
	}, nil
}

// ServeHTTP handles incoming requests
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// Capture request body first (needed for routing and forwarding)
	var requestBody []byte
	if r.Body != nil {
		requestBody, _ = io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(requestBody))
	}

	// Select backend using router
	backend, err := p.router.Select(r, requestBody)
	if err != nil {
		slog.Error("failed to select backend", "error", err)
		http.Error(w, "Failed to select backend", http.StatusInternalServerError)
		return
	}

	// Get or create session
	sessionID := r.Header.Get(p.config.Session.Header)

	var sess *session.Session
	if sessionID != "" {
		// Explicit session ID provided - use it
		sess = p.manager.GetOrCreate(sessionID, backend.URL.String(), r.RemoteAddr)
	} else if p.config.Session.GenerateIfMissing {
		// No session ID - use client IP-based session tracking
		// This groups all requests from the same client (e.g., Claude Code) into one session
		sess = p.manager.GetOrCreateByClient(r.RemoteAddr, backend.URL.String())
	}

	if sess == nil {
		// Session was killed - reject request
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"session_terminated","message":"Session has been killed and cannot be reused"}`))
		return
	}
	sess.Touch()
	sess.AddBytes(int64(len(requestBody)), 0)
	sess.RecordBackend(backend.Name)

	// Check if session was killed
	select {
	case <-sess.KillChan():
		slog.Warn("request rejected: session killed",
			"session_id", sess.ID,
			"path", r.URL.Path,
		)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"session_terminated","message":"Session has been killed"}`))
		return
	default:
	}

	// Add session ID to response headers
	w.Header().Set(p.config.Session.Header, sess.ID)

	// Determine if this is a streaming request
	isStreaming := p.isStreamingRequest(r, requestBody)

	// Start telemetry span
	ctx, span := p.telemetry.StartRequestSpan(ctx, sess.ID, r.Method, r.URL.Path, isStreaming)
	defer span.End()

	// Create the backend request with context
	backendReq := p.createBackendRequest(r, requestBody, backend.URL).WithContext(ctx)

	// Log the request
	slog.Info("proxying request",
		"session_id", sess.ID,
		"method", r.Method,
		"path", r.URL.Path,
		"backend", backend.Name,
		"streaming", isStreaming,
	)

	var statusCode int
	var bytesOut int64

	if isStreaming {
		statusCode, bytesOut = p.handleStreaming(w, backendReq, sess, backend)
	} else {
		statusCode, bytesOut = p.handleStandard(w, backendReq, sess, backend)
	}

	// End telemetry span with metrics
	p.telemetry.EndRequestSpan(span, statusCode, int64(len(requestBody)), bytesOut, nil)

	// Evaluate policy rules
	if p.policy != nil {
		p.evaluatePolicy(sess, r.Method, r.URL.Path, requestBody, statusCode)
	}

	slog.Info("request completed",
		"session_id", sess.ID,
		"duration", time.Since(startTime),
		"path", r.URL.Path,
		"backend", backend.Name,
	)
}

// evaluatePolicy checks the session against policy rules
func (p *Proxy) evaluatePolicy(sess *session.Session, method, path string, requestBody []byte, statusCode int) {
	snap := sess.Snapshot()

	metrics := policy.SessionMetrics{
		SessionID:    snap.ID,
		BytesIn:      snap.BytesIn,
		BytesOut:     snap.BytesOut,
		RequestCount: snap.RequestCount,
		Duration:     sess.Duration(),
		IdleTime:     sess.IdleTime(),
		StartTime:    snap.StartTime,
		RequestTimes: sess.GetRequestTimes(),
	}

	violations := p.policy.Evaluate(metrics)

	if len(violations) > 0 {
		for _, v := range violations {
			slog.Warn("policy violation detected",
				"session_id", sess.ID,
				"rule", v.RuleName,
				"severity", v.Severity,
				"threshold", v.Threshold,
				"actual", v.ActualValue,
			)
		}

		// Capture the request for flagged sessions
		p.policy.CaptureRequest(sess.ID, policy.CapturedRequest{
			Timestamp:   time.Now(),
			Method:      method,
			Path:        path,
			RequestBody: string(requestBody),
			StatusCode:  statusCode,
		})
	}
}

// isStreamingRequest determines if the request expects a streaming response
func (p *Proxy) isStreamingRequest(r *http.Request, body []byte) bool {
	// Check for stream parameter in body (common for chat completions)
	if len(body) > 0 {
		// Simple check - look for "stream":true or "stream": true
		bodyStr := string(body)
		if strings.Contains(bodyStr, `"stream":true`) || strings.Contains(bodyStr, `"stream": true`) {
			return true
		}
	}

	// Check Accept header for SSE
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "text/event-stream") {
		return true
	}

	return false
}

// createBackendRequest creates a new request to the backend
func (p *Proxy) createBackendRequest(r *http.Request, body []byte, backendURL *url.URL) *http.Request {
	targetURL := *backendURL
	targetURL.Path = r.URL.Path
	targetURL.RawQuery = r.URL.RawQuery

	req, _ := http.NewRequest(r.Method, targetURL.String(), bytes.NewReader(body))

	// Copy headers
	for key, values := range r.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// Set host header
	req.Host = backendURL.Host

	return req
}

// handleStandard handles non-streaming requests
func (p *Proxy) handleStandard(w http.ResponseWriter, req *http.Request, sess *session.Session, backend *router.Backend) (int, int64) {
	resp, err := backend.Transport.RoundTrip(req)
	if err != nil {
		slog.Error("backend request failed",
			"session_id", sess.ID,
			"error", err,
		)
		http.Error(w, "Backend unavailable", http.StatusBadGateway)
		return http.StatusBadGateway, 0
	}
	defer resp.Body.Close()

	// Copy response headers
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// Read response body for logging/metrics
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Error("failed to read response",
			"session_id", sess.ID,
			"error", err,
		)
		http.Error(w, "Failed to read backend response", http.StatusBadGateway)
		return http.StatusBadGateway, 0
	}

	sess.AddBytes(0, int64(len(responseBody)))

	// Log response (truncated for large responses)
	p.logResponse(sess.ID, responseBody)

	// Write response
	w.WriteHeader(resp.StatusCode)
	w.Write(responseBody)

	return resp.StatusCode, int64(len(responseBody))
}

// handleStreaming handles streaming responses (SSE and NDJSON)
func (p *Proxy) handleStreaming(w http.ResponseWriter, req *http.Request, sess *session.Session, backend *router.Backend) (int, int64) {
	resp, err := backend.Transport.RoundTrip(req)
	if err != nil {
		slog.Error("backend request failed",
			"session_id", sess.ID,
			"error", err,
		)
		http.Error(w, "Backend unavailable", http.StatusBadGateway)
		return http.StatusBadGateway, 0
	}
	defer resp.Body.Close()

	// Copy response headers
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// Get flusher for streaming
	flusher, ok := w.(http.Flusher)
	if !ok {
		slog.Error("response writer does not support flushing", "session_id", sess.ID)
		return resp.StatusCode, 0
	}

	// Determine streaming format from content type
	contentType := resp.Header.Get("Content-Type")
	isSSE := strings.Contains(contentType, "text/event-stream")
	isNDJSON := strings.Contains(contentType, "application/x-ndjson") ||
		strings.Contains(contentType, "application/json")

	slog.Debug("streaming response",
		"session_id", sess.ID,
		"content_type", contentType,
		"is_sse", isSSE,
		"is_ndjson", isNDJSON,
	)

	// Buffer for collecting chunks for logging
	var chunks []string
	var totalBytes int64

	// Read and forward chunks
	buf := make([]byte, 4096)
	for {
		// Check if session was killed
		select {
		case <-sess.KillChan():
			slog.Warn("streaming aborted: session killed", "session_id", sess.ID)
			return resp.StatusCode, totalBytes
		default:
		}

		n, err := resp.Body.Read(buf)
		if n > 0 {
			totalBytes += int64(n)
			chunk := buf[:n]

			// Store chunk for logging (limit stored chunks)
			if len(chunks) < 100 {
				chunks = append(chunks, string(chunk))
			}

			// Write to client
			_, writeErr := w.Write(chunk)
			if writeErr != nil {
				slog.Error("failed to write chunk",
					"session_id", sess.ID,
					"error", writeErr,
				)
				return resp.StatusCode, totalBytes
			}
			flusher.Flush()
		}

		if err != nil {
			if err != io.EOF {
				slog.Error("error reading stream",
					"session_id", sess.ID,
					"error", err,
				)
			}
			break
		}
	}

	sess.AddBytes(0, totalBytes)

	// Log aggregated streaming response
	p.logStreamingResponse(sess.ID, chunks, isSSE)

	return resp.StatusCode, totalBytes
}

// logResponse logs a standard response
func (p *Proxy) logResponse(sessionID string, body []byte) {
	// Truncate for logging if too large
	logBody := string(body)
	if len(logBody) > 1000 {
		logBody = logBody[:1000] + "...[truncated]"
	}

	slog.Debug("response",
		"session_id", sessionID,
		"size", len(body),
		"body", logBody,
	)
}

// logStreamingResponse logs an aggregated streaming response
func (p *Proxy) logStreamingResponse(sessionID string, chunks []string, isSSE bool) {
	// Reconstruct response from chunks
	var response strings.Builder
	for _, chunk := range chunks {
		response.WriteString(chunk)
	}

	// For SSE, parse out the actual content
	if isSSE {
		content := parseSSEContent(response.String())
		slog.Debug("streaming response complete",
			"session_id", sessionID,
			"chunks", len(chunks),
			"content_preview", truncate(content, 500),
		)
	} else {
		// NDJSON - parse out content
		content := parseNDJSONContent(response.String())
		slog.Debug("streaming response complete",
			"session_id", sessionID,
			"chunks", len(chunks),
			"content_preview", truncate(content, 500),
		)
	}
}

// parseSSEContent extracts content from SSE format
func parseSSEContent(data string) string {
	var content strings.Builder
	lines := strings.Split(data, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			if payload == "[DONE]" {
				continue
			}
			// For now, just append the data line
			// In future, could parse JSON and extract actual content
			content.WriteString(payload)
		}
	}
	return content.String()
}

// parseNDJSONContent extracts content from NDJSON format (Ollama style)
func parseNDJSONContent(data string) string {
	// For now, just return the raw data
	// In future, could parse each JSON line and extract response/message content
	return data
}

// truncate truncates a string to maxLen
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ReverseProxy creates a standard reverse proxy using the default backend
func (p *Proxy) ReverseProxy() *httputil.ReverseProxy {
	defaultBackend := p.router.GetDefaultBackend()
	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = defaultBackend.URL.Scheme
			req.URL.Host = defaultBackend.URL.Host
			req.Host = defaultBackend.URL.Host
		},
		Transport: defaultBackend.Transport,
	}
}
