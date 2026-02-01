package control

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"elida/internal/dashboard"
	"elida/internal/policy"
	"elida/internal/session"
	"elida/internal/storage"
	"elida/internal/websocket"
)

// Handler handles control API requests
type Handler struct {
	store        session.Store
	manager      *session.Manager
	historyStore *storage.SQLiteStore
	policyEngine *policy.Engine
	wsHandler    *websocket.Handler
	dashboard    *dashboard.Handler
	mux          *http.ServeMux

	// Authentication
	authEnabled bool
	apiKey      string
}

// New creates a new control API handler
func New(store session.Store, manager *session.Manager) *Handler {
	return NewWithHistory(store, manager, nil)
}

// NewWithHistory creates a new control API handler with history support
func NewWithHistory(store session.Store, manager *session.Manager, historyStore *storage.SQLiteStore) *Handler {
	return NewWithPolicy(store, manager, historyStore, nil)
}

// NewWithPolicy creates a new control API handler with history and policy support
func NewWithPolicy(store session.Store, manager *session.Manager, historyStore *storage.SQLiteStore, policyEngine *policy.Engine) *Handler {
	return NewWithAuth(store, manager, historyStore, policyEngine, false, "")
}

// NewWithAuth creates a new control API handler with all options including authentication
func NewWithAuth(store session.Store, manager *session.Manager, historyStore *storage.SQLiteStore, policyEngine *policy.Engine, authEnabled bool, apiKey string) *Handler {
	h := &Handler{
		store:        store,
		manager:      manager,
		historyStore: historyStore,
		policyEngine: policyEngine,
		dashboard:    dashboard.New(),
		mux:          http.NewServeMux(),
		authEnabled:  authEnabled,
		apiKey:       apiKey,
	}

	// Dashboard UI (catch-all pattern for Go 1.22+)
	h.mux.Handle("/{path...}", h.dashboard)

	// Control API endpoints
	h.mux.HandleFunc("/control/health", h.handleHealth)
	h.mux.HandleFunc("/control/stats", h.handleStats)
	h.mux.HandleFunc("/control/sessions", h.handleSessions)
	h.mux.HandleFunc("/control/sessions/", h.handleSession)

	// History endpoints (only if history store is available)
	h.mux.HandleFunc("/control/history", h.handleHistory)
	h.mux.HandleFunc("/control/history/stats", h.handleHistoryStats)
	h.mux.HandleFunc("/control/history/timeseries", h.handleTimeSeries)
	h.mux.HandleFunc("/control/history/", h.handleHistorySession)

	// Policy/flagged sessions endpoints
	h.mux.HandleFunc("/control/flagged", h.handleFlagged)
	h.mux.HandleFunc("/control/flagged/stats", h.handleFlaggedStats)
	h.mux.HandleFunc("/control/flagged/", h.handleFlaggedSession)

	// Voice sessions endpoints (live)
	h.mux.HandleFunc("/control/voice", h.handleVoiceSessions)
	h.mux.HandleFunc("/control/voice/", h.handleVoiceSession)

	// Voice session history endpoints (persisted CDRs)
	h.mux.HandleFunc("/control/voice-history", h.handleVoiceHistory)
	h.mux.HandleFunc("/control/voice-history/stats", h.handleVoiceHistoryStats)
	h.mux.HandleFunc("/control/voice-history/", h.handleVoiceHistorySession)

	// TTS (Text-to-Speech) tracking endpoints
	h.mux.HandleFunc("/control/tts", h.handleTTSRequests)
	h.mux.HandleFunc("/control/tts/stats", h.handleTTSStats)

	return h
}

// SetWebSocketHandler sets the WebSocket handler for voice session access
func (h *Handler) SetWebSocketHandler(wsHandler *websocket.Handler) {
	h.wsHandler = wsHandler
}

// ServeHTTP implements http.Handler
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Add CORS headers for dashboard access
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Check authentication for /control/* endpoints
	if h.authEnabled && strings.HasPrefix(r.URL.Path, "/control/") {
		if !h.checkAuth(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="ELIDA Control API"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error":   "unauthorized",
				"message": "Valid API key required. Use 'Authorization: Bearer <api_key>' header.",
			})
			return
		}
	}

	h.mux.ServeHTTP(w, r)
}

// checkAuth verifies the request has a valid API key
func (h *Handler) checkAuth(r *http.Request) bool {
	// Check Authorization header (Bearer token)
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		// Support "Bearer <key>" format
		if strings.HasPrefix(authHeader, "Bearer ") {
			token := strings.TrimPrefix(authHeader, "Bearer ")
			if token == h.apiKey {
				return true
			}
		}
		// Also support just the key directly
		if authHeader == h.apiKey {
			return true
		}
	}

	// Check X-API-Key header as alternative
	if apiKey := r.Header.Get("X-API-Key"); apiKey == h.apiKey {
		return true
	}

	return false
}

// handleHealth handles GET /control/health
func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	response := HealthResponse{
		Status:    "ok",
		Timestamp: time.Now(),
		Version:   "0.1.0",
	}

	writeJSON(w, http.StatusOK, response)
}

// handleStats handles GET /control/stats
func (h *Handler) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	stats := h.manager.Stats()
	writeJSON(w, http.StatusOK, stats)
}

// handleSessions handles GET /control/sessions
func (h *Handler) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Query params for filtering
	query := r.URL.Query()
	stateFilter := query.Get("state")
	activeOnly := query.Get("active") == "true"

	var sessions []*session.Session
	if activeOnly || stateFilter == "active" {
		sessions = h.manager.ListActive()
	} else {
		sessions = h.manager.ListAll()
	}

	// Convert to response format
	response := SessionsResponse{
		Sessions: make([]SessionInfo, 0, len(sessions)),
	}

	for _, s := range sessions {
		snap := s.Snapshot()
		info := SessionInfo{
			ID:           snap.ID,
			State:        snap.State.String(),
			StartTime:    snap.StartTime,
			LastActivity: snap.LastActivity,
			Duration:     s.Duration().String(),
			IdleTime:     s.IdleTime().String(),
			RequestCount: snap.RequestCount,
			BytesIn:      snap.BytesIn,
			BytesOut:     snap.BytesOut,
			Backend:      snap.Backend,
			BackendsUsed: snap.BackendsUsed,
			ClientAddr:   snap.ClientAddr,
			Metadata:     snap.Metadata,
		}
		if snap.EndTime != nil {
			info.EndTime = snap.EndTime
		}
		response.Sessions = append(response.Sessions, info)
	}

	response.Total = len(response.Sessions)

	writeJSON(w, http.StatusOK, response)
}

// handleSession handles requests to /control/sessions/{id}
func (h *Handler) handleSession(w http.ResponseWriter, r *http.Request) {
	// Extract session ID from path
	path := strings.TrimPrefix(r.URL.Path, "/control/sessions/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "Session ID required", http.StatusBadRequest)
		return
	}

	sessionID := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch r.Method {
	case http.MethodGet:
		h.getSession(w, sessionID)
	case http.MethodPost:
		switch action {
		case "kill":
			h.killSession(w, sessionID)
		case "terminate":
			h.terminateSession(w, sessionID)
		case "resume":
			h.resumeSession(w, sessionID)
		default:
			http.Error(w, "Unknown action", http.StatusBadRequest)
		}
	case http.MethodDelete:
		h.killSession(w, sessionID)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// getSession handles GET /control/sessions/{id}
func (h *Handler) getSession(w http.ResponseWriter, id string) {
	sess, ok := h.manager.Get(id)
	if !ok {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	snap := sess.Snapshot()
	info := SessionInfo{
		ID:           snap.ID,
		State:        snap.State.String(),
		StartTime:    snap.StartTime,
		LastActivity: snap.LastActivity,
		Duration:     sess.Duration().String(),
		IdleTime:     sess.IdleTime().String(),
		RequestCount: snap.RequestCount,
		BytesIn:      snap.BytesIn,
		BytesOut:     snap.BytesOut,
		Backend:      snap.Backend,
		BackendsUsed: snap.BackendsUsed,
		ClientAddr:   snap.ClientAddr,
		Metadata:     snap.Metadata,
	}
	if snap.EndTime != nil {
		info.EndTime = snap.EndTime
	}

	writeJSON(w, http.StatusOK, info)
}

// killSession handles POST /control/sessions/{id}/kill
func (h *Handler) killSession(w http.ResponseWriter, id string) {
	slog.Info("kill request received", "session_id", id)

	if h.manager.Kill(id) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":     "killed",
			"session_id": id,
		})
	} else {
		http.Error(w, "Session not found or already terminated", http.StatusNotFound)
	}
}

// resumeSession handles POST /control/sessions/{id}/resume
func (h *Handler) resumeSession(w http.ResponseWriter, id string) {
	slog.Info("resume request received", "session_id", id)

	if h.manager.Resume(id) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":     "resumed",
			"session_id": id,
		})
	} else {
		// Check if it's terminated
		if sess, ok := h.manager.Get(id); ok && sess.IsTerminated() {
			http.Error(w, "Session is terminated and cannot be resumed", http.StatusForbidden)
			return
		}
		http.Error(w, "Session not found or not in killed state", http.StatusNotFound)
	}
}

// terminateSession handles POST /control/sessions/{id}/terminate
func (h *Handler) terminateSession(w http.ResponseWriter, id string) {
	slog.Warn("terminate request received", "session_id", id)

	if h.manager.Terminate(id) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":     "terminated",
			"session_id": id,
			"message":    "Session permanently terminated, cannot be resumed",
		})
	} else {
		http.Error(w, "Session not found or already terminated", http.StatusNotFound)
	}
}

// writeJSON writes a JSON response
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("failed to encode response", "error", err)
	}
}

// HealthResponse represents a health check response
type HealthResponse struct {
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
	Version   string    `json:"version"`
}

// SessionsResponse represents a list of sessions
type SessionsResponse struct {
	Total    int           `json:"total"`
	Sessions []SessionInfo `json:"sessions"`
}

// SessionInfo represents session information for API responses
type SessionInfo struct {
	ID           string            `json:"id"`
	State        string            `json:"state"`
	StartTime    time.Time         `json:"start_time"`
	LastActivity time.Time         `json:"last_activity"`
	EndTime      *time.Time        `json:"end_time,omitempty"`
	Duration     string            `json:"duration"`
	IdleTime     string            `json:"idle_time"`
	RequestCount int               `json:"request_count"`
	BytesIn      int64             `json:"bytes_in"`
	BytesOut     int64             `json:"bytes_out"`
	Backend      string            `json:"backend"`
	BackendsUsed map[string]int    `json:"backends_used,omitempty"`
	ClientAddr   string            `json:"client_addr"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// handleHistory handles GET /control/history
func (h *Handler) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.historyStore == nil {
		http.Error(w, "History storage not enabled", http.StatusServiceUnavailable)
		return
	}

	query := r.URL.Query()

	opts := storage.ListSessionsOptions{
		Limit:   50,
		State:   query.Get("state"),
		Backend: query.Get("backend"),
	}

	if limitStr := query.Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 {
			opts.Limit = limit
		}
	}

	if offsetStr := query.Get("offset"); offsetStr != "" {
		if offset, err := strconv.Atoi(offsetStr); err == nil && offset >= 0 {
			opts.Offset = offset
		}
	}

	if sinceStr := query.Get("since"); sinceStr != "" {
		if since, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			opts.Since = &since
		}
	}

	if untilStr := query.Get("until"); untilStr != "" {
		if until, err := time.Parse(time.RFC3339, untilStr); err == nil {
			opts.Until = &until
		}
	}

	sessions, err := h.historyStore.ListSessions(opts)
	if err != nil {
		slog.Error("failed to list history", "error", err)
		http.Error(w, "Failed to retrieve history", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"sessions": sessions,
		"count":    len(sessions),
	})
}

// handleHistoryStats handles GET /control/history/stats
func (h *Handler) handleHistoryStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.historyStore == nil {
		http.Error(w, "History storage not enabled", http.StatusServiceUnavailable)
		return
	}

	query := r.URL.Query()
	var since *time.Time

	if sinceStr := query.Get("since"); sinceStr != "" {
		if s, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			since = &s
		}
	}

	stats, err := h.historyStore.GetStats(since)
	if err != nil {
		slog.Error("failed to get history stats", "error", err)
		http.Error(w, "Failed to retrieve stats", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, stats)
}

// handleTimeSeries handles GET /control/history/timeseries
func (h *Handler) handleTimeSeries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.historyStore == nil {
		http.Error(w, "History storage not enabled", http.StatusServiceUnavailable)
		return
	}

	query := r.URL.Query()

	// Default to last 24 hours
	since := time.Now().Add(-24 * time.Hour)
	if sinceStr := query.Get("since"); sinceStr != "" {
		if s, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			since = s
		}
	}

	interval := query.Get("interval")
	if interval == "" {
		interval = "hour"
	}

	points, err := h.historyStore.GetTimeSeries(since, interval)
	if err != nil {
		slog.Error("failed to get time series", "error", err)
		http.Error(w, "Failed to retrieve time series", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"interval": interval,
		"since":    since,
		"points":   points,
	})
}

// handleHistorySession handles GET /control/history/{id}
func (h *Handler) handleHistorySession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.historyStore == nil {
		http.Error(w, "History storage not enabled", http.StatusServiceUnavailable)
		return
	}

	// Extract session ID from path
	path := strings.TrimPrefix(r.URL.Path, "/control/history/")
	if path == "" || path == "stats" || path == "timeseries" {
		http.Error(w, "Session ID required", http.StatusBadRequest)
		return
	}

	sessionID := strings.Split(path, "/")[0]

	record, err := h.historyStore.GetSession(sessionID)
	if err != nil {
		slog.Error("failed to get session from history", "session_id", sessionID, "error", err)
		http.Error(w, "Failed to retrieve session", http.StatusInternalServerError)
		return
	}

	if record == nil {
		http.Error(w, "Session not found in history", http.StatusNotFound)
		return
	}

	writeJSON(w, http.StatusOK, record)
}

// handleFlagged handles GET /control/flagged
func (h *Handler) handleFlagged(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.policyEngine == nil {
		http.Error(w, "Policy engine not enabled", http.StatusServiceUnavailable)
		return
	}

	query := r.URL.Query()
	minSeverity := query.Get("severity")

	var flagged []*policy.FlaggedSession
	if minSeverity != "" {
		flagged = h.policyEngine.GetFlaggedSessionsBySeverity(policy.Severity(minSeverity))
	} else {
		flagged = h.policyEngine.GetFlaggedSessions()
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"flagged": flagged,
		"count":   len(flagged),
	})
}

// handleFlaggedStats handles GET /control/flagged/stats
func (h *Handler) handleFlaggedStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.policyEngine == nil {
		http.Error(w, "Policy engine not enabled", http.StatusServiceUnavailable)
		return
	}

	stats := h.policyEngine.Stats()
	writeJSON(w, http.StatusOK, stats)
}

// handleFlaggedSession handles GET /control/flagged/{id}
func (h *Handler) handleFlaggedSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.policyEngine == nil {
		http.Error(w, "Policy engine not enabled", http.StatusServiceUnavailable)
		return
	}

	// Extract session ID from path
	path := strings.TrimPrefix(r.URL.Path, "/control/flagged/")
	if path == "" || path == "stats" {
		http.Error(w, "Session ID required", http.StatusBadRequest)
		return
	}

	sessionID := strings.Split(path, "/")[0]

	flagged := h.policyEngine.GetFlaggedSession(sessionID)
	if flagged == nil {
		http.Error(w, "Session not flagged or not found", http.StatusNotFound)
		return
	}

	writeJSON(w, http.StatusOK, flagged)
}

// handleVoiceSessions handles GET /control/voice - list all voice sessions
func (h *Handler) handleVoiceSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.wsHandler == nil {
		http.Error(w, "WebSocket handler not enabled", http.StatusServiceUnavailable)
		return
	}

	// Collect all voice sessions across all WebSocket connections
	managers := h.wsHandler.ListVoiceManagers()
	allSessions := make([]websocket.VoiceSessionInfo, 0)
	stats := make(map[string]int)

	for sessionID, mgr := range managers {
		sessions := h.wsHandler.ListVoiceSessions(sessionID)
		allSessions = append(allSessions, sessions...)

		// Count by state
		for _, vs := range sessions {
			stats[vs.State]++
		}
		mgrStats := mgr.Stats()
		stats["active"] = mgrStats.ActiveSessions
		stats["completed"] = mgrStats.CompletedSessions
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"voice_sessions":    allSessions,
		"total":             len(allSessions),
		"websocket_sessions": len(managers),
		"stats":             stats,
	})
}

// handleVoiceSession handles requests to /control/voice/{sessionID}
// Path patterns:
//   GET /control/voice/{sessionID} - List voice sessions for a WebSocket session
//   GET /control/voice/{sessionID}/{voiceID} - Get a specific voice session
//   POST /control/voice/{sessionID}/{voiceID}/bye - End a voice session
//   POST /control/voice/{sessionID}/{voiceID}/hold - Put on hold
//   POST /control/voice/{sessionID}/{voiceID}/resume - Resume from hold
func (h *Handler) handleVoiceSession(w http.ResponseWriter, r *http.Request) {
	if h.wsHandler == nil {
		http.Error(w, "WebSocket handler not enabled", http.StatusServiceUnavailable)
		return
	}

	// Parse path: /control/voice/{sessionID}/{voiceID?}/{action?}
	path := strings.TrimPrefix(r.URL.Path, "/control/voice/")
	parts := strings.Split(path, "/")

	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "Session ID required", http.StatusBadRequest)
		return
	}

	sessionID := parts[0]
	voiceID := ""
	action := ""

	if len(parts) > 1 {
		voiceID = parts[1]
	}
	if len(parts) > 2 {
		action = parts[2]
	}

	switch r.Method {
	case http.MethodGet:
		if voiceID == "" {
			// List voice sessions for this WebSocket session
			h.listVoiceSessions(w, sessionID)
		} else {
			// Get specific voice session
			h.getVoiceSession(w, sessionID, voiceID)
		}
	case http.MethodPost:
		if voiceID == "" {
			http.Error(w, "Voice session ID required", http.StatusBadRequest)
			return
		}
		switch action {
		case "bye":
			h.endVoiceSession(w, r, sessionID, voiceID)
		case "hold":
			h.holdVoiceSession(w, sessionID, voiceID)
		case "resume":
			h.resumeVoiceSession(w, sessionID, voiceID)
		default:
			http.Error(w, "Unknown action. Use: bye, hold, or resume", http.StatusBadRequest)
		}
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// listVoiceSessions handles GET /control/voice/{sessionID}
func (h *Handler) listVoiceSessions(w http.ResponseWriter, sessionID string) {
	sessions := h.wsHandler.ListVoiceSessions(sessionID)
	if sessions == nil {
		http.Error(w, "WebSocket session not found or has no voice sessions", http.StatusNotFound)
		return
	}

	mgr := h.wsHandler.GetVoiceManager(sessionID)
	var stats interface{}
	if mgr != nil {
		stats = mgr.Stats()
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id":     sessionID,
		"voice_sessions": sessions,
		"total":          len(sessions),
		"stats":          stats,
	})
}

// getVoiceSession handles GET /control/voice/{sessionID}/{voiceID}
func (h *Handler) getVoiceSession(w http.ResponseWriter, sessionID, voiceID string) {
	vs := h.wsHandler.GetVoiceSession(sessionID, voiceID)
	if vs == nil {
		http.Error(w, "Voice session not found", http.StatusNotFound)
		return
	}

	info := websocket.VoiceSessionInfo{
		ID:              vs.ID,
		ParentSessionID: vs.ParentSessionID,
		State:           vs.GetState().String(),
		AudioFramesIn:   vs.AudioFramesIn,
		AudioFramesOut:  vs.AudioFramesOut,
		AudioBytesIn:    vs.AudioBytesIn,
		AudioBytesOut:   vs.AudioBytesOut,
		AudioDurationMs: vs.AudioDurationMs,
		TurnCount:       vs.TurnCount,
		Model:           vs.Model,
		Voice:           vs.Voice,
		Language:        vs.Language,
		Metadata:        vs.Metadata,
	}

	writeJSON(w, http.StatusOK, info)
}

// endVoiceSession handles POST /control/voice/{sessionID}/{voiceID}/bye
func (h *Handler) endVoiceSession(w http.ResponseWriter, r *http.Request, sessionID, voiceID string) {
	reason := r.URL.Query().Get("reason")
	if reason == "" {
		reason = "api_request"
	}

	slog.Info("voice session end request", "session_id", sessionID, "voice_id", voiceID, "reason", reason)

	if h.wsHandler.EndVoiceSession(sessionID, voiceID, reason) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":     "ended",
			"session_id": sessionID,
			"voice_id":   voiceID,
			"reason":     reason,
		})
	} else {
		http.Error(w, "Voice session not found or already ended", http.StatusNotFound)
	}
}

// holdVoiceSession handles POST /control/voice/{sessionID}/{voiceID}/hold
func (h *Handler) holdVoiceSession(w http.ResponseWriter, sessionID, voiceID string) {
	slog.Info("voice session hold request", "session_id", sessionID, "voice_id", voiceID)

	if h.wsHandler.HoldVoiceSession(sessionID, voiceID) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":     "held",
			"session_id": sessionID,
			"voice_id":   voiceID,
		})
	} else {
		http.Error(w, "Voice session not found or not in active state", http.StatusNotFound)
	}
}

// resumeVoiceSession handles POST /control/voice/{sessionID}/{voiceID}/resume
func (h *Handler) resumeVoiceSession(w http.ResponseWriter, sessionID, voiceID string) {
	slog.Info("voice session resume request", "session_id", sessionID, "voice_id", voiceID)

	if h.wsHandler.ResumeVoiceSession(sessionID, voiceID) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":     "resumed",
			"session_id": sessionID,
			"voice_id":   voiceID,
		})
	} else {
		http.Error(w, "Voice session not found or not in held state", http.StatusNotFound)
	}
}

// handleVoiceHistory handles GET /control/voice-history
func (h *Handler) handleVoiceHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.historyStore == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"count":          0,
			"voice_sessions": nil,
			"error":          "storage not enabled",
		})
		return
	}

	// Parse query parameters
	opts := storage.ListVoiceSessionsOptions{
		Limit:  100,
		Offset: 0,
	}

	if limit := r.URL.Query().Get("limit"); limit != "" {
		if l, err := strconv.Atoi(limit); err == nil && l > 0 {
			opts.Limit = l
		}
	}
	if offset := r.URL.Query().Get("offset"); offset != "" {
		if o, err := strconv.Atoi(offset); err == nil && o >= 0 {
			opts.Offset = o
		}
	}
	if state := r.URL.Query().Get("state"); state != "" {
		opts.State = state
	}
	if parentID := r.URL.Query().Get("parent_session_id"); parentID != "" {
		opts.ParentSessionID = parentID
	}
	if since := r.URL.Query().Get("since"); since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			opts.Since = &t
		}
	}

	sessions, err := h.historyStore.ListVoiceSessions(opts)
	if err != nil {
		slog.Error("failed to list voice session history", "error", err)
		http.Error(w, "Failed to retrieve voice session history", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"count":          len(sessions),
		"voice_sessions": sessions,
	})
}

// handleVoiceHistoryStats handles GET /control/voice-history/stats
func (h *Handler) handleVoiceHistoryStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.historyStore == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"error": "storage not enabled",
		})
		return
	}

	var since *time.Time
	if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
		if t, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			since = &t
		}
	}

	stats, err := h.historyStore.GetVoiceStats(since)
	if err != nil {
		slog.Error("failed to get voice stats", "error", err)
		http.Error(w, "Failed to retrieve voice stats", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, stats)
}

// handleVoiceHistorySession handles GET /control/voice-history/{voiceID}
func (h *Handler) handleVoiceHistorySession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.historyStore == nil {
		http.Error(w, "Storage not enabled", http.StatusServiceUnavailable)
		return
	}

	// Extract voice session ID from path
	path := strings.TrimPrefix(r.URL.Path, "/control/voice-history/")
	voiceID := strings.TrimSuffix(path, "/")

	if voiceID == "" {
		http.Error(w, "Voice session ID required", http.StatusBadRequest)
		return
	}

	session, err := h.historyStore.GetVoiceSession(voiceID)
	if err != nil {
		slog.Error("failed to get voice session", "voice_id", voiceID, "error", err)
		http.Error(w, "Failed to retrieve voice session", http.StatusInternalServerError)
		return
	}

	if session == nil {
		http.Error(w, "Voice session not found", http.StatusNotFound)
		return
	}

	writeJSON(w, http.StatusOK, session)
}

// handleTTSRequests handles GET /control/tts
func (h *Handler) handleTTSRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.historyStore == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"count":        0,
			"tts_requests": nil,
			"error":        "storage not enabled",
		})
		return
	}

	// Parse query parameters
	opts := storage.ListTTSRequestsOptions{
		Limit:  100,
		Offset: 0,
	}

	if limit := r.URL.Query().Get("limit"); limit != "" {
		if l, err := strconv.Atoi(limit); err == nil && l > 0 {
			opts.Limit = l
		}
	}
	if offset := r.URL.Query().Get("offset"); offset != "" {
		if o, err := strconv.Atoi(offset); err == nil && o >= 0 {
			opts.Offset = o
		}
	}
	if sessionID := r.URL.Query().Get("session_id"); sessionID != "" {
		opts.SessionID = sessionID
	}
	if provider := r.URL.Query().Get("provider"); provider != "" {
		opts.Provider = provider
	}
	if since := r.URL.Query().Get("since"); since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			opts.Since = &t
		}
	}

	requests, err := h.historyStore.ListTTSRequests(opts)
	if err != nil {
		slog.Error("failed to list TTS requests", "error", err)
		http.Error(w, "Failed to retrieve TTS requests", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"count":        len(requests),
		"tts_requests": requests,
	})
}

// handleTTSStats handles GET /control/tts/stats
func (h *Handler) handleTTSStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.historyStore == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"error": "storage not enabled",
		})
		return
	}

	var since *time.Time
	if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
		if t, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			since = &t
		}
	}

	stats, err := h.historyStore.GetTTSStats(since)
	if err != nil {
		slog.Error("failed to get TTS stats", "error", err)
		http.Error(w, "Failed to retrieve TTS stats", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, stats)
}
