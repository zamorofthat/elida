package websocket

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"elida/internal/config"
	"elida/internal/policy"
	"elida/internal/router"
	"elida/internal/session"
)

// VoiceSessionCallback is called when voice sessions start/end
type VoiceSessionCallback func(wsSession *session.Session, voiceSession *VoiceSession)

// Handler handles WebSocket proxy requests
type Handler struct {
	config  *config.WebSocketConfig
	manager *session.Manager
	router  *router.Router

	// Session header name from main config
	sessionHeader string

	// Session control parser for detecting INVITE/BYE
	controlParser *SessionControlParser

	// Policy engine for scanning text frames
	policyEngine *policy.Engine

	// Callbacks for voice session lifecycle
	onVoiceSessionStart VoiceSessionCallback
	onVoiceSessionEnd   VoiceSessionCallback

	// Track voice session managers per WebSocket session
	voiceManagers   map[string]*VoiceSessionManager
	voiceManagersMu sync.RWMutex
}

// NewHandler creates a new WebSocket proxy handler
func NewHandler(cfg *config.WebSocketConfig, sessionHeader string, manager *session.Manager, router *router.Router) *Handler {
	// Build custom patterns config if any
	var customPatterns *CustomPatternConfig
	if len(cfg.VoiceSessions.CustomPatterns) > 0 {
		customPatterns = &CustomPatternConfig{
			Patterns: make([]CustomPattern, len(cfg.VoiceSessions.CustomPatterns)),
		}
		for i, p := range cfg.VoiceSessions.CustomPatterns {
			customPatterns.Patterns[i] = CustomPattern{
				Name:    p.Name,
				TypeStr: p.Type,
				Pattern: p.Pattern,
			}
		}
	}

	return &Handler{
		config:        cfg,
		sessionHeader: sessionHeader,
		manager:       manager,
		router:        router,
		controlParser: NewSessionControlParser(customPatterns),
		voiceManagers: make(map[string]*VoiceSessionManager),
	}
}

// SetVoiceSessionCallbacks sets callbacks for voice session lifecycle events
func (h *Handler) SetVoiceSessionCallbacks(onStart, onEnd VoiceSessionCallback) {
	h.onVoiceSessionStart = onStart
	h.onVoiceSessionEnd = onEnd
}

// SetPolicyEngine sets the policy engine for scanning text frames
func (h *Handler) SetPolicyEngine(engine *policy.Engine) {
	h.policyEngine = engine
}

// IsWebSocketRequest checks if the request is a WebSocket upgrade request
// This should be called BEFORE reading the request body
func IsWebSocketRequest(r *http.Request) bool {
	// Check for WebSocket upgrade headers (case-insensitive)
	connection := r.Header.Get("Connection")
	upgrade := r.Header.Get("Upgrade")

	hasUpgrade := strings.Contains(strings.ToLower(connection), "upgrade")
	isWebSocket := strings.EqualFold(upgrade, "websocket")

	return hasUpgrade && isWebSocket
}

// GetVoiceManager returns the voice session manager for a WebSocket session
func (h *Handler) GetVoiceManager(sessionID string) *VoiceSessionManager {
	h.voiceManagersMu.RLock()
	defer h.voiceManagersMu.RUnlock()
	return h.voiceManagers[sessionID]
}

// ListVoiceManagers returns all active voice session managers
func (h *Handler) ListVoiceManagers() map[string]*VoiceSessionManager {
	h.voiceManagersMu.RLock()
	defer h.voiceManagersMu.RUnlock()
	result := make(map[string]*VoiceSessionManager, len(h.voiceManagers))
	for k, v := range h.voiceManagers {
		result[k] = v
	}
	return result
}

// VoiceSessionInfo contains information about a voice session for API responses
type VoiceSessionInfo struct {
	ID              string            `json:"id"`
	ParentSessionID string            `json:"parent_session_id"`
	State           string            `json:"state"`
	StartTime       string            `json:"start_time"`
	AnswerTime      *string           `json:"answer_time,omitempty"`
	EndTime         *string           `json:"end_time,omitempty"`
	Duration        string            `json:"duration"`
	TalkTime        string            `json:"talk_time"`
	AudioFramesIn   int64             `json:"audio_frames_in"`
	AudioFramesOut  int64             `json:"audio_frames_out"`
	AudioBytesIn    int64             `json:"audio_bytes_in"`
	AudioBytesOut   int64             `json:"audio_bytes_out"`
	AudioDurationMs int64             `json:"audio_duration_ms"`
	TurnCount       int               `json:"turn_count"`
	Model           string            `json:"model,omitempty"`
	Voice           string            `json:"voice,omitempty"`
	Language        string            `json:"language,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	Transcript      []TranscriptEntry `json:"transcript,omitempty"`
}

// ListVoiceSessions returns all voice sessions for a WebSocket session
func (h *Handler) ListVoiceSessions(sessionID string) []VoiceSessionInfo {
	h.voiceManagersMu.RLock()
	mgr := h.voiceManagers[sessionID]
	h.voiceManagersMu.RUnlock()

	if mgr == nil {
		return nil
	}

	sessions := mgr.ListSessions()
	history := mgr.ListHistory()

	result := make([]VoiceSessionInfo, 0, len(sessions)+len(history))

	// Add active sessions
	for _, vs := range sessions {
		result = append(result, voiceSessionToInfo(vs))
	}

	// Add historical sessions
	for _, vs := range history {
		result = append(result, voiceSessionToInfo(vs))
	}

	return result
}

// voiceSessionToInfo converts a VoiceSession to VoiceSessionInfo
func voiceSessionToInfo(vs *VoiceSession) VoiceSessionInfo {
	snap := vs.Snapshot()
	info := VoiceSessionInfo{
		ID:              snap.ID,
		ParentSessionID: snap.ParentSessionID,
		State:           snap.State.String(),
		StartTime:       snap.StartTime.Format(time.RFC3339),
		Duration:        vs.Duration().String(),
		TalkTime:        vs.TalkTime().String(),
		AudioFramesIn:   snap.AudioFramesIn,
		AudioFramesOut:  snap.AudioFramesOut,
		AudioBytesIn:    snap.AudioBytesIn,
		AudioBytesOut:   snap.AudioBytesOut,
		AudioDurationMs: snap.AudioDurationMs,
		TurnCount:       snap.TurnCount,
		Model:           snap.Model,
		Voice:           snap.Voice,
		Language:        snap.Language,
		Metadata:        snap.Metadata,
		Transcript:      snap.Transcript,
	}
	if snap.AnswerTime != nil {
		t := snap.AnswerTime.Format(time.RFC3339)
		info.AnswerTime = &t
	}
	if snap.EndTime != nil {
		t := snap.EndTime.Format(time.RFC3339)
		info.EndTime = &t
	}
	return info
}

// GetVoiceSession returns a specific voice session
func (h *Handler) GetVoiceSession(sessionID, voiceSessionID string) *VoiceSession {
	h.voiceManagersMu.RLock()
	mgr := h.voiceManagers[sessionID]
	h.voiceManagersMu.RUnlock()

	if mgr == nil {
		return nil
	}

	if vs, ok := mgr.GetSession(voiceSessionID); ok {
		return vs
	}

	// Check history
	for _, vs := range mgr.ListHistory() {
		if vs.ID == voiceSessionID {
			return vs
		}
	}

	return nil
}

// EndVoiceSession ends a voice session (BYE)
func (h *Handler) EndVoiceSession(sessionID, voiceSessionID, reason string) bool {
	h.voiceManagersMu.RLock()
	mgr := h.voiceManagers[sessionID]
	h.voiceManagersMu.RUnlock()

	if mgr == nil {
		return false
	}

	return mgr.EndSession(voiceSessionID, reason)
}

// HoldVoiceSession puts a voice session on hold
func (h *Handler) HoldVoiceSession(sessionID, voiceSessionID string) bool {
	h.voiceManagersMu.RLock()
	mgr := h.voiceManagers[sessionID]
	h.voiceManagersMu.RUnlock()

	if mgr == nil {
		return false
	}

	return mgr.HoldSession(voiceSessionID)
}

// ResumeVoiceSession resumes a voice session from hold
func (h *Handler) ResumeVoiceSession(sessionID, voiceSessionID string) bool {
	h.voiceManagersMu.RLock()
	mgr := h.voiceManagers[sessionID]
	h.voiceManagersMu.RUnlock()

	if mgr == nil {
		return false
	}

	return mgr.ResumeSession(voiceSessionID)
}

// ServeHTTP handles the WebSocket upgrade and proxying
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Select backend using router (no body for WebSocket routing)
	// WebSocket requests can only use header, path, or default routing
	backend, err := h.router.Select(r, nil)
	if err != nil {
		slog.Error("failed to select backend for websocket", "error", err)
		http.Error(w, "Failed to select backend", http.StatusInternalServerError)
		return
	}

	// Get or create session
	sessionID := r.Header.Get(h.sessionHeader)
	var sess *session.Session

	if sessionID != "" {
		sess = h.manager.GetOrCreate(sessionID, backend.URL.String(), r.RemoteAddr)
	} else {
		sess = h.manager.GetOrCreateByClient(r.RemoteAddr, backend.Name, backend.URL.String())
	}

	if sess == nil {
		// Session was killed - reject request
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"session_terminated","message":"Session has been killed and cannot be reused"}`))
		return
	}

	// Mark session as WebSocket
	sess.SetWebSocket()
	sess.Touch()

	// Check if session was killed
	select {
	case <-sess.KillChan():
		slog.Warn("websocket upgrade rejected: session killed",
			"session_id", sess.ID,
			"path", r.URL.Path,
		)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"session_terminated","message":"Session has been killed"}`))
		return
	default:
	}

	// Create voice session manager if enabled
	var voiceMgr *VoiceSessionManager
	if h.config.VoiceSessions.Enabled {
		voiceMgr = NewVoiceSessionManager(sess.ID, h.config.VoiceSessions.MaxConcurrent)

		// Set policy engine for post-session transcript scanning
		if h.policyEngine != nil {
			voiceMgr.SetPolicyEngine(h.policyEngine)
		}

		// Set up voice session callbacks
		voiceMgr.SetCallbacks(
			func(vs *VoiceSession) {
				slog.Info("voice session started",
					"ws_session_id", sess.ID,
					"voice_session_id", vs.ID,
					"protocol", vs.Metadata["protocol"],
				)
				if h.onVoiceSessionStart != nil {
					h.onVoiceSessionStart(sess, vs)
				}
			},
			func(vs *VoiceSession) {
				slog.Info("voice session ended",
					"ws_session_id", sess.ID,
					"voice_session_id", vs.ID,
					"duration_ms", vs.Duration().Milliseconds(),
					"audio_duration_ms", vs.AudioDurationMs,
					"turns", vs.TurnCount,
				)
				if h.onVoiceSessionEnd != nil {
					h.onVoiceSessionEnd(sess, vs)
				}
			},
		)

		// Register voice manager
		h.voiceManagersMu.Lock()
		h.voiceManagers[sess.ID] = voiceMgr
		h.voiceManagersMu.Unlock()

		// Cleanup on exit
		defer func() {
			h.voiceManagersMu.Lock()
			delete(h.voiceManagers, sess.ID)
			h.voiceManagersMu.Unlock()
		}()
	}

	slog.Info("websocket upgrade request",
		"session_id", sess.ID,
		"path", r.URL.Path,
		"backend", backend.Name,
		"voice_sessions_enabled", h.config.VoiceSessions.Enabled,
	)

	// Accept the client WebSocket connection
	acceptOpts := &websocket.AcceptOptions{
		InsecureSkipVerify: true, // Allow any origin for proxy use
	}

	clientConn, err := websocket.Accept(w, r, acceptOpts)
	if err != nil {
		slog.Error("failed to accept websocket connection",
			"session_id", sess.ID,
			"error", err,
		)
		return
	}
	defer clientConn.CloseNow()

	// Connect to backend WebSocket
	backendConn, err := DialBackend(ctx, backend, r, h.config)
	if err != nil {
		slog.Error("failed to connect to backend websocket",
			"session_id", sess.ID,
			"backend", backend.Name,
			"error", err,
		)
		clientConn.Close(websocket.StatusInternalError, "Backend connection failed")
		return
	}
	defer backendConn.CloseNow()

	// Set message size limits
	if h.config.MaxMessageSize > 0 {
		clientConn.SetReadLimit(h.config.MaxMessageSize)
		backendConn.SetReadLimit(h.config.MaxMessageSize)
	}

	slog.Info("websocket connection established",
		"session_id", sess.ID,
		"backend", backend.Name,
		"ws_url", backend.WSURL.String(),
	)

	// Create cancellable context for the proxy
	proxyCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start bidirectional proxy
	var wg sync.WaitGroup
	wg.Add(2)

	// Forward client -> backend
	go func() {
		defer wg.Done()
		h.forwardFrames(proxyCtx, clientConn, backendConn, sess, voiceMgr, session.FrameInbound, cancel)
	}()

	// Forward backend -> client
	go func() {
		defer wg.Done()
		h.forwardFrames(proxyCtx, backendConn, clientConn, sess, voiceMgr, session.FrameOutbound, cancel)
	}()

	// Monitor for kill signal
	go func() {
		select {
		case <-proxyCtx.Done():
			return
		case <-sess.KillChan():
			slog.Warn("websocket connection killed",
				"session_id", sess.ID,
			)
			// End all voice sessions
			if voiceMgr != nil {
				voiceMgr.EndAll("session_killed")
			}
			// Send close frame to both connections
			clientConn.Close(websocket.StatusNormalClosure, "Session terminated")
			backendConn.Close(websocket.StatusNormalClosure, "Session terminated")
			cancel()
		}
	}()

	// Start ping/pong keep-alive
	if h.config.PingInterval > 0 {
		go h.keepAlive(proxyCtx, clientConn, sess)
	}

	// Wait for both directions to complete
	wg.Wait()

	// End any remaining voice sessions
	if voiceMgr != nil {
		voiceMgr.EndAll("websocket_closed")
	}

	// Get voice session stats
	var voiceStats VoiceSessionStats
	if voiceMgr != nil {
		voiceStats = voiceMgr.Stats()
	}

	slog.Info("websocket connection closed",
		"session_id", sess.ID,
		"frame_count", sess.FrameCount,
		"text_frames", sess.TextFrames,
		"binary_frames", sess.BinaryFrames,
		"bytes_in", sess.BytesIn,
		"bytes_out", sess.BytesOut,
		"voice_sessions_completed", voiceStats.CompletedSessions,
		"total_audio_duration_ms", voiceStats.TotalAudioDurationMs,
	)
}

// forwardFrames forwards WebSocket frames from src to dst
func (h *Handler) forwardFrames(ctx context.Context, src, dst *websocket.Conn, sess *session.Session, voiceMgr *VoiceSessionManager, direction session.FrameDirection, cancel context.CancelFunc) {
	dirStr := "client->backend"
	inbound := true
	if direction == session.FrameOutbound {
		dirStr = "backend->client"
		inbound = false
	}

	// Track if we've auto-started a session
	autoStarted := false

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Read frame from source
		msgType, data, err := src.Read(ctx)
		if err != nil {
			if err == io.EOF || websocket.CloseStatus(err) != -1 {
				// Normal close
				slog.Debug("websocket closed normally",
					"session_id", sess.ID,
					"direction", dirStr,
				)
			} else if ctx.Err() == nil {
				slog.Error("websocket read error",
					"session_id", sess.ID,
					"direction", dirStr,
					"error", err,
				)
			}
			cancel()
			return
		}

		// Track frame statistics
		frameType := session.FrameBinary
		if msgType == websocket.MessageText {
			frameType = session.FrameText
		}
		sess.AddFrame(frameType, int64(len(data)), direction)

		// Process voice session control for text frames
		if voiceMgr != nil && msgType == websocket.MessageText {
			h.processSessionControl(voiceMgr, data, inbound, sess)
		}

		// Policy scanning for text frames
		if h.config.ScanTextFrames && h.policyEngine != nil && msgType == websocket.MessageText {
			var result *policy.ContentCheckResult
			if inbound {
				result = h.policyEngine.EvaluateRequestContent(sess.ID, string(data))
			} else {
				result = h.policyEngine.EvaluateResponseContent(sess.ID, string(data))
			}

			if result != nil && len(result.Violations) > 0 {
				// Log violations
				for _, v := range result.Violations {
					slog.Warn("websocket policy violation",
						"session_id", sess.ID,
						"direction", dirStr,
						"rule", v.RuleName,
						"severity", v.Severity,
						"action", v.Action,
						"matched", v.MatchedText,
					)
				}

				// Handle block/terminate actions
				if result.ShouldTerminate {
					slog.Warn("websocket connection terminated by policy",
						"session_id", sess.ID,
						"direction", dirStr,
					)
					// Close both connections
					src.Close(websocket.StatusPolicyViolation, "Policy violation: session terminated")
					dst.Close(websocket.StatusPolicyViolation, "Policy violation: session terminated")
					cancel()
					return
				}

				if result.ShouldBlock {
					slog.Warn("websocket frame blocked by policy",
						"session_id", sess.ID,
						"direction", dirStr,
						"size", len(data),
					)
					// Don't forward this frame, but keep connection open
					continue
				}
			}
		}

		// Track voice session metrics
		if voiceMgr != nil {
			activeVS := voiceMgr.GetActiveSession()

			// Auto-start session on first binary frame if enabled
			if activeVS == nil && msgType == websocket.MessageBinary &&
				h.config.VoiceSessions.AutoStartSession && !autoStarted && inbound {
				var err error
				activeVS, err = voiceMgr.StartSession()
				if err == nil {
					activeVS.SetMetadata("protocol", "auto_detected")
					activeVS.SetMetadata("trigger", "first_audio_frame")
					activeVS.Activate()
					autoStarted = true
					slog.Debug("auto-started voice session on first audio frame",
						"ws_session_id", sess.ID,
						"voice_session_id", activeVS.ID,
					)
				}
			}

			// Update active voice session metrics
			if activeVS != nil && activeVS.IsActive() {
				if msgType == websocket.MessageBinary {
					// Estimate audio duration: assume 16kHz 16-bit mono = 32KB/s = 31.25ms per KB
					estimatedMs := int64(len(data)) / 32
					activeVS.AddAudioFrame(int64(len(data)), inbound, estimatedMs)
				} else {
					activeVS.AddTextFrame(inbound)
				}
			}
		}

		// Write frame to destination
		err = dst.Write(ctx, msgType, data)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("websocket write error",
					"session_id", sess.ID,
					"direction", dirStr,
					"error", err,
				)
			}
			cancel()
			return
		}

		slog.Debug("websocket frame forwarded",
			"session_id", sess.ID,
			"direction", dirStr,
			"type", msgType.String(),
			"size", len(data),
		)
	}
}

// processSessionControl handles INVITE/BYE/etc. messages
func (h *Handler) processSessionControl(voiceMgr *VoiceSessionManager, data []byte, inbound bool, sess *session.Session) {
	msg := h.controlParser.Parse(data)
	if msg == nil {
		return
	}

	slog.Debug("session control message detected",
		"ws_session_id", sess.ID,
		"type", msg.Type.String(),
		"protocol", msg.Protocol,
		"event", msg.RawEvent,
		"inbound", inbound,
	)

	switch msg.Type {
	case ControlInvite:
		// Start new voice session (INVITE from client)
		if inbound {
			vs, err := voiceMgr.StartSession()
			if err != nil {
				slog.Warn("failed to start voice session",
					"ws_session_id", sess.ID,
					"error", err,
				)
				return
			}

			// Copy metadata from control message
			for k, v := range msg.Metadata {
				vs.SetMetadata(k, v)
			}
			vs.SetMetadata("protocol", msg.Protocol)
			vs.SetMetadata("invite_event", msg.RawEvent)

			// Extract common fields
			if model, ok := msg.Metadata["model"]; ok {
				vs.Model = model
			}
			if voice, ok := msg.Metadata["voice"]; ok {
				vs.Voice = voice
			}
		}

	case ControlOK:
		// Session confirmed (OK from backend)
		if !inbound {
			activeVS := voiceMgr.GetActiveSession()
			if activeVS != nil {
				activeVS.Activate()
				for k, v := range msg.Metadata {
					activeVS.SetMetadata(k, v)
				}
			}
		}

	case ControlBye:
		// End voice session (BYE from either side)
		reason := "bye"
		if errMsg, ok := msg.Metadata["error"]; ok {
			reason = "error: " + errMsg
		}
		voiceMgr.EndActiveSession(reason)

	case ControlHold:
		activeVS := voiceMgr.GetActiveSession()
		if activeVS != nil {
			voiceMgr.HoldSession(activeVS.ID)
		}

	case ControlResume:
		// Find held session and resume
		for _, vs := range voiceMgr.ListSessions() {
			if vs.GetState() == VoiceSessionHeld {
				voiceMgr.ResumeSession(vs.ID)
				break
			}
		}

	case ControlTurnStart:
		activeVS := voiceMgr.GetActiveSession()
		if activeVS != nil {
			activeVS.IncrementTurnCount()
		}

	case ControlTurnEnd:
		// Could track turn timing here
	}

	// Capture transcript content if present
	if msg.Transcript != "" {
		activeVS := voiceMgr.GetActiveSession()
		if activeVS != nil {
			activeVS.AddTranscript(msg.TranscriptSpeaker, msg.Transcript, msg.TranscriptSource, msg.TranscriptFinal)
			slog.Debug("transcript captured",
				"ws_session_id", sess.ID,
				"voice_session_id", activeVS.ID,
				"speaker", msg.TranscriptSpeaker,
				"source", msg.TranscriptSource,
				"final", msg.TranscriptFinal,
				"length", len(msg.Transcript),
			)
		}
	}
}

// keepAlive sends periodic ping frames to keep the connection alive
func (h *Handler) keepAlive(ctx context.Context, conn *websocket.Conn, sess *session.Session) {
	ticker := time.NewTicker(h.config.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, h.config.PongTimeout)
			err := conn.Ping(pingCtx)
			cancel()

			if err != nil {
				if ctx.Err() == nil {
					slog.Debug("websocket ping failed",
						"session_id", sess.ID,
						"error", err,
					)
				}
				return
			}
		}
	}
}

