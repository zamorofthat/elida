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

	// Track all backends used and request count per backend
	BackendsUsed map[string]int `json:"backends_used,omitempty"`

	// Terminated sessions cannot be resumed (for malicious agents)
	Terminated bool `json:"terminated,omitempty"`

	// WebSocket session fields
	IsWebSocket  bool  `json:"is_websocket,omitempty"`
	FrameCount   int64 `json:"frame_count,omitempty"`
	TextFrames   int64 `json:"text_frames,omitempty"`
	BinaryFrames int64 `json:"binary_frames,omitempty"`

	// For rate limiting - track recent request times
	RequestTimes []time.Time `json:"-"`

	// For kill signaling
	killChan chan struct{}

	// Token tracking (for LLM API usage)
	TokensIn  int64 `json:"tokens_in"`
	TokensOut int64 `json:"tokens_out"`

	// Tool/function call tracking
	ToolCalls      int            `json:"tool_calls"`
	ToolCallCounts map[string]int `json:"tool_call_counts,omitempty"` // Per-tool counts

	// Tool call history (who called what)
	ToolCallHistory []ToolCallRecord `json:"tool_call_history,omitempty"`
}

// ToolCallRecord tracks a single tool/function call
type ToolCallRecord struct {
	Timestamp time.Time `json:"timestamp"`
	ToolName  string    `json:"tool_name"`
	ToolType  string    `json:"tool_type,omitempty"` // "function", "code_interpreter", etc.
	RequestID string    `json:"request_id,omitempty"`
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
		BackendsUsed: make(map[string]int),
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

// AddTokens adds token counts to the session
func (s *Session) AddTokens(in, out int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TokensIn += in
	s.TokensOut += out
}

// GetTokens returns current token counts
func (s *Session) GetTokens() (in, out int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.TokensIn, s.TokensOut
}

// RecordToolCall records a tool/function call
func (s *Session) RecordToolCall(toolName, toolType, requestID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ToolCalls++

	if s.ToolCallCounts == nil {
		s.ToolCallCounts = make(map[string]int)
	}
	s.ToolCallCounts[toolName]++

	// Keep history (limited to last 100 calls to avoid memory bloat)
	record := ToolCallRecord{
		Timestamp: time.Now(),
		ToolName:  toolName,
		ToolType:  toolType,
		RequestID: requestID,
	}
	s.ToolCallHistory = append(s.ToolCallHistory, record)
	if len(s.ToolCallHistory) > 100 {
		s.ToolCallHistory = s.ToolCallHistory[1:]
	}
}

// GetToolCalls returns total tool call count
func (s *Session) GetToolCalls() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ToolCalls
}

// GetToolFanout returns the number of distinct tools used
func (s *Session) GetToolFanout() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.ToolCallCounts)
}

// GetToolCallCounts returns a copy of per-tool call counts
func (s *Session) GetToolCallCounts() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]int, len(s.ToolCallCounts))
	for k, v := range s.ToolCallCounts {
		result[k] = v
	}
	return result
}

// GetToolCallHistory returns a copy of the tool call history
func (s *Session) GetToolCallHistory() []ToolCallRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]ToolCallRecord, len(s.ToolCallHistory))
	copy(result, s.ToolCallHistory)
	return result
}

// FrameType represents the type of WebSocket frame
type FrameType int

const (
	FrameText FrameType = iota
	FrameBinary
)

// FrameDirection represents the direction of a WebSocket frame
type FrameDirection int

const (
	FrameInbound FrameDirection = iota
	FrameOutbound
)

// AddFrame records a WebSocket frame and updates byte counters
func (s *Session) AddFrame(frameType FrameType, size int64, direction FrameDirection) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.FrameCount++
	s.LastActivity = time.Now()

	switch frameType {
	case FrameText:
		s.TextFrames++
	case FrameBinary:
		s.BinaryFrames++
	}

	switch direction {
	case FrameInbound:
		s.BytesIn += size
	case FrameOutbound:
		s.BytesOut += size
	}
}

// SetWebSocket marks this session as a WebSocket session
func (s *Session) SetWebSocket() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.IsWebSocket = true
}

// RecordBackend tracks which backend was used for a request
func (s *Session) RecordBackend(backend string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.BackendsUsed == nil {
		s.BackendsUsed = make(map[string]int)
	}
	s.BackendsUsed[backend]++
	// Also update the Backend field to show the last used backend
	s.Backend = backend
}

// GetBackendsUsed returns a copy of the backends used map
func (s *Session) GetBackendsUsed() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]int, len(s.BackendsUsed))
	for k, v := range s.BackendsUsed {
		result[k] = v
	}
	return result
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

// Kill signals the session to terminate (can be resumed later)
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

// Terminate permanently kills the session (cannot be resumed)
// Use this for malicious or runaway agents
func (s *Session) Terminate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.State == Active || s.State == Killed {
		s.State = Killed
		s.Terminated = true
		now := time.Now()
		s.EndTime = &now
		// Only close if not already closed
		select {
		case <-s.killChan:
			// Already closed
		default:
			close(s.killChan)
		}
	}
}

// IsTerminated returns true if the session was permanently terminated
func (s *Session) IsTerminated() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Terminated
}

// Resume reactivates a killed session, allowing new requests
// Returns false if session is terminated (cannot be resumed)
func (s *Session) Resume() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Terminated {
		return false // Cannot resume terminated sessions
	}
	if s.State == Killed {
		s.State = Active
		s.EndTime = nil
		s.LastActivity = time.Now()
		// Create new kill channel for future kill operations
		s.killChan = make(chan struct{})
		return true
	}
	return false
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

// Snapshot returns a copy of the session for safe reading.
// Note: Returns a new Session with a fresh zero-value mutex, not copying s.mu.
//
//nolint:govet // mutex field is zero-initialized, not copied from source
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
		BackendsUsed: make(map[string]int, len(s.BackendsUsed)),
		IsWebSocket:  s.IsWebSocket,
		FrameCount:   s.FrameCount,
		TextFrames:   s.TextFrames,
		BinaryFrames: s.BinaryFrames,
		TokensIn:     s.TokensIn,
		TokensOut:    s.TokensOut,
		ToolCalls:    s.ToolCalls,
	}
	for k, v := range s.Metadata {
		snap.Metadata[k] = v
	}
	for k, v := range s.BackendsUsed {
		snap.BackendsUsed[k] = v
	}
	if s.ToolCallCounts != nil {
		snap.ToolCallCounts = make(map[string]int, len(s.ToolCallCounts))
		for k, v := range s.ToolCallCounts {
			snap.ToolCallCounts[k] = v
		}
	}
	if s.ToolCallHistory != nil {
		snap.ToolCallHistory = make([]ToolCallRecord, len(s.ToolCallHistory))
		copy(snap.ToolCallHistory, s.ToolCallHistory)
	}
	return snap
}
