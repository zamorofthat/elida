package session

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// Manager handles session lifecycle, timeouts, and cleanup
type Manager struct {
	store   Store
	timeout time.Duration

	// Cleanup interval for expired sessions
	cleanupInterval time.Duration
	// How long to keep completed sessions before deletion
	retentionPeriod time.Duration
}

// NewManager creates a new session manager
func NewManager(store Store, timeout time.Duration) *Manager {
	return &Manager{
		store:           store,
		timeout:         timeout,
		cleanupInterval: 30 * time.Second,
		retentionPeriod: 5 * time.Minute,
	}
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

	slog.Info("session killed",
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
