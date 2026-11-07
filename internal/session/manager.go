package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"strings"
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

	// Mutex for session lifecycle operations (check-and-delete atomicity)
	sessionMu sync.Mutex

	// Map client IPs to session IDs for IP-based session tracking
	clientSessions   map[string]string
	clientSessionsMu sync.RWMutex

	// Periodic write-back of in-flight request sessions (Redis only). Zero
	// disables it. Set before Run.
	persistInterval time.Duration
	inflightMu      sync.Mutex
	inflight        map[*Session]struct{}
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

// GetSessionEndCallback returns the current session end callback
func (m *Manager) GetSessionEndCallback() SessionEndCallback {
	return m.onSessionEnd
}

// Run starts the session manager's background tasks
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(m.cleanupInterval)
	defer ticker.Stop()

	var persistC <-chan time.Time
	if m.persistInterval > 0 {
		pt := time.NewTicker(m.persistInterval)
		defer pt.Stop()
		persistC = pt.C
	}

	for {
		select {
		case <-ctx.Done():
			slog.Info("session manager stopping")
			return
		case <-ticker.C:
			m.checkTimeouts()
			m.cleanup()
		case <-persistC:
			m.persistInflight()
		}
	}
}

// DefaultRedisPersistInterval is how often in-flight request sessions are
// written back under the Redis store, bounding what a crash mid-request
// loses.
const DefaultRedisPersistInterval = 5 * time.Minute

// SetPersistInterval sets the in-flight write-back interval; 0 disables it.
// Call before Run.
func (m *Manager) SetPersistInterval(d time.Duration) {
	m.persistInterval = d
}

// TrackRequest registers a session a request is working on so the periodic
// persist can write its accumulated state while the request is still running
// (an in-flight request's state is otherwise only in this process). The
// returned func ends the request: it unregisters the session and writes it
// back with Persist. Defer it for the life of the request. Only the request
// goroutine's own copy is tracked: List returns fresh copies under Redis, so
// re-writing those would persist nothing.
func (m *Manager) TrackRequest(sess *Session) (end func()) {
	if sess == nil {
		return func() {}
	}
	if m.persistInterval > 0 {
		m.inflightMu.Lock()
		if m.inflight == nil {
			m.inflight = make(map[*Session]struct{})
		}
		m.inflight[sess] = struct{}{}
		m.inflightMu.Unlock()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			m.inflightMu.Lock()
			delete(m.inflight, sess)
			m.inflightMu.Unlock()
			m.Persist(sess)
		})
	}
}

// persistInflight writes back every tracked in-flight session. It persists
// outside inflightMu: Persist does store I/O. A tick can snapshot a session,
// then the request's end() runs, then the tick issues one more write for it:
// benign, but the request-end persist is not strictly guaranteed to be the
// last write.
func (m *Manager) persistInflight() {
	m.inflightMu.Lock()
	live := make([]*Session, 0, len(m.inflight))
	for s := range m.inflight {
		live = append(live, s)
	}
	m.inflightMu.Unlock()
	for _, s := range live {
		m.Persist(s)
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

	// Atomic check-and-delete to prevent race between isKillBlockActive and Delete
	m.sessionMu.Lock()
	if sess, ok := m.store.Get(sessionID); ok {
		if sess.IsActive() {
			m.sessionMu.Unlock()
			return sess
		}
		// Session exists but is not active - check state
		if sess.GetState() == Killed {
			// Check if kill block has expired based on mode
			if m.isKillBlockActive(sess) {
				m.sessionMu.Unlock()
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
	m.sessionMu.Unlock()

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

// RealClientAddr returns the best available client address from the request.
// Checks X-Forwarded-For and X-Real-IP headers (trusted proxy scenarios)
// before falling back to RemoteAddr. In NAT/shared-IP environments without
// these headers, multiple clients behind the same IP will share a session.
func RealClientAddr(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For can contain multiple IPs; use the first (original client)
		if idx := strings.Index(xff, ","); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	return r.RemoteAddr
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

// dataPutter is the package-internal write seam for stores that hand out
// copies (RedisStore, CopyStore): it stores already-converted wire data, so
// Persist can adjust the lifecycle on the data without touching the live
// session. MemoryStore does not implement it and does not need to.
type dataPutter interface {
	putData(*sessionData)
}

// Persist writes a session's request-scoped state (accounting, tool history,
// shadow lists, WebSocket counters) back to the store. Under a store that
// hands out copies (Redis) nothing else does: a Get returns the last
// written state, not the live struct a request has been mutating.
//
// It is a read-modify-write, not a bare Put, because the lifecycle methods
// (Kill, Terminate, Resume, Complete, timeout) each re-read and write the
// stored session: a bare Put of a request's older copy would overwrite a
// concurrent kill with Active. Persist overwrites the lifecycle keys (State,
// EndTime, Terminated) on the wire data with the stored session's, and never
// on the live session: a stored non-Active state must not flip a request
// that is still running (Runner.Bind refuses non-Active sessions, Kill and
// Terminate gate on IsActive). It writes nothing if the stored session is
// already gone or is a different instance under a reused ID, and does nothing
// for a store that shares live pointers (MemoryStore).
//
// Residual race: the guarantees above (not resurrecting a deleted or expired
// session, keeping a concurrent kill or terminate) hold only outside the
// GET->SET window below, which is one round trip and is not atomic. A delete
// or expiry landing in it can be undone by the write, and a kill or terminate
// landing in it can revert the stored lifecycle to Active. LastActivity and
// RequestCount can also regress, because the write carries this request's
// older copy. Concurrent requests for one session each hold their own copy
// under Redis, so their counters are last-writer-wins as well. Closing the
// window needs a compare-and-set (a Lua script or WATCH/MULTI) in the store;
// that is the follow-up.
func (m *Manager) Persist(sess *Session) {
	if sess == nil {
		return
	}
	dp, ok := m.store.(dataPutter)
	if !ok {
		return
	}
	cur, ok := m.store.Get(sess.ID)
	if !ok || cur == sess || !cur.StartTime.Equal(sess.StartTime) {
		return
	}
	sd := sessionToData(sess)
	l := cur.lifecycleState()
	sd.State, sd.EndTime, sd.Terminated = l.State, l.EndTime, l.Terminated
	dp.putData(sd)
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
		_ = rs.PublishKill(id)
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
		_ = rs.PublishKill(id)
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

	// Persist completed state to store (important for Redis-backed deployments)
	m.store.Put(sess)

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
		if sess.IdleTime() <= m.timeout {
			continue
		}
		// Re-read before writing: under a copying store the listed copy can
		// predate a request's write-back, and the state must be written back
		// for the timeout to exist outside this process at all.
		cur, ok := m.store.Get(sess.ID)
		if !ok || !cur.IsActive() || cur.IdleTime() <= m.timeout {
			continue
		}
		cur.SetState(TimedOut)
		m.store.Put(cur)

		slog.Warn("session timed out",
			"session_id", cur.ID,
			"idle_time", cur.IdleTime(),
			"timeout", m.timeout,
		)
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

// DrainActiveSessions marks all active sessions as completed and invokes
// the session end callback for each. Called during graceful shutdown to
// ensure all session data is persisted and exported before process exit.
// Returns the number of sessions drained.
func (m *Manager) DrainActiveSessions() int {
	// Snapshot active sessions
	active := m.store.List(ActiveFilter)

	// Also grab timed-out sessions that haven't been cleaned up yet
	timedOut := m.store.List(func(s *Session) bool {
		return s.GetState() == TimedOut
	})

	drained := 0

	for _, sess := range active {
		sess.SetState(Completed)
		m.store.Put(sess)

		if m.onSessionEnd != nil {
			m.onSessionEnd(sess)
		}

		slog.Info("drained active session",
			"session_id", sess.ID,
			"duration", sess.Duration(),
			"requests", sess.RequestCount,
		)
		drained++
	}

	for _, sess := range timedOut {
		if m.onSessionEnd != nil {
			m.onSessionEnd(sess)
		}

		slog.Info("drained timed-out session",
			"session_id", sess.ID,
			"duration", sess.Duration(),
			"requests", sess.RequestCount,
		)
		drained++
	}

	return drained
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
