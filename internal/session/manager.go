package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
)

// SessionEndCallback is called when a session ends (before cleanup)
type SessionEndCallback func(sess *Session)

// KillBlockMode defines how long killed sessions stay blocked
type KillBlockMode string

const (
	KillBlockDuration        KillBlockMode = "duration"
	KillBlockUntilHourChange KillBlockMode = "until_hour_change"
	KillBlockPermanent       KillBlockMode = "permanent"
)

// KillBlockConfig configures kill block behavior
type KillBlockConfig struct {
	Mode     KillBlockMode
	Duration time.Duration
}

// Manager handles session lifecycle, timeouts, and cleanup
type Manager struct {
	store   Store
	timeout time.Duration

	// Cleanup interval for expired sessions
	cleanupInterval time.Duration
	// How long to keep completed sessions before deletion
	retentionPeriod time.Duration

	// Kill block configuration
	killBlockConfig KillBlockConfig

	// How long a killed session can be resumed before auto-terminating
	// Default: 30 minutes. Set to 0 to disable auto-termination.
	killResumeTimeout time.Duration

	// Callback for when sessions end (for persistence)
	onSessionEnd SessionEndCallback

	// Map client IPs to session IDs for IP-based session tracking
	clientSessions   map[string]string
	clientSessionsMu sync.RWMutex
}

// NewManager creates a new session manager with default kill block settings
func NewManager(store Store, timeout time.Duration) *Manager {
	return NewManagerWithKillBlock(store, timeout, KillBlockConfig{
		Mode:     KillBlockUntilHourChange,
		Duration: 30 * time.Minute,
	})
}

// NewManagerWithKillBlock creates a new session manager with custom kill block settings
func NewManagerWithKillBlock(store Store, timeout time.Duration, killBlock KillBlockConfig) *Manager {
	return &Manager{
		store:             store,
		timeout:           timeout,
		cleanupInterval:   30 * time.Second,
		retentionPeriod:   5 * time.Minute,
		killBlockConfig:   killBlock,
		killResumeTimeout: 30 * time.Minute, // Default: 30 min to resume before auto-terminate
		clientSessions:    make(map[string]string),
	}
}

// SetKillResumeTimeout sets how long a killed session can be resumed
// before it auto-terminates. Set to 0 to disable auto-termination.
func (m *Manager) SetKillResumeTimeout(d time.Duration) {
	m.killResumeTimeout = d
}

// SetSessionEndCallback sets a callback to be called when sessions end
func (m *Manager) SetSessionEndCallback(cb SessionEndCallback) {
	m.onSessionEnd = cb
}

// Run starts the session manager's background tasks
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(m.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("session manager stopping")
			return
		case <-ticker.C:
			m.checkTimeouts()
			m.cleanup()
		}
	}
}

// GetOrCreate retrieves an existing session or creates a new one.
// Returns nil if the session was previously killed (caller should reject request).
func (m *Manager) GetOrCreate(id, backend, clientAddr string) *Session {
	if id == "" {
		id = m.generateID()
	}

	// Try to get existing session
	if sess, ok := m.store.Get(id); ok {
		if sess.IsActive() {
			return sess
		}
		// Session exists but is not active
		if sess.GetState() == Killed {
			// Killed sessions cannot be reused
			slog.Warn("rejected request for killed session",
				"session_id", id,
				"client", clientAddr,
			)
			return nil
		}
		// TimedOut or Completed sessions - allow new session with same ID
	}

	// Create new session
	sess := NewSession(id, backend, clientAddr)
	m.store.Put(sess)

	slog.Info("session created",
		"session_id", id,
		"backend", backend,
		"client", clientAddr,
	)

	return sess
}

// GetOrCreateByClient retrieves or creates a session based on client IP and backend.
// This is used when no X-Session-ID header is provided, to group requests
// from the same client to the same backend into a single session.
// Each (client, backend) pair gets its own session for granular control.
func (m *Manager) GetOrCreateByClient(clientAddr, backendName, backendURL string) *Session {
	// Extract IP from client address (remove port)
	clientIP := extractIP(clientAddr)

	// Generate the session ID (deterministic based on IP + backend + time window)
	sessionID := m.generateClientSessionID(clientIP, backendName)

	// Check if session already exists in store (regardless of client mapping)
	if sess, ok := m.store.Get(sessionID); ok {
		if sess.IsActive() {
			return sess
		}
		// Session exists but is not active - check state
		if sess.GetState() == Killed {
			// Check if kill block has expired based on mode
			if m.isKillBlockActive(sess) {
				slog.Warn("rejected request for killed client session",
					"session_id", sessionID,
					"client_ip", clientIP,
					"backend", backendName,
					"kill_block_mode", m.killBlockConfig.Mode,
				)
				return nil // BLOCK - don't create new session
			}
			// Kill block expired - allow new session
			slog.Info("kill block expired, allowing new session",
				"session_id", sessionID,
				"client_ip", clientIP,
				"backend", backendName,
			)
			m.store.Delete(sessionID)
		} else {
			// TimedOut or Completed - allow creating new session
			m.store.Delete(sessionID)
		}
	}

	// Create new session
	sess := NewSession(sessionID, backendURL, clientAddr)
	m.store.Put(sess)

	// Update client mapping (now includes backend)
	m.clientSessionsMu.Lock()
	m.clientSessions[clientIP+"-"+backendName] = sessionID
	m.clientSessionsMu.Unlock()

	slog.Info("client session created",
		"session_id", sessionID,
		"client_ip", clientIP,
		"backend", backendName,
	)

	return sess
}

// isKillBlockActive checks if a killed session should still be blocked
func (m *Manager) isKillBlockActive(sess *Session) bool {
	if sess.GetState() != Killed || sess.EndTime == nil {
		return false
	}

	switch m.killBlockConfig.Mode {
	case KillBlockPermanent:
		// Permanent block - always active until server restart
		return true

	case KillBlockDuration:
		// Block for a specific duration after kill
		elapsed := time.Since(*sess.EndTime)
		return elapsed < m.killBlockConfig.Duration

	case KillBlockUntilHourChange:
		// Block until the hour changes (session ID would regenerate)
		// Since session ID includes the hour, if we got here with the same ID,
		// we're still in the same hour - so block is active
		return true

	default:
		// Unknown mode - default to blocking
		return true
	}
}

// generateClientSessionID creates a session ID based on client IP, backend, and timestamp
func (m *Manager) generateClientSessionID(clientIP, backendName string) string {
	// Create a short hash of the IP + backend + current hour (sessions reset hourly)
	hourKey := time.Now().Format("2006-01-02-15")
	data := clientIP + "-" + backendName + "-" + hourKey
	hash := sha256.Sum256([]byte(data))
	shortHash := hex.EncodeToString(hash[:4]) // 8 char hex

	// Include backend name in session ID for clarity
	return "client-" + shortHash + "-" + backendName
}

// extractIP extracts the IP address from a client address (host:port)
func extractIP(clientAddr string) string {
	host, _, err := net.SplitHostPort(clientAddr)
	if err != nil {
		// Maybe it's just an IP without port
		return clientAddr
	}
	return host
}

// Get retrieves a session by ID
func (m *Manager) Get(id string) (*Session, bool) {
	return m.store.Get(id)
}

// Kill terminates a session
func (m *Manager) Kill(id string) bool {
	sess, ok := m.store.Get(id)
	if !ok {
		return false
	}

	if !sess.IsActive() {
		return false
	}

	sess.Kill()

	// Persist the killed state (important for Redis store)
	m.store.Put(sess)

	// Publish kill signal for distributed kill switch
	if rs, ok := m.store.(*RedisStore); ok {
		rs.PublishKill(id)
	}

	// Export CDR immediately when session is killed
	if m.onSessionEnd != nil {
		m.onSessionEnd(sess)
	}

	slog.Info("session killed",
		"session_id", id,
		"duration", sess.Duration(),
		"requests", sess.RequestCount,
	)

	return true
}

// Resume reactivates a killed session, allowing it to continue
// Returns false if session is terminated (cannot be resumed)
func (m *Manager) Resume(id string) bool {
	sess, ok := m.store.Get(id)
	if !ok {
		return false
	}

	if sess.GetState() != Killed {
		return false
	}

	if !sess.Resume() {
		slog.Warn("cannot resume terminated session",
			"session_id", id,
		)
		return false
	}

	// Persist the resumed state
	m.store.Put(sess)

	slog.Info("session resumed",
		"session_id", id,
		"duration", sess.Duration(),
		"requests", sess.RequestCount,
	)

	return true
}

// Terminate permanently kills a session (cannot be resumed)
// Use this for malicious or runaway agents
func (m *Manager) Terminate(id string) bool {
	sess, ok := m.store.Get(id)
	if !ok {
		return false
	}

	if !sess.IsActive() && !sess.IsTerminated() {
		// Already killed but not terminated - allow upgrade to terminated
		if sess.GetState() != Killed {
			return false
		}
	}

	sess.Terminate()

	// Persist the terminated state
	m.store.Put(sess)

	// Publish kill signal for distributed kill switch
	if rs, ok := m.store.(*RedisStore); ok {
		rs.PublishKill(id)
	}

	// Export session record immediately
	if m.onSessionEnd != nil {
		m.onSessionEnd(sess)
	}

	slog.Warn("session terminated (permanent)",
		"session_id", id,
		"duration", sess.Duration(),
		"requests", sess.RequestCount,
	)

	return true
}

// Complete marks a session as completed
func (m *Manager) Complete(id string) {
	sess, ok := m.store.Get(id)
	if !ok {
		return
	}

	sess.SetState(Completed)

	slog.Info("session completed",
		"session_id", id,
		"duration", sess.Duration(),
		"requests", sess.RequestCount,
		"bytes_in", sess.BytesIn,
		"bytes_out", sess.BytesOut,
	)
}

// ListActive returns all active sessions
func (m *Manager) ListActive() []*Session {
	return m.store.List(ActiveFilter)
}

// ListAll returns all sessions
func (m *Manager) ListAll() []*Session {
	return m.store.List(nil)
}

// Stats returns session statistics
func (m *Manager) Stats() Stats {
	sessions := m.store.List(nil)
	
	stats := Stats{}
	for _, s := range sessions {
		switch s.GetState() {
		case Active:
			stats.Active++
		case Completed:
			stats.Completed++
		case Killed:
			stats.Killed++
		case TimedOut:
			stats.TimedOut++
		}
		stats.TotalRequests += s.RequestCount
		stats.TotalBytesIn += s.BytesIn
		stats.TotalBytesOut += s.BytesOut
	}
	stats.Total = len(sessions)
	
	return stats
}

// checkTimeouts checks for and handles timed out sessions
func (m *Manager) checkTimeouts() {
	// Check active sessions for idle timeout
	sessions := m.store.List(ActiveFilter)
	for _, sess := range sessions {
		if sess.IdleTime() > m.timeout {
			sess.SetState(TimedOut)

			slog.Warn("session timed out",
				"session_id", sess.ID,
				"idle_time", sess.IdleTime(),
				"timeout", m.timeout,
			)
		}
	}

	// Check killed sessions for auto-termination
	if m.killResumeTimeout > 0 {
		m.checkKilledSessionsForTermination()
	}
}

// checkKilledSessionsForTermination auto-terminates killed sessions
// that have exceeded the resume timeout window
func (m *Manager) checkKilledSessionsForTermination() {
	sessions := m.store.List(func(s *Session) bool {
		return s.GetState() == Killed && !s.IsTerminated()
	})

	for _, sess := range sessions {
		if sess.EndTime != nil && time.Since(*sess.EndTime) > m.killResumeTimeout {
			sess.Terminate()
			m.store.Put(sess)

			slog.Warn("killed session auto-terminated after resume timeout",
				"session_id", sess.ID,
				"killed_duration", time.Since(*sess.EndTime),
				"timeout", m.killResumeTimeout,
			)
		}
	}
}

// cleanup removes old completed/terminated sessions
func (m *Manager) cleanup() {
	sessions := m.store.List(func(s *Session) bool {
		// Keep active sessions
		if s.IsActive() {
			return false
		}
		// Remove sessions that ended more than retentionPeriod ago
		if s.EndTime != nil {
			return time.Since(*s.EndTime) > m.retentionPeriod
		}
		return false
	})

	for _, sess := range sessions {
		// Call the callback before deleting (for persistence)
		if m.onSessionEnd != nil {
			m.onSessionEnd(sess)
		}
		m.store.Delete(sess.ID)
		slog.Debug("session cleaned up", "session_id", sess.ID)
	}
}

// generateID creates a new unique session ID
func (m *Manager) generateID() string {
	return uuid.New().String()
}

// Stats holds session statistics
type Stats struct {
	Total         int   `json:"total"`
	Active        int   `json:"active"`
	Completed     int   `json:"completed"`
	Killed        int   `json:"killed"`
	TimedOut      int   `json:"timed_out"`
	TotalRequests int   `json:"total_requests"`
	TotalBytesIn  int64 `json:"total_bytes_in"`
	TotalBytesOut int64 `json:"total_bytes_out"`
}
