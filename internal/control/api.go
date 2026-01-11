package control

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"elida/internal/session"
	"elida/internal/storage"
)

// Handler handles control API requests
type Handler struct {
	store        session.Store
	manager      *session.Manager
	historyStore *storage.SQLiteStore
	mux          *http.ServeMux
}

// New creates a new control API handler
func New(store session.Store, manager *session.Manager) *Handler {
	return NewWithHistory(store, manager, nil)
}

// NewWithHistory creates a new control API handler with history support
func NewWithHistory(store session.Store, manager *session.Manager, historyStore *storage.SQLiteStore) *Handler {
	h := &Handler{
		store:        store,
		manager:      manager,
		historyStore: historyStore,
		mux:          http.NewServeMux(),
	}

	h.mux.HandleFunc("/control/health", h.handleHealth)
	h.mux.HandleFunc("/control/stats", h.handleStats)
	h.mux.HandleFunc("/control/sessions", h.handleSessions)
	h.mux.HandleFunc("/control/sessions/", h.handleSession)

	// History endpoints (only if history store is available)
	h.mux.HandleFunc("/control/history", h.handleHistory)
	h.mux.HandleFunc("/control/history/stats", h.handleHistoryStats)
	h.mux.HandleFunc("/control/history/timeseries", h.handleTimeSeries)
	h.mux.HandleFunc("/control/history/", h.handleHistorySession)

	return h
}

// ServeHTTP implements http.Handler
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Add CORS headers for dashboard access
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	h.mux.ServeHTTP(w, r)
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
		if action == "kill" {
			h.killSession(w, sessionID)
		} else {
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
