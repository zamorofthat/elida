package proxy

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"regexp"
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

const (
	// maxRequestBodySize is the maximum request body size (10MB).
	// Prevents OOM from malicious payloads.
	maxRequestBodySize = 10 * 1024 * 1024

	// maxStreamingChunks is the maximum number of streaming response chunks to store
	// for logging, async scanning, and capture. Beyond this, chunks are dropped.
	maxStreamingChunks = 100

	// streamReadBufSize is the buffer size for reading streaming response chunks.
	streamReadBufSize = 4096

	// logTruncateLen is the max length for truncated log output of response bodies.
	logTruncateLen = 1000

	// logPreviewLen is the max length for streaming response content previews.
	logPreviewLen = 500

	// maxFailoverRetries is the maximum number of failover attempts before giving up.
	maxFailoverRetries = 3
)

// WebSocketHandler is an interface for the WebSocket handler to avoid import cycle
type WebSocketHandler interface {
	ServeHTTP(w http.ResponseWriter, r *http.Request)
}

// Proxy handles proxying requests to the backend
type Proxy struct {
	config           *config.Config
	store            session.Store
	manager          *session.Manager
	router           *router.Router
	telemetry        *telemetry.Provider
	policy           *policy.Engine
	storage          *storage.SQLiteStore // For persisting flagged sessions immediately
	wsHandler        WebSocketHandler     // WebSocket proxy handler
	captureBuffer    *CaptureBuffer       // For capture-all mode (policy-independent)
	captureAll       bool                 // True when capture_mode == "all"
	failover         *FailoverController  // Session-aware failover controller
	trustedTagRegexs []*regexp.Regexp     // Pre-compiled regexes for trusted tag stripping
}

// ProxyOption configures a Proxy.
type ProxyOption func(*Proxy)

// WithTelemetry sets the telemetry provider.
func WithTelemetry(tp *telemetry.Provider) ProxyOption {
	return func(p *Proxy) { p.telemetry = tp }
}

// WithPolicyEngine sets the policy engine.
func WithPolicyEngine(pe *policy.Engine) ProxyOption {
	return func(p *Proxy) { p.policy = pe }
}

// WithRouter sets a custom router (overrides config-derived routing).
func WithRouter(r *router.Router) ProxyOption {
	return func(p *Proxy) { p.router = r }
}

// New creates a new proxy handler with the given options.
func New(cfg *config.Config, store session.Store, manager *session.Manager, opts ...ProxyOption) (*Proxy, error) {
	p := &Proxy{
		config:  cfg,
		store:   store,
		manager: manager,
	}

	for _, opt := range opts {
		opt(p)
	}

	// Default telemetry to noop if not provided
	if p.telemetry == nil {
		p.telemetry = telemetry.NoopProvider()
	}

	// Build router from config if not explicitly provided
	if p.router == nil {
		var r *router.Router
		var err error

		if cfg.HasMultiBackend() {
			r, err = router.NewRouter(cfg.Backends, cfg.Routing)
			if err != nil {
				return nil, err
			}
		} else {
			r, err = router.NewSingleBackendRouter(cfg.Backend)
			if err != nil {
				return nil, err
			}
		}
		p.router = r
	}

	// Pre-compile trusted tag regex patterns (avoids re-compilation per request)
	for _, tag := range cfg.Policy.Trust.TrustedTags {
		pattern := fmt.Sprintf(`(?s)<%s>.*?</%s>`, regexp.QuoteMeta(tag), regexp.QuoteMeta(tag))
		p.trustedTagRegexs = append(p.trustedTagRegexs, regexp.MustCompile(pattern))
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

// SetFailoverController sets the failover controller for session-aware failover
func (p *Proxy) SetFailoverController(fc *FailoverController) {
	p.failover = fc
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

	// Proxy authentication check (skip health endpoints)
	if p.config.Proxy.Auth.Enabled && !isHealthEndpoint(r.URL.Path) {
		if !p.validateProxyAuth(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			if _, err := w.Write([]byte(`{"error":"unauthorized","message":"Valid API key required"}`)); err != nil {
				slog.Warn("write failed", "error", err)
			}
			return
		}
	}

	startTime := time.Now()
	ctx := r.Context()

	// Capture request body first (needed for routing and forwarding)
	// Limit to 10MB to prevent OOM from malicious payloads
	var requestBody []byte
	if r.Body != nil {
		var err error
		requestBody, err = io.ReadAll(io.LimitReader(r.Body, maxRequestBodySize))
		if err != nil {
			slog.Error("failed to read request body", "error", err)
			http.Error(w, "Failed to read request body", http.StatusBadRequest)
			return
		}
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
		sess = p.manager.GetOrCreateByClient(session.RealClientAddr(r), backend.Name, backend.URL.String())
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
	sess.TouchAndRecord(int64(len(requestBody)), backend.Name)

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

	// Risk ladder enforcement — check cumulative risk score before processing
	if p.policy != nil {
		if p.policy.ShouldBlockByRisk(sess.ID) {
			slog.Warn("request blocked by risk ladder",
				"session_id", sess.ID,
			)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			if _, err := w.Write([]byte(`{"error":"risk_threshold_exceeded","message":"Session risk score too high"}`)); err != nil {
				slog.Warn("write failed", "session_id", sess.ID, "error", err)
			}
			return
		}
		if shouldThrottle, delayMs := p.policy.ShouldThrottle(sess.ID); shouldThrottle {
			slog.Info("request throttled by risk ladder",
				"session_id", sess.ID,
				"delay_ms", delayMs,
			)
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}
	}

	// Content inspection - check request body against policy rules BEFORE forwarding
	// Smart scanning: cache system prompt hash, re-scan only if changed
	// Always scan: user messages, assistant responses, tool results
	// Trusted tags (e.g., <system-reminder>) are stripped before scanning
	if p.policy != nil && len(requestBody) > 0 {
		allowlistedTools := p.config.Policy.Trust.AllowlistedTools
		contentToScan := extractScannableContent(requestBody, sess, p.trustedTagRegexs, allowlistedTools)
		if contentToScan == "" {
			contentToScan = string(requestBody) // Fallback for non-chat requests
		}
		if result := p.policy.EvaluateContent(sess.ID, contentToScan); result != nil {
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
				// Real-time OTEL block log
				if p.telemetry != nil {
					for _, v := range result.Violations {
						p.telemetry.EmitBlockLog(ctx, sess.ID, v.RuleName, v.MatchedText, backend.Name, "")
					}
				}
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
				// Real-time OTEL block log
				if p.telemetry != nil {
					for _, v := range result.Violations {
						p.telemetry.EmitBlockLog(ctx, sess.ID, v.RuleName, v.MatchedText, backend.Name, "")
					}
				}
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
	backendReq := p.createBackendRequest(r, requestBody, backend).WithContext(ctx)

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

	// Snapshot token counts before request for per-request delta
	tokInBefore, tokOutBefore := sess.GetTokens()

	if isStreaming {
		statusCode, bytesOut = p.handleStreaming(w, backendReq, sess, backend)
	} else {
		statusCode, bytesOut = p.handleStandard(w, backendReq, sess, backend)
	}

	// End telemetry span with metrics
	p.telemetry.EndRequestSpan(span, statusCode, int64(len(requestBody)), bytesOut, nil)

	// Record per-request GenAI metrics (token usage + operation duration)
	if p.telemetry != nil {
		durationSec := time.Since(startTime).Seconds()
		tokInAfter, tokOutAfter := sess.GetTokens()
		reqModel := extractModelFromBody(requestBody)
		hasError := statusCode >= 400

		p.telemetry.RecordTokenUsage(ctx, tokInAfter-tokInBefore, tokOutAfter-tokOutBefore, reqModel, backend.Name)
		p.telemetry.RecordOperationDuration(ctx, durationSec, reqModel, backend.Name, hasError)
	}

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
	// Parse the stream field from the JSON body (handles any valid JSON whitespace)
	if len(body) > 0 {
		var req struct {
			Stream bool `json:"stream"`
		}
		if json.Unmarshal(body, &req) == nil && req.Stream {
			return true
		}
	}

	// Check Accept header for SSE
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "text/event-stream")
}

// createBackendRequest creates a new request to the backend
func (p *Proxy) createBackendRequest(r *http.Request, body []byte, backend *router.Backend) *http.Request {
	targetURL := *backend.URL
	targetURL.Path = r.URL.Path
	targetURL.RawQuery = r.URL.RawQuery

	req, _ := http.NewRequestWithContext(r.Context(), r.Method, targetURL.String(), bytes.NewReader(body))

	// Copy headers (excluding ELIDA internal headers and compression)
	for key, values := range r.Header {
		// Strip ELIDA proxy auth header - don't leak to backend
		if strings.EqualFold(key, "X-Elida-API-Key") {
			continue
		}
		// Strip Accept-Encoding to get uncompressed responses
		// (we don't decompress, so compressed data would be garbled)
		if strings.EqualFold(key, "Accept-Encoding") {
			continue
		}
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// Inject backend API key if configured (enables keyless clients)
	if backend.APIKey != "" {
		switch backend.Type {
		case "anthropic":
			req.Header.Set("x-api-key", backend.APIKey)
		case "openai", "groq":
			req.Header.Set("Authorization", "Bearer "+backend.APIKey)
		default:
			// For unknown types, try both common patterns
			if req.Header.Get("x-api-key") == "" && req.Header.Get("Authorization") == "" {
				req.Header.Set("Authorization", "Bearer "+backend.APIKey)
			}
		}
		slog.Debug("injected backend API key", "backend", backend.Name, "type", backend.Type)
	}

	// Set host header
	req.Host = backend.URL.Host

	return req
}

// handleStandard handles non-streaming requests
func (p *Proxy) handleStandard(w http.ResponseWriter, req *http.Request, sess *session.Session, backend *router.Backend) (int, int64) {
	resp, err := backend.Transport.RoundTrip(req)

	// Check for failover conditions
	failureType := DetectFailure(resp, err)
	if failureType != FailureNone && p.failover != nil && p.failover.IsEnabled() {
		statusCode, bytesOut, retried := p.attemptFailover(w, req, sess, backend, failureType)
		if retried {
			return statusCode, bytesOut
		}
		// Failover failed or not possible, continue with error handling
	}

	if err != nil {
		slog.Error("backend request failed",
			"session_id", sess.ID,
			"error", err,
		)
		http.Error(w, "Backend unavailable", http.StatusBadGateway)
		return http.StatusBadGateway, 0
	}
	defer func() { _ = resp.Body.Close() }()

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
		// Evaluate tool calls against tool call policy rules
		if p.policy != nil {
			policyToolCalls := proxyToolCallsToPolicyToolCalls(toolCalls)
			if result := p.policy.EvaluateToolCalls(sess.ID, policyToolCalls); result != nil {
				if result.ShouldTerminate {
					p.manager.Terminate(sess.ID)
					return p.writeBlockedResponse(w, "Tool call violates security policy - session terminated", true)
				}
				if result.ShouldBlock {
					return p.writeBlockedResponse(w, "Tool call violates security policy", false)
				}
			}
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
	defer func() { _ = resp.Body.Close() }()

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
	buf := make([]byte, streamReadBufSize)

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

	// Evaluate tool calls in buffered streaming response
	if p.policy != nil && len(responseBody) > 0 {
		if toolCalls := ExtractToolCallsFromResponse([]byte(responseBody)); len(toolCalls) > 0 {
			for _, tc := range toolCalls {
				sess.RecordToolCall(tc.Name, tc.Type, tc.ID)
			}
			policyToolCalls := proxyToolCallsToPolicyToolCalls(toolCalls)
			if result := p.policy.EvaluateToolCalls(sess.ID, policyToolCalls); result != nil {
				if result.ShouldTerminate {
					p.manager.Terminate(sess.ID)
					return p.writeBlockedResponse(w, "Tool call violates security policy - session terminated", true)
				}
				if result.ShouldBlock {
					return p.writeBlockedResponse(w, "Tool call violates security policy", false)
				}
			}
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
	buf := make([]byte, streamReadBufSize)

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
			if len(chunks) < maxStreamingChunks {
				chunks = append(chunks, string(chunk))
			} else if len(chunks) == maxStreamingChunks {
				slog.Warn("streaming chunk limit reached, further chunks will not be captured",
					"session_id", sess.ID,
					"limit", maxStreamingChunks,
				)
				chunks = append(chunks, "") // sentinel to prevent repeated warnings
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

	// Reconstruct response once for capture and flagging
	reconstructed := joinChunks(chunks)

	// Capture response for capture-all mode
	if p.captureAll && p.captureBuffer != nil {
		p.captureBuffer.UpdateLastResponse(sess.ID, reconstructed, resp.StatusCode)
	}

	// Capture response for flagged sessions
	if p.policy.IsFlagged(sess.ID) {
		p.policy.UpdateLastCaptureWithResponseAndStatus(sess.ID, reconstructed, resp.StatusCode)
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
	buf := make([]byte, streamReadBufSize)
	for {
		// Check if session was killed
		select {
		case <-sess.KillChan():
			slog.Warn("streaming aborted: session killed", "session_id", sess.ID)
			// Record tool calls on live session before async goroutine
			abortContent := joinChunks(chunks)
			if toolCalls := ExtractToolCallsFromResponse([]byte(abortContent)); len(toolCalls) > 0 {
				for _, tc := range toolCalls {
					sess.RecordToolCall(tc.Name, tc.Type, tc.ID)
				}
			}
			abortSnap := sess.Snapshot()
			go p.asyncScanResponse(sess.ID, &abortSnap, backend, chunks, resp.StatusCode)
			return resp.StatusCode, totalBytes
		default:
		}

		n, err := resp.Body.Read(buf)
		if n > 0 {
			totalBytes += int64(n)
			chunk := buf[:n]

			// Store chunk for logging and async scanning (limit stored chunks)
			if len(chunks) < maxStreamingChunks {
				chunks = append(chunks, string(chunk))
			} else if len(chunks) == maxStreamingChunks {
				slog.Warn("streaming chunk limit reached, further chunks will not be captured",
					"session_id", sess.ID,
					"limit", maxStreamingChunks,
				)
				chunks = append(chunks, "") // sentinel to prevent repeated warnings
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

	// Reconstruct response once for capture and tool call extraction
	responseContent := joinChunks(chunks)

	// Capture response for capture-all mode
	if p.captureAll && p.captureBuffer != nil {
		p.captureBuffer.UpdateLastResponse(sess.ID, responseContent, resp.StatusCode)
	}

	// Record tool calls on the live session before launching the async goroutine
	// to avoid mutating session state from a detached goroutine
	if toolCalls := ExtractToolCallsFromResponse([]byte(responseContent)); len(toolCalls) > 0 {
		for _, tc := range toolCalls {
			sess.RecordToolCall(tc.Name, tc.Type, tc.ID)
		}
	}

	// Async scan for flag-only response rules (no latency impact)
	// Pass session ID and snapshot — not the live session pointer — to avoid
	// accessing a session that may be cleaned up by the manager
	sessionID := sess.ID
	sessSnap := sess.Snapshot()
	go p.asyncScanResponse(sessionID, &sessSnap, backend, chunks, resp.StatusCode)

	return resp.StatusCode, totalBytes
}

// asyncScanResponse scans response content asynchronously for flag-only rules.
// Takes a session ID and snapshot instead of a live session pointer to avoid
// races with session cleanup.
func (p *Proxy) asyncScanResponse(sessionID string, sessSnap *session.Session, backend *router.Backend, chunks []string, statusCode int) {
	if p.policy == nil {
		return
	}

	content := joinChunks(chunks)
	if content == "" {
		return
	}

	if result := p.policy.EvaluateResponseContent(sessionID, content); result != nil {
		for _, v := range result.Violations {
			slog.Info("async response scan: violation detected",
				"session_id", sessionID,
				"rule", v.RuleName,
				"severity", v.Severity,
				"action", v.Action,
			)
		}
	}
	// Evaluate tool calls in async-scanned response
	if toolCalls := ExtractToolCallsFromResponse([]byte(content)); len(toolCalls) > 0 {
		policyToolCalls := proxyToolCallsToPolicyToolCalls(toolCalls)
		if result := p.policy.EvaluateToolCalls(sessionID, policyToolCalls); result != nil {
			for _, v := range result.Violations {
				slog.Info("async tool call scan: violation detected",
					"session_id", sessionID,
					"rule", v.RuleName,
					"severity", v.Severity,
					"action", v.Action,
				)
			}
		}
	}
	// Capture response for flagged sessions (even if response itself has no violations)
	if p.policy.IsFlagged(sessionID) {
		p.policy.UpdateLastCaptureWithResponseAndStatus(sessionID, content, statusCode)
		// Persist flagged session immediately to survive crashes
		p.persistFlaggedSession(sessSnap, backend.Name)
	}
}

// logResponse logs a standard response
// proxyToolCallsToPolicyToolCalls converts proxy ToolCallInfo to policy ToolCall
func proxyToolCallsToPolicyToolCalls(toolCalls []ToolCallInfo) []policy.ToolCall {
	result := make([]policy.ToolCall, len(toolCalls))
	for i, tc := range toolCalls {
		result[i] = policy.ToolCall{
			Name:      tc.Name,
			Arguments: tc.Arguments,
		}
	}
	return result
}

func (p *Proxy) logResponse(sessionID string, body []byte) {
	// Truncate for logging if too large
	logBody := string(body)
	if len(logBody) > logTruncateLen {
		logBody = logBody[:logTruncateLen] + "...[truncated]"
	}

	slog.Debug("response",
		"session_id", sessionID,
		"size", len(body),
		"body", logBody,
	)
}

// logStreamingResponse logs an aggregated streaming response
func (p *Proxy) logStreamingResponse(sessionID string, chunks []string, isSSE bool) {
	// For SSE, extract meaningful content; for NDJSON, use raw data
	content := joinChunks(chunks)
	if isSSE {
		content = parseSSEContent(content)
	}

	slog.Debug("streaming response complete",
		"session_id", sessionID,
		"chunks", len(chunks),
		"content_preview", truncate(content, logPreviewLen),
	)
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

// joinChunks reconstructs a full response string from streaming chunks.
func joinChunks(chunks []string) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(c)
	}
	return b.String()
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

// attemptFailover tries to failover to another backend after a failure
// Returns (statusCode, bytesOut, retried) where retried indicates if failover was attempted
func (p *Proxy) attemptFailover(w http.ResponseWriter, originalReq *http.Request, sess *session.Session, failedBackend *router.Backend, failureType FailureType) (int, int64, bool) {
	return p.attemptFailoverWithDepth(w, originalReq, sess, failedBackend, failureType, 0)
}

func (p *Proxy) attemptFailoverWithDepth(w http.ResponseWriter, originalReq *http.Request, sess *session.Session, failedBackend *router.Backend, failureType FailureType, depth int) (int, int64, bool) {
	if depth >= maxFailoverRetries {
		slog.Error("failover exhausted max retries",
			"session_id", sess.ID,
			"max_retries", maxFailoverRetries,
			"last_backend", failedBackend.Name,
		)
		http.Error(w, "All backends unavailable", http.StatusBadGateway)
		return http.StatusBadGateway, 0, true
	}
	ctx := originalReq.Context()

	// Get next available backend
	fallbackInfo, err := p.failover.HandleFailover(ctx, sess, failedBackend.Name, failureType)
	if err != nil {
		slog.Warn("failover failed",
			"session_id", sess.ID,
			"failed_backend", failedBackend.Name,
			"error", err,
		)
		return 0, 0, false
	}

	// Get the actual backend from the router
	fallbackBackend, ok := p.router.GetBackend(fallbackInfo.Name)
	if !ok {
		slog.Error("failover backend not found in router",
			"session_id", sess.ID,
			"backend", fallbackInfo.Name,
		)
		return 0, 0, false
	}

	// Get session state for rehydration
	state := sess.Serialize()

	// Get the appropriate rehydrator for the target backend
	rehydrator := GetRehydrator(fallbackInfo.Type)

	// Rehydrate the request with full conversation history
	newReq, err := rehydrator.Rehydrate(state, originalReq)
	if err != nil {
		slog.Error("failed to rehydrate request for failover",
			"session_id", sess.ID,
			"target_backend", fallbackInfo.Name,
			"error", err,
		)
		return 0, 0, false
	}

	// Update request URL for new backend
	newReq.URL.Scheme = fallbackBackend.URL.Scheme
	newReq.URL.Host = fallbackBackend.URL.Host
	newReq.Host = fallbackBackend.URL.Host

	slog.Info("executing failover request",
		"session_id", sess.ID,
		"from_backend", failedBackend.Name,
		"to_backend", fallbackInfo.Name,
		"messages", len(state.Messages),
	)

	// Record the backend switch
	sess.RecordBackend(fallbackInfo.Name)

	// Execute request on new backend
	resp, err := fallbackBackend.Transport.RoundTrip(newReq)
	if err != nil {
		// Failover also failed - check if we should try another
		failureType := DetectFailure(resp, err)
		if failureType != FailureNone {
			// Try next fallback
			return p.attemptFailoverWithDepth(w, originalReq, sess, fallbackBackend, failureType, depth+1)
		}
		slog.Error("failover request failed",
			"session_id", sess.ID,
			"backend", fallbackInfo.Name,
			"error", err,
		)
		http.Error(w, "Backend unavailable after failover", http.StatusBadGateway)
		return http.StatusBadGateway, 0, true
	}
	defer func() { _ = resp.Body.Close() }()

	// Copy response headers
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// Read and process response
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Error("failed to read failover response",
			"session_id", sess.ID,
			"error", err,
		)
		http.Error(w, "Failed to read backend response", http.StatusBadGateway)
		return http.StatusBadGateway, 0, true
	}

	sess.AddBytes(0, int64(len(responseBody)))

	// Extract token usage from response
	if tokenUsage := ExtractTokenUsage(responseBody); tokenUsage != nil {
		sess.AddTokens(tokenUsage.PromptTokens, tokenUsage.CompletionTokens)
	}

	// Write response
	w.WriteHeader(resp.StatusCode)
	if _, err := w.Write(responseBody); err != nil {
		slog.Warn("write failed", "session_id", sess.ID, "error", err)
	}

	slog.Info("failover request completed",
		"session_id", sess.ID,
		"backend", fallbackInfo.Name,
		"status", resp.StatusCode,
		"bytes", len(responseBody),
	)

	return resp.StatusCode, int64(len(responseBody)), true
}

// isHealthEndpoint returns true if the path is a health check endpoint
func isHealthEndpoint(path string) bool {
	return path == "/health" || path == "/healthz" || path == "/ready" || path == "/readyz"
}

// validateProxyAuth checks if the request has valid proxy authentication
// Uses constant-time comparison to prevent timing attacks
func (p *Proxy) validateProxyAuth(r *http.Request) bool {
	expectedKey := p.config.Proxy.Auth.APIKey
	if expectedKey == "" {
		return false
	}

	// Check X-Elida-API-Key header (preferred - doesn't conflict with backend auth)
	if secureCompare(r.Header.Get("X-Elida-API-Key"), expectedKey) {
		return true
	}

	// Check Authorization: Bearer <token> (if not already used for backend auth)
	// Note: Anthropic uses x-api-key, not Bearer, so this is safe
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if secureCompare(token, expectedKey) {
			return true
		}
	}

	return false
}

// secureCompare performs constant-time string comparison to prevent timing attacks
func secureCompare(provided, expected string) bool {
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
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

	// Build session record using Snapshot() for thread-safe field access
	snap := sess.Snapshot()
	record := storage.SessionRecord{
		ID:           snap.ID,
		State:        "flagged",
		StartTime:    snap.StartTime,
		EndTime:      time.Now(),
		DurationMs:   snap.Duration().Milliseconds(),
		RequestCount: snap.RequestCount,
		BytesIn:      snap.BytesIn,
		BytesOut:     snap.BytesOut,
		Backend:      backendName,
		ClientAddr:   snap.ClientAddr,
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

// extractScannableContent parses the request body and returns content to scan.
// Caches system prompt hash to avoid re-scanning identical prompts.
// Always scans: user messages, assistant responses, tool results.
// Skips: system prompts that match the cached hash.
// Strips content within trusted tags (e.g., <system-reminder>...</system-reminder>).
func extractScannableContent(body []byte, sess *session.Session, trustedTagRegexs []*regexp.Regexp, allowlistedTools ...[]string) string {
	// Try to parse as chat completion request
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"` // Can be string or array of content blocks
		} `json:"messages"`
	}

	if err := json.Unmarshal(body, &req); err != nil || len(req.Messages) == 0 {
		return "" // Not a chat request, fallback to full body
	}

	// Check if request contains only allowlisted tool usage — skip scanning if so
	var allowed []string
	if len(allowlistedTools) > 0 {
		allowed = allowlistedTools[0]
	}
	if len(allowed) > 0 && containsOnlyAllowlistedTools(req.Messages, allowed) {
		slog.Debug("skipping content scan — allowlisted tools only", "session_id", sess.ID)
		return ""
	}

	var scannableContent strings.Builder
	cachedHash := sess.GetSystemPromptHash()

	for _, msg := range req.Messages {
		content := extractMessageContent(msg.Content)
		if content == "" {
			continue
		}

		// Strip content within trusted tags before scanning
		if len(trustedTagRegexs) > 0 {
			content = stripTrustedTags(content, trustedTagRegexs)
		}

		if msg.Role == "system" {
			// Calculate hash of system prompt (after stripping trusted tags)
			hash := hashContent(content)

			if cachedHash == "" {
				// First time seeing system prompt - scan it and cache hash
				sess.SetSystemPromptHash(hash)
				scannableContent.WriteString(content)
				scannableContent.WriteString("\n")
				slog.Debug("cached system prompt hash", "session_id", sess.ID, "hash", hash[:16])
			} else if hash != cachedHash {
				// System prompt changed - scan it and update cache
				sess.SetSystemPromptHash(hash)
				scannableContent.WriteString(content)
				scannableContent.WriteString("\n")
				slog.Warn("system prompt changed mid-session", "session_id", sess.ID, "old_hash", cachedHash[:16], "new_hash", hash[:16])
			}
			// If hash matches cache, skip scanning (already validated)
		} else {
			// User, assistant, tool - always scan
			scannableContent.WriteString(content)
			scannableContent.WriteString("\n")
		}
	}

	return scannableContent.String()
}

// containsOnlyAllowlistedTools checks if the most recent assistant message only uses allowlisted tools.
// If any tool_use block references a non-allowlisted tool, returns false.
func containsOnlyAllowlistedTools(messages []struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}, allowlisted []string) bool {
	allowSet := make(map[string]bool, len(allowlisted))
	for _, t := range allowlisted {
		allowSet[strings.ToLower(t)] = true
	}

	// Check the last assistant message for tool_use blocks
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "assistant" {
			continue
		}
		blocks, ok := messages[i].Content.([]any)
		if !ok {
			return false
		}
		hasToolUse := false
		for _, block := range blocks {
			m, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if m["type"] == "tool_use" {
				hasToolUse = true
				name, _ := m["name"].(string)
				if !allowSet[strings.ToLower(name)] {
					return false
				}
			}
		}
		return hasToolUse // Only check the most recent assistant message
	}
	return false
}

// extractMessageContent extracts text from message content (handles string or content blocks)
func extractMessageContent(content any) string {
	// Simple string content
	if s, ok := content.(string); ok {
		return s
	}

	// Array of content blocks (Anthropic/OpenAI vision format)
	if blocks, ok := content.([]any); ok {
		var result strings.Builder
		for _, block := range blocks {
			if m, ok := block.(map[string]any); ok {
				if text, ok := m["text"].(string); ok {
					result.WriteString(text)
					result.WriteString(" ")
				}
			}
		}
		return result.String()
	}

	return ""
}

// extractModelFromBody extracts the "model" field from a JSON request body
func extractModelFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var req struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &req) == nil {
		return req.Model
	}
	return ""
}

// hashContent returns a SHA256 hash of the content
func hashContent(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h)
}

// stripTrustedTags removes content within trusted XML-style tags using
// pre-compiled regex patterns. For example, with trustedTags=["system-reminder"],
// it removes: <system-reminder>...any content...</system-reminder>
// This prevents false positives from scanning trusted system content.
func stripTrustedTags(content string, compiledRegexes []*regexp.Regexp) string {
	if len(compiledRegexes) == 0 {
		return content
	}

	result := content
	for _, re := range compiledRegexes {
		result = re.ReplaceAllString(result, "")
	}

	return result
}
