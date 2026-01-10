package control

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"elida/internal/session"
)

// Handler handles control API requests
type Handler struct {
	store   session.Store
	manager *session.Manager
	mux     *http.ServeMux
}

// New creates a new control API handler
func New(store session.Store, manager *session.Manager) *Handler {
	h := &Handler{
		store:   store,
		manager: manager,
		mux:     http.NewServeMux(),
	}

	h.mux.HandleFunc("/control/health", h.handleHealth)
	h.mux.HandleFunc("/control/stats", h.handleStats)
	h.mux.HandleFunc("/control/sessions", h.handleSessions)
	h.mux.HandleFunc("/control/sessions/", h.handleSession)

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
