package session

import (
	"sync"
	"time"
)

// State represents the current state of a session
type State int

const (
	Active State = iota
	Completed
	Killed
	TimedOut
)

func (s State) String() string {
	switch s {
	case Active:
		return "active"
	case Completed:
		return "completed"
	case Killed:
		return "killed"
	case TimedOut:
		return "timeout"
	default:
		return "unknown"
	}
}

// Session represents an agent session
type Session struct {
	mu sync.RWMutex

	ID           string            `json:"id"`
	State        State             `json:"state"`
	StartTime    time.Time         `json:"start_time"`
	LastActivity time.Time         `json:"last_activity"`
	EndTime      *time.Time        `json:"end_time,omitempty"`
	RequestCount int               `json:"request_count"`
	BytesIn      int64             `json:"bytes_in"`
	BytesOut     int64             `json:"bytes_out"`
	Backend      string            `json:"backend"`
	ClientAddr   string            `json:"client_addr"`
	Metadata     map[string]string `json:"metadata,omitempty"`

	// For rate limiting - track recent request times
	RequestTimes []time.Time `json:"-"`

	// For kill signaling
	killChan chan struct{}
}

// NewSession creates a new session with the given ID
func NewSession(id, backend, clientAddr string) *Session {
	now := time.Now()
	return &Session{
		ID:           id,
		State:        Active,
		StartTime:    now,
		LastActivity: now,
		Backend:      backend,
		ClientAddr:   clientAddr,
		Metadata:     make(map[string]string),
		killChan:     make(chan struct{}),
	}
}

// Touch updates the last activity time and increments request count
func (s *Session) Touch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.LastActivity = now
	s.RequestCount++

	// Track request time for rate limiting (keep last 2 minutes)
	s.RequestTimes = append(s.RequestTimes, now)
	cutoff := now.Add(-2 * time.Minute)
	for len(s.RequestTimes) > 0 && s.RequestTimes[0].Before(cutoff) {
		s.RequestTimes = s.RequestTimes[1:]
	}
}

// GetRequestTimes returns a copy of recent request times
func (s *Session) GetRequestTimes() []time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]time.Time, len(s.RequestTimes))
	copy(result, s.RequestTimes)
	return result
}

// AddBytes adds bytes to the session counters
func (s *Session) AddBytes(in, out int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.BytesIn += in
	s.BytesOut += out
}

// SetState updates the session state
func (s *Session) SetState(state State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.State = state
	if state != Active {
		now := time.Now()
		s.EndTime = &now
	}
}

// GetState returns the current session state
func (s *Session) GetState() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.State
}

// IsActive returns true if the session is still active
func (s *Session) IsActive() bool {
	return s.GetState() == Active
}

// Kill signals the session to terminate
func (s *Session) Kill() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.State == Active {
		s.State = Killed
		now := time.Now()
		s.EndTime = &now
		close(s.killChan)
	}
}

// KillChan returns the channel that's closed when the session is killed
func (s *Session) KillChan() <-chan struct{} {
	return s.killChan
}

// Duration returns how long the session has been running
func (s *Session) Duration() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.EndTime != nil {
		return s.EndTime.Sub(s.StartTime)
	}
	return time.Since(s.StartTime)
}

// IdleTime returns how long since the last activity
func (s *Session) IdleTime() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return time.Since(s.LastActivity)
}

// SetMetadata sets a metadata key-value pair
func (s *Session) SetMetadata(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Metadata[key] = value
}

// Snapshot returns a copy of the session for safe reading
func (s *Session) Snapshot() Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	
	snap := Session{
		ID:           s.ID,
		State:        s.State,
		StartTime:    s.StartTime,
		LastActivity: s.LastActivity,
		EndTime:      s.EndTime,
		RequestCount: s.RequestCount,
		BytesIn:      s.BytesIn,
		BytesOut:     s.BytesOut,
		Backend:      s.Backend,
		ClientAddr:   s.ClientAddr,
		Metadata:     make(map[string]string, len(s.Metadata)),
	}
	for k, v := range s.Metadata {
		snap.Metadata[k] = v
	}
	return snap
}
