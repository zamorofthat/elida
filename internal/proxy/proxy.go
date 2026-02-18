package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"elida/internal/config"
	"elida/internal/policy"
	"elida/internal/router"
	"elida/internal/session"
	"elida/internal/storage"
	"elida/internal/telemetry"
)

// WebSocketHandler is an interface for the WebSocket handler to avoid import cycle
type WebSocketHandler interface {
	ServeHTTP(w http.ResponseWriter, r *http.Request)
}

// Proxy handles proxying requests to the backend
type Proxy struct {
	config        *config.Config
	store         session.Store
	manager       *session.Manager
	router        *router.Router
	telemetry     *telemetry.Provider
	policy        *policy.Engine
	storage       *storage.SQLiteStore // For persisting flagged sessions immediately
	wsHandler     WebSocketHandler     // WebSocket proxy handler
	captureBuffer *CaptureBuffer       // For capture-all mode (policy-independent)
	captureAll    bool                 // True when capture_mode == "all"
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

	p := &Proxy{
		config:    cfg,
		store:     store,
		manager:   manager,
		router:    r,
		telemetry: tp,
		policy:    pe,
	}

	// Initialize capture-all buffer when storage is enabled with capture_mode="all"
	if cfg.Storage.Enabled && cfg.Storage.CaptureMode == "all" {
		p.captureAll = true
		p.captureBuffer = NewCaptureBuffer(cfg.Storage.MaxCaptureSize, cfg.Storage.MaxCapturedPerSession)
		slog.Info("capture-all mode enabled",
			"max_capture_size", cfg.Storage.MaxCaptureSize,
			"max_per_session", cfg.Storage.MaxCapturedPerSession,
		)
	}

	return p, nil
}

// SetStorage sets the SQLite storage for persisting flagged sessions immediately
func (p *Proxy) SetStorage(s *storage.SQLiteStore) {
	p.storage = s
}

// SetWebSocketHandler sets the WebSocket handler for proxying WebSocket connections
func (p *Proxy) SetWebSocketHandler(h WebSocketHandler) {
	p.wsHandler = h
}

// GetRouter returns the router for use by the WebSocket handler
func (p *Proxy) GetRouter() *router.Router {
	return p.router
}

// GetCaptureBuffer returns the capture buffer (nil if capture-all is not enabled)
func (p *Proxy) GetCaptureBuffer() *CaptureBuffer {
	return p.captureBuffer
}

// IsCaptureAll returns true if capture-all mode is enabled
func (p *Proxy) IsCaptureAll() bool {
	return p.captureAll
}

// isWebSocketRequest checks if the request is a WebSocket upgrade request
func isWebSocketRequest(r *http.Request) bool {
	connection := r.Header.Get("Connection")
	upgrade := r.Header.Get("Upgrade")

	hasUpgrade := strings.Contains(strings.ToLower(connection), "upgrade")
	isWebSocket := strings.EqualFold(upgrade, "websocket")

	return hasUpgrade && isWebSocket
}

// TTSRequestInfo contains parsed TTS request details
type TTSRequestInfo struct {
	Provider string
	Model    string
	Voice    string
	Text     string
}

// isTTSRequest checks if the request is a TTS API call and returns provider info
func isTTSRequest(r *http.Request, body []byte) *TTSRequestInfo {
	path := r.URL.Path

	// OpenAI TTS: POST /v1/audio/speech
	if strings.HasSuffix(path, "/v1/audio/speech") || strings.HasSuffix(path, "/audio/speech") {
		return parseTTSRequest("openai", body)
	}

	// Deepgram Aura: POST /v1/speak
	if strings.HasSuffix(path, "/v1/speak") || strings.HasSuffix(path, "/speak") {
		return parseTTSRequest("deepgram", body)
	}

	// ElevenLabs REST: POST /v1/text-to-speech/{voice_id}
	if strings.Contains(path, "/text-to-speech/") && !strings.Contains(path, "/stream") {
		info := parseTTSRequest("elevenlabs", body)
		// Extract voice ID from path
		parts := strings.Split(path, "/text-to-speech/")
		if len(parts) > 1 {
			voiceID := strings.Split(parts[1], "/")[0]
			info.Voice = voiceID
		}
		return info
	}

	return nil
}

// parseTTSRequest extracts TTS details from the request body
func parseTTSRequest(provider string, body []byte) *TTSRequestInfo {
	info := &TTSRequestInfo{Provider: provider}

	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return info
	}

	// Extract model
	if model, ok := data["model"].(string); ok {
		info.Model = model
	}

	// Extract voice
	if voice, ok := data["voice"].(string); ok {
		info.Voice = voice
	}

	// Extract text/input
	if text, ok := data["text"].(string); ok {
		info.Text = text
	} else if input, ok := data["input"].(string); ok {
		info.Text = input
	}

	return info
}

// ServeHTTP handles incoming requests
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Check for WebSocket upgrade BEFORE reading body
	// Body cannot be read during WebSocket handshake
	if p.wsHandler != nil && isWebSocketRequest(r) {
		p.wsHandler.ServeHTTP(w, r)
		return
	}

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
		// No session ID - use client IP + backend based session tracking
		// Each (client, backend) pair gets its own session for granular control
		sess = p.manager.GetOrCreateByClient(r.RemoteAddr, backend.Name, backend.URL.String())
	}

	if sess == nil {
		// Session was killed - reject request
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		if _, err := w.Write([]byte(`{"error":"session_terminated","message":"Session has been killed and cannot be reused"}`)); err != nil {
			slog.Warn("write failed", "error", err)
		}
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
		if _, err := w.Write([]byte(`{"error":"session_terminated","message":"Session has been killed"}`)); err != nil {
			slog.Warn("write failed", "session_id", sess.ID, "error", err)
		}
		return
	default:
	}

	// Add session ID to response headers
	w.Header().Set(p.config.Session.Header, sess.ID)

	// Content inspection - check request body against policy rules BEFORE forwarding
	if p.policy != nil && len(requestBody) > 0 {
		if result := p.policy.EvaluateContent(sess.ID, string(requestBody)); result != nil {
			// Capture the request content for forensics (before potential early return)
			p.policy.CaptureRequest(sess.ID, policy.CapturedRequest{
				Timestamp:   time.Now(),
				Method:      r.Method,
				Path:        r.URL.Path,
				RequestBody: string(requestBody),
				StatusCode:  http.StatusForbidden,
			})

			if result.ShouldTerminate {
				// Terminate the session for serious violations
				p.manager.Terminate(sess.ID)
				slog.Warn("request blocked and session terminated by policy",
					"session_id", sess.ID,
					"violations", len(result.Violations),
				)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				if _, err := w.Write([]byte(`{"error":"policy_violation","message":"Request violates security policy - session terminated"}`)); err != nil {
					slog.Warn("write failed", "session_id", sess.ID, "error", err)
				}
				return
			}
			if result.ShouldBlock {
				slog.Warn("request blocked by policy",
					"session_id", sess.ID,
					"violations", len(result.Violations),
				)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				if _, err := w.Write([]byte(`{"error":"policy_violation","message":"Request violates security policy"}`)); err != nil {
					slog.Warn("write failed", "session_id", sess.ID, "error", err)
				}
				return
			}
			// Just flagged - continue but log
			slog.Warn("request flagged by policy",
				"session_id", sess.ID,
				"violations", len(result.Violations),
			)
		}
	}

	// Extract tool calls from request (tools being defined or tool results being sent)
	if len(requestBody) > 0 {
		if toolCalls := ExtractToolCalls(requestBody); len(toolCalls) > 0 {
			for _, tc := range toolCalls {
				sess.RecordToolCall(tc.Name, tc.Type, tc.ID)
			}
		}
	}

	// Capture request body for capture-all mode (policy-independent)
	if p.captureAll && p.captureBuffer != nil && len(requestBody) > 0 {
		p.captureBuffer.Capture(sess.ID, CapturedRequest{
			Timestamp:   time.Now(),
			Method:      r.Method,
			Path:        r.URL.Path,
			RequestBody: string(requestBody),
		})
	}

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

	// Track TTS requests
	if p.storage != nil && r.Method == http.MethodPost {
		if ttsInfo := isTTSRequest(r, requestBody); ttsInfo != nil {
			ttsRecord := storage.TTSRequest{
				ID:            uuid.New().String()[:8],
				SessionID:     sess.ID,
				Timestamp:     startTime,
				Provider:      ttsInfo.Provider,
				Model:         ttsInfo.Model,
				Voice:         ttsInfo.Voice,
				Text:          ttsInfo.Text,
				TextLength:    len(ttsInfo.Text),
				ResponseBytes: bytesOut,
				DurationMs:    time.Since(startTime).Milliseconds(),
				StatusCode:    statusCode,
			}

			if err := p.storage.SaveTTSRequest(ttsRecord); err != nil {
				slog.Error("failed to save TTS request", "error", err)
			} else {
				slog.Info("TTS request tracked",
					"session_id", sess.ID,
					"provider", ttsInfo.Provider,
					"voice", ttsInfo.Voice,
					"text_length", len(ttsInfo.Text),
				)
			}
		}
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
		TokensIn:     snap.TokensIn,
		TokensOut:    snap.TokensOut,
		ToolCalls:    snap.ToolCalls,
		ToolFanout:   sess.GetToolFanout(),
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
	return strings.Contains(accept, "text/event-stream")
}

// createBackendRequest creates a new request to the backend
func (p *Proxy) createBackendRequest(r *http.Request, body []byte, backendURL *url.URL) *http.Request {
	targetURL := *backendURL
	targetURL.Path = r.URL.Path
	targetURL.RawQuery = r.URL.RawQuery

	req, _ := http.NewRequestWithContext(r.Context(), r.Method, targetURL.String(), bytes.NewReader(body))

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

	// Scan response body against policy rules BEFORE sending to client
	if p.policy != nil && len(responseBody) > 0 {
		if result := p.policy.EvaluateResponseContent(sess.ID, string(responseBody)); result != nil {
			if result.ShouldTerminate {
				p.manager.Terminate(sess.ID)
				slog.Warn("response blocked and session terminated by policy",
					"session_id", sess.ID,
					"violations", len(result.Violations),
				)
				return p.writeBlockedResponse(w, "Response content violates security policy - session terminated", true)
			}
			if result.ShouldBlock {
				slog.Warn("response blocked by policy",
					"session_id", sess.ID,
					"violations", len(result.Violations),
				)
				return p.writeBlockedResponse(w, "Response content violates security policy", false)
			}
		}
		// Capture response for any flagged session (even if response itself has no violations)
		if p.policy.IsFlagged(sess.ID) {
			p.policy.UpdateLastCaptureWithResponseAndStatus(sess.ID, string(responseBody), resp.StatusCode)
			// Persist flagged session immediately to survive crashes
			p.persistFlaggedSession(sess, backend.Name)
		}
	}

	sess.AddBytes(0, int64(len(responseBody)))

	// Extract token usage from response
	if tokenUsage := ExtractTokenUsage(responseBody); tokenUsage != nil {
		sess.AddTokens(tokenUsage.PromptTokens, tokenUsage.CompletionTokens)
	}

	// Extract tool calls from response (when model decides to call tools)
	if toolCalls := ExtractToolCallsFromResponse(responseBody); len(toolCalls) > 0 {
		for _, tc := range toolCalls {
			sess.RecordToolCall(tc.Name, tc.Type, tc.ID)
		}
	}

	// Capture response for capture-all mode
	if p.captureAll && p.captureBuffer != nil {
		p.captureBuffer.UpdateLastResponse(sess.ID, string(responseBody), resp.StatusCode)
	}

	// Log response (truncated for large responses)
	p.logResponse(sess.ID, responseBody)

	// Write response
	w.WriteHeader(resp.StatusCode)
	if _, err := w.Write(responseBody); err != nil {
		slog.Warn("write failed", "session_id", sess.ID, "error", err)
	}

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

	// Determine streaming format from content type
	contentType := resp.Header.Get("Content-Type")
	isSSE := strings.Contains(contentType, "text/event-stream")

	// Check if we have any response rules to evaluate
	hasBlockingRules := p.policy != nil && p.policy.HasBlockingResponseRules()

	if !hasBlockingRules {
		// No blocking rules - stream directly with async scan for flag-only rules
		return p.handleStreamingDirect(w, resp, sess, backend, isSSE)
	}

	// Determine streaming scan mode from config
	streamingMode := p.config.Policy.Streaming.Mode
	if streamingMode == "" {
		streamingMode = "chunked" // Default to low-latency chunked mode
	}

	switch streamingMode {
	case "buffered":
		// Full buffer mode - accumulate entire response before sending
		// Higher latency but guaranteed pattern detection
		return p.handleStreamingWithBuffer(w, resp, sess, backend, isSSE)
	case "chunked":
		fallthrough
	default:
		// Chunked mode - scan as chunks arrive with overlap buffer
		// Lower latency, real-time termination on detection
		return p.handleStreamingChunked(w, resp, sess, backend, isSSE)
	}
}

// handleStreamingWithBuffer accumulates the full response before sending
// Used when block/terminate response rules are configured (adds latency)
func (p *Proxy) handleStreamingWithBuffer(w http.ResponseWriter, resp *http.Response, sess *session.Session, backend *router.Backend, isSSE bool) (int, int64) {
	// Accumulate entire response before sending
	var buffer bytes.Buffer
	buf := make([]byte, 4096)

	for {
		// Check if session was killed during buffering
		select {
		case <-sess.KillChan():
			slog.Warn("streaming aborted during buffering: session killed", "session_id", sess.ID)
			return resp.StatusCode, int64(buffer.Len())
		default:
		}

		n, err := resp.Body.Read(buf)
		if n > 0 {
			buffer.Write(buf[:n])
		}
		if err != nil {
			if err != io.EOF {
				slog.Error("error reading stream for buffering",
					"session_id", sess.ID,
					"error", err,
				)
			}
			break
		}
	}

	responseBody := buffer.String()

	// Scan accumulated response against policy rules
	if p.policy != nil && len(responseBody) > 0 {
		if result := p.policy.EvaluateResponseContent(sess.ID, responseBody); result != nil {
			if result.ShouldTerminate {
				p.manager.Terminate(sess.ID)
				slog.Warn("streaming response blocked and session terminated",
					"session_id", sess.ID,
					"violations", len(result.Violations),
				)
				return p.writeBlockedResponse(w, "Streaming response violates security policy - session terminated", true)
			}
			if result.ShouldBlock {
				slog.Warn("streaming response blocked",
					"session_id", sess.ID,
					"violations", len(result.Violations),
				)
				return p.writeBlockedResponse(w, "Streaming response violates security policy", false)
			}
		}
		// Capture response for any flagged session (even if response itself has no violations)
		if p.policy.IsFlagged(sess.ID) {
			p.policy.UpdateLastCaptureWithResponseAndStatus(sess.ID, responseBody, resp.StatusCode)
			// Persist flagged session immediately to survive crashes
			p.persistFlaggedSession(sess, backend.Name)
		}
	}

	// Response passed policy check - now send to client
	sess.AddBytes(0, int64(len(responseBody)))

	// Capture response for capture-all mode
	if p.captureAll && p.captureBuffer != nil {
		p.captureBuffer.UpdateLastResponse(sess.ID, responseBody, resp.StatusCode)
	}

	// Copy response headers and send buffered content
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := w.Write(buffer.Bytes()); err != nil {
		slog.Warn("write failed", "session_id", sess.ID, "error", err)
	}

	// Flush if supported
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	slog.Debug("buffered streaming response sent",
		"session_id", sess.ID,
		"size", len(responseBody),
		"is_sse", isSSE,
	)

	return resp.StatusCode, int64(len(responseBody))
}

// handleStreamingChunked scans chunks as they arrive with overlap for cross-boundary patterns
// Low latency: chunks are forwarded immediately, stream terminated on detection
func (p *Proxy) handleStreamingChunked(w http.ResponseWriter, resp *http.Response, sess *session.Session, backend *router.Backend, isSSE bool) (int, int64) {
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

	// Create streaming scanner with overlap buffer
	overlapSize := p.config.Policy.Streaming.OverlapSize
	if overlapSize <= 0 {
		overlapSize = 1024
	}
	scanner := p.policy.NewStreamingScanner(sess.ID, overlapSize)

	var totalBytes int64
	var chunks []string // For logging and capture
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
			chunk := buf[:n]
			totalBytes += int64(n)

			// Scan chunk with overlap buffer for cross-boundary patterns
			if result := scanner.ScanChunk(chunk); result != nil {
				if result.ShouldTerminate {
					// Terminate immediately - send error and close
					p.manager.Terminate(sess.ID)
					slog.Warn("streaming terminated mid-stream by policy",
						"session_id", sess.ID,
						"violations", len(result.Violations),
						"bytes_sent", totalBytes-int64(n), // Bytes already sent before this chunk
					)
					// Write termination message inline (client already received partial response)
					if _, wErr := w.Write([]byte("\n\n[ELIDA: Stream terminated - security policy violation detected]\n")); wErr != nil {
						slog.Warn("write failed", "session_id", sess.ID, "error", wErr)
					}
					flusher.Flush()
					return resp.StatusCode, totalBytes
				}
				if result.ShouldBlock {
					// Block immediately - send error and close
					slog.Warn("streaming blocked mid-stream by policy",
						"session_id", sess.ID,
						"violations", len(result.Violations),
						"bytes_sent", totalBytes-int64(n),
					)
					if _, wErr := w.Write([]byte("\n\n[ELIDA: Stream blocked - security policy violation detected]\n")); wErr != nil {
						slog.Warn("write failed", "session_id", sess.ID, "error", wErr)
					}
					flusher.Flush()
					return resp.StatusCode, totalBytes
				}
				// Just flagged - continue streaming, will capture at end
			}

			// Store chunk for logging (limit stored chunks)
			if len(chunks) < 100 {
				chunks = append(chunks, string(chunk))
			}

			// Forward chunk to client
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

	// Final scan of overlap buffer
	if result := scanner.Finalize(); result != nil {
		// Log any violations found in final scan
		for _, v := range result.Violations {
			slog.Info("final chunk scan: violation detected",
				"session_id", sess.ID,
				"rule", v.RuleName,
				"severity", v.Severity,
			)
		}
	}

	sess.AddBytes(0, totalBytes)

	// Log aggregated streaming response
	p.logStreamingResponse(sess.ID, chunks, isSSE)

	// Capture response for capture-all mode
	if p.captureAll && p.captureBuffer != nil {
		var response strings.Builder
		for _, chunk := range chunks {
			response.WriteString(chunk)
		}
		p.captureBuffer.UpdateLastResponse(sess.ID, response.String(), resp.StatusCode)
	}

	// Capture response for flagged sessions
	if p.policy.IsFlagged(sess.ID) {
		var response strings.Builder
		for _, chunk := range chunks {
			response.WriteString(chunk)
		}
		p.policy.UpdateLastCaptureWithResponseAndStatus(sess.ID, response.String(), resp.StatusCode)
		// Persist flagged session immediately to survive crashes
		p.persistFlaggedSession(sess, backend.Name)
	}

	slog.Debug("chunked streaming response complete",
		"session_id", sess.ID,
		"total_bytes", totalBytes,
		"chunks_scanned", len(chunks),
		"is_sse", isSSE,
	)

	return resp.StatusCode, totalBytes
}

// handleStreamingDirect streams directly to client with async scanning
// Used when only flag actions are configured (no latency impact)
func (p *Proxy) handleStreamingDirect(w http.ResponseWriter, resp *http.Response, sess *session.Session, backend *router.Backend, isSSE bool) (int, int64) {
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

	// Buffer for collecting chunks for logging and async scanning
	var chunks []string
	var totalBytes int64

	// Read and forward chunks
	buf := make([]byte, 4096)
	for {
		// Check if session was killed
		select {
		case <-sess.KillChan():
			slog.Warn("streaming aborted: session killed", "session_id", sess.ID)
			// Still do async scan on what we have
			go p.asyncScanResponse(sess, backend, chunks, resp.StatusCode)
			return resp.StatusCode, totalBytes
		default:
		}

		n, err := resp.Body.Read(buf)
		if n > 0 {
			totalBytes += int64(n)
			chunk := buf[:n]

			// Store chunk for logging and async scanning (limit stored chunks)
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

	// Capture response for capture-all mode
	if p.captureAll && p.captureBuffer != nil {
		var response strings.Builder
		for _, chunk := range chunks {
			response.WriteString(chunk)
		}
		p.captureBuffer.UpdateLastResponse(sess.ID, response.String(), resp.StatusCode)
	}

	// Async scan for flag-only response rules (no latency impact)
	go p.asyncScanResponse(sess, backend, chunks, resp.StatusCode)

	return resp.StatusCode, totalBytes
}

// asyncScanResponse scans response content asynchronously for flag-only rules
func (p *Proxy) asyncScanResponse(sess *session.Session, backend *router.Backend, chunks []string, statusCode int) {
	if p.policy == nil {
		return
	}

	// Reconstruct response from chunks
	var response strings.Builder
	for _, chunk := range chunks {
		response.WriteString(chunk)
	}

	content := response.String()
	if content == "" {
		return
	}

	if result := p.policy.EvaluateResponseContent(sess.ID, content); result != nil {
		// Log violations (already logged in EvaluateResponseContent, but add async context)
		for _, v := range result.Violations {
			slog.Info("async response scan: violation detected",
				"session_id", sess.ID,
				"rule", v.RuleName,
				"severity", v.Severity,
				"action", v.Action,
			)
		}
	}
	// Capture response for flagged sessions (even if response itself has no violations)
	if p.policy.IsFlagged(sess.ID) {
		p.policy.UpdateLastCaptureWithResponseAndStatus(sess.ID, content, statusCode)
		// Persist flagged session immediately to survive crashes
		p.persistFlaggedSession(sess, backend.Name)
	}
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

// writeBlockedResponse writes an error response when content is blocked by policy
func (p *Proxy) writeBlockedResponse(w http.ResponseWriter, message string, terminated bool) (int, int64) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Elida-Blocked", "true")

	response := map[string]interface{}{
		"error":   "response_blocked",
		"message": message,
	}
	if terminated {
		response["session_terminated"] = true
	}

	body, _ := json.Marshal(response)
	w.WriteHeader(http.StatusForbidden)
	if _, err := w.Write(body); err != nil {
		slog.Warn("write failed", "context", "blocked_response", "error", err)
	}

	return http.StatusForbidden, int64(len(body))
}

// ReverseProxy creates a standard reverse proxy using the default backend
func (p *Proxy) ReverseProxy() *httputil.ReverseProxy {
	defaultBackend := p.router.GetDefaultBackend()
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(defaultBackend.URL)
			pr.Out.Host = defaultBackend.URL.Host
		},
		Transport: defaultBackend.Transport,
	}
}

// persistFlaggedSession saves a flagged session to storage immediately
// This ensures forensic data survives crashes
func (p *Proxy) persistFlaggedSession(sess *session.Session, backendName string) {
	if p.storage == nil || p.policy == nil {
		return
	}

	// Only persist if session is flagged
	if !p.policy.IsFlagged(sess.ID) {
		return
	}

	flaggedData := p.policy.GetFlaggedSession(sess.ID)
	if flaggedData == nil {
		return
	}

	// Build session record
	record := storage.SessionRecord{
		ID:           sess.ID,
		State:        "flagged",
		StartTime:    sess.StartTime,
		EndTime:      time.Now(),
		DurationMs:   sess.Duration().Milliseconds(),
		RequestCount: sess.RequestCount,
		BytesIn:      sess.BytesIn,
		BytesOut:     sess.BytesOut,
		Backend:      backendName,
		ClientAddr:   sess.ClientAddr,
	}

	// Add captured content
	for _, c := range flaggedData.CapturedContent {
		record.CapturedContent = append(record.CapturedContent, storage.CapturedRequest{
			Timestamp:    c.Timestamp,
			Method:       c.Method,
			Path:         c.Path,
			RequestBody:  c.RequestBody,
			ResponseBody: c.ResponseBody,
			StatusCode:   c.StatusCode,
		})
	}

	// Add violations
	for _, v := range flaggedData.Violations {
		record.Violations = append(record.Violations, storage.Violation{
			RuleName:    v.RuleName,
			Description: v.Description,
			Severity:    string(v.Severity),
			MatchedText: v.MatchedText,
			Action:      v.Action,
		})
	}

	// Save to storage (upserts if exists)
	if err := p.storage.SaveSession(record); err != nil {
		slog.Error("failed to persist flagged session", "session_id", sess.ID, "error", err)
	} else {
		slog.Debug("persisted flagged session", "session_id", sess.ID, "violations", len(record.Violations))
	}
}
