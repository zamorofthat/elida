package websocket

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"elida/internal/policy"

	"github.com/google/uuid"
)

// VoiceSessionState represents the state of a voice session (SIP-inspired)
type VoiceSessionState int

const (
	// VoiceSessionIdle - No active voice session
	VoiceSessionIdle VoiceSessionState = iota
	// VoiceSessionInviting - INVITE sent, waiting for OK
	VoiceSessionInviting
	// VoiceSessionActive - Voice session is active
	VoiceSessionActive
	// VoiceSessionHeld - Voice session is on hold
	VoiceSessionHeld
	// VoiceSessionTerminating - BYE sent, waiting for confirmation
	VoiceSessionTerminating
	// VoiceSessionTerminated - Voice session has ended
	VoiceSessionTerminated
)

func (s VoiceSessionState) String() string {
	switch s {
	case VoiceSessionIdle:
		return "idle"
	case VoiceSessionInviting:
		return "inviting"
	case VoiceSessionActive:
		return "active"
	case VoiceSessionHeld:
		return "held"
	case VoiceSessionTerminating:
		return "terminating"
	case VoiceSessionTerminated:
		return "terminated"
	default:
		return "unknown"
	}
}

// TranscriptEntry represents a single utterance in the conversation
type TranscriptEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Speaker   string    `json:"speaker"` // "user" or "assistant"
	Text      string    `json:"text"`
	IsFinal   bool      `json:"is_final"` // false for interim results
	Source    string    `json:"source"`   // "stt", "tts", "text"
}

// VoiceSession represents a single voice conversation within a WebSocket connection
// This follows the SIP model where multiple calls can occur over one connection
type VoiceSession struct {
	mu sync.RWMutex

	// Identity
	ID              string            `json:"id"`
	ParentSessionID string            `json:"parent_session_id"` // WebSocket session ID
	State           VoiceSessionState `json:"state"`

	// Timing
	StartTime  time.Time  `json:"start_time"`
	AnswerTime *time.Time `json:"answer_time,omitempty"` // When session became active
	EndTime    *time.Time `json:"end_time,omitempty"`
	HoldTime   *time.Time `json:"hold_time,omitempty"` // When put on hold

	// Metadata (from INVITE)
	Metadata map[string]string `json:"metadata,omitempty"`
	Model    string            `json:"model,omitempty"`    // AI model being used
	Voice    string            `json:"voice,omitempty"`    // Voice ID for TTS
	Language string            `json:"language,omitempty"` // Language code

	// Metrics
	AudioFramesIn   int64 `json:"audio_frames_in"`
	AudioFramesOut  int64 `json:"audio_frames_out"`
	TextFramesIn    int64 `json:"text_frames_in"`
	TextFramesOut   int64 `json:"text_frames_out"`
	AudioBytesIn    int64 `json:"audio_bytes_in"`
	AudioBytesOut   int64 `json:"audio_bytes_out"`
	AudioDurationMs int64 `json:"audio_duration_ms"` // Estimated audio duration
	TurnCount       int   `json:"turn_count"`        // Number of conversation turns

	// Transcript - what was said during the session
	Transcript []TranscriptEntry `json:"transcript,omitempty"`

	// For termination signaling
	byeChan chan struct{}
}

// NewVoiceSession creates a new voice session
func NewVoiceSession(parentSessionID string) *VoiceSession {
	return &VoiceSession{
		ID:              uuid.New().String()[:8], // Short ID for voice sessions
		ParentSessionID: parentSessionID,
		State:           VoiceSessionInviting,
		StartTime:       time.Now(),
		Metadata:        make(map[string]string),
		Transcript:      make([]TranscriptEntry, 0),
		byeChan:         make(chan struct{}),
	}
}

// Activate transitions the voice session to active state (INVITE accepted)
func (v *VoiceSession) Activate() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.State == VoiceSessionInviting {
		v.State = VoiceSessionActive
		now := time.Now()
		v.AnswerTime = &now
	}
}

// Hold puts the voice session on hold
func (v *VoiceSession) Hold() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.State == VoiceSessionActive {
		v.State = VoiceSessionHeld
		now := time.Now()
		v.HoldTime = &now
		return true
	}
	return false
}

// Resume takes the voice session off hold
func (v *VoiceSession) Resume() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.State == VoiceSessionHeld {
		v.State = VoiceSessionActive
		v.HoldTime = nil
		return true
	}
	return false
}

// Terminate ends the voice session (BYE)
func (v *VoiceSession) Terminate(reason string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.State == VoiceSessionTerminated {
		return
	}
	v.State = VoiceSessionTerminated
	now := time.Now()
	v.EndTime = &now
	if reason != "" {
		v.Metadata["termination_reason"] = reason
	}
	// Signal termination
	select {
	case <-v.byeChan:
		// Already closed
	default:
		close(v.byeChan)
	}
}

// ByeChan returns channel that's closed when session is terminated
func (v *VoiceSession) ByeChan() <-chan struct{} {
	return v.byeChan
}

// IsActive returns true if the voice session is active or held
func (v *VoiceSession) IsActive() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.State == VoiceSessionActive || v.State == VoiceSessionHeld
}

// GetState returns the current state
func (v *VoiceSession) GetState() VoiceSessionState {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.State
}

// Duration returns the total duration of the voice session
func (v *VoiceSession) Duration() time.Duration {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.EndTime != nil {
		return v.EndTime.Sub(v.StartTime)
	}
	return time.Since(v.StartTime)
}

// TalkTime returns the active talk time (excluding hold time)
func (v *VoiceSession) TalkTime() time.Duration {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.AnswerTime == nil {
		return 0
	}
	end := time.Now()
	if v.EndTime != nil {
		end = *v.EndTime
	}
	return end.Sub(*v.AnswerTime)
}

// AddAudioFrame records an audio frame
func (v *VoiceSession) AddAudioFrame(size int64, inbound bool, estimatedDurationMs int64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if inbound {
		v.AudioFramesIn++
		v.AudioBytesIn += size
	} else {
		v.AudioFramesOut++
		v.AudioBytesOut += size
	}
	v.AudioDurationMs += estimatedDurationMs
}

// AddTextFrame records a text frame
func (v *VoiceSession) AddTextFrame(inbound bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if inbound {
		v.TextFramesIn++
	} else {
		v.TextFramesOut++
	}
}

// IncrementTurnCount increments the conversation turn counter
func (v *VoiceSession) IncrementTurnCount() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.TurnCount++
}

// SetMetadata sets metadata from INVITE message
func (v *VoiceSession) SetMetadata(key, value string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.Metadata[key] = value
}

// AddTranscript adds a transcript entry to the session
func (v *VoiceSession) AddTranscript(speaker, text, source string, isFinal bool) {
	if text == "" {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.Transcript = append(v.Transcript, TranscriptEntry{
		Timestamp: time.Now(),
		Speaker:   speaker,
		Text:      text,
		IsFinal:   isFinal,
		Source:    source,
	})
}

// GetTranscript returns a copy of the transcript
func (v *VoiceSession) GetTranscript() []TranscriptEntry {
	v.mu.RLock()
	defer v.mu.RUnlock()
	result := make([]TranscriptEntry, len(v.Transcript))
	copy(result, v.Transcript)
	return result
}

// GetFullTranscript returns the entire transcript as a formatted string
// Format: "user: Hello\nassistant: Hi there\n..."
func (v *VoiceSession) GetFullTranscript() string {
	v.mu.RLock()
	defer v.mu.RUnlock()

	if len(v.Transcript) == 0 {
		return ""
	}

	var sb strings.Builder
	for _, entry := range v.Transcript {
		if entry.IsFinal && entry.Text != "" {
			sb.WriteString(entry.Speaker)
			sb.WriteString(": ")
			sb.WriteString(entry.Text)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// Snapshot returns a copy of the voice session for safe reading.
// Note: Returns a new VoiceSession with a fresh zero-value mutex, not copying v.mu.
//
//nolint:govet // mutex field is zero-initialized, not copied from source
func (v *VoiceSession) Snapshot() VoiceSession {
	v.mu.RLock()
	defer v.mu.RUnlock()

	snap := VoiceSession{
		ID:              v.ID,
		ParentSessionID: v.ParentSessionID,
		State:           v.State,
		StartTime:       v.StartTime,
		AnswerTime:      v.AnswerTime,
		EndTime:         v.EndTime,
		HoldTime:        v.HoldTime,
		Model:           v.Model,
		Voice:           v.Voice,
		Language:        v.Language,
		AudioFramesIn:   v.AudioFramesIn,
		AudioFramesOut:  v.AudioFramesOut,
		TextFramesIn:    v.TextFramesIn,
		TextFramesOut:   v.TextFramesOut,
		AudioBytesIn:    v.AudioBytesIn,
		AudioBytesOut:   v.AudioBytesOut,
		AudioDurationMs: v.AudioDurationMs,
		TurnCount:       v.TurnCount,
		Metadata:        make(map[string]string, len(v.Metadata)),
		Transcript:      make([]TranscriptEntry, len(v.Transcript)),
	}
	for k, val := range v.Metadata {
		snap.Metadata[k] = val
	}
	copy(snap.Transcript, v.Transcript)
	return snap
}

// VoiceSessionManager manages voice sessions within a WebSocket connection
type VoiceSessionManager struct {
	mu sync.RWMutex

	parentSessionID string
	sessions        map[string]*VoiceSession
	activeSession   *VoiceSession   // Current active voice session
	history         []*VoiceSession // Completed sessions

	// Limits
	maxConcurrent int

	// Callbacks
	onSessionStart func(vs *VoiceSession)
	onSessionEnd   func(vs *VoiceSession)

	// Policy engine for post-session transcript scanning (telecom CDR model)
	policyEngine *policy.Engine
}

// NewVoiceSessionManager creates a new manager for a WebSocket session
func NewVoiceSessionManager(parentSessionID string, maxConcurrent int) *VoiceSessionManager {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &VoiceSessionManager{
		parentSessionID: parentSessionID,
		sessions:        make(map[string]*VoiceSession),
		maxConcurrent:   maxConcurrent,
	}
}

// SetCallbacks sets the session lifecycle callbacks
func (m *VoiceSessionManager) SetCallbacks(onStart, onEnd func(vs *VoiceSession)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onSessionStart = onStart
	m.onSessionEnd = onEnd
}

// SetPolicyEngine sets the policy engine for post-session transcript scanning
func (m *VoiceSessionManager) SetPolicyEngine(engine *policy.Engine) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policyEngine = engine
}

// StartSession creates a new voice session (INVITE)
func (m *VoiceSessionManager) StartSession() (*VoiceSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check concurrent session limit
	// Count all sessions in the map (includes Inviting, Active, Held states)
	// Terminated sessions are moved to history and removed from this map
	if len(m.sessions) >= m.maxConcurrent {
		return nil, ErrMaxConcurrentSessions
	}

	vs := NewVoiceSession(m.parentSessionID)
	m.sessions[vs.ID] = vs
	m.activeSession = vs

	if m.onSessionStart != nil {
		go m.onSessionStart(vs)
	}

	return vs, nil
}

// ActivateSession marks session as active (INVITE OK received)
func (m *VoiceSessionManager) ActivateSession(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	vs, ok := m.sessions[id]
	if !ok {
		return false
	}
	vs.Activate()
	return true
}

// EndSession terminates a voice session (BYE)
func (m *VoiceSessionManager) EndSession(id string, reason string) bool {
	m.mu.Lock()
	vs, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return false
	}

	vs.Terminate(reason)

	// Move to history
	m.history = append(m.history, vs)
	delete(m.sessions, id)

	if m.activeSession != nil && m.activeSession.ID == id {
		m.activeSession = nil
	}

	callback := m.onSessionEnd
	policyEngine := m.policyEngine
	parentSessionID := m.parentSessionID
	m.mu.Unlock()

	if callback != nil {
		callback(vs)
	}

	// Post-session transcript scanning (telecom CDR model)
	// Scan completed transcript for policy violations
	if policyEngine != nil {
		m.scanTranscript(vs, policyEngine, parentSessionID)
	}

	return true
}

// scanTranscript performs post-session policy scanning on the voice transcript
func (m *VoiceSessionManager) scanTranscript(vs *VoiceSession, engine *policy.Engine, parentSessionID string) {
	transcript := vs.GetFullTranscript()
	if transcript == "" {
		return
	}

	snap := vs.Snapshot()

	slog.Debug("scanning voice session transcript",
		"voice_session_id", vs.ID,
		"parent_session_id", parentSessionID,
		"transcript_length", len(transcript),
		"turns", snap.TurnCount,
	)

	// Scan user speech as request content
	// Scan assistant speech as response content
	// For simplicity, we scan the full transcript and let rules match
	result := engine.EvaluateContent(parentSessionID, transcript)

	if result != nil && len(result.Violations) > 0 {
		// Compute max severity from violations
		maxSeverity := computeMaxSeverity(result.Violations)

		slog.Warn("voice session transcript violations detected",
			"voice_session_id", vs.ID,
			"parent_session_id", parentSessionID,
			"violations", len(result.Violations),
			"max_severity", maxSeverity,
		)

		// Capture the transcript as content for the flagged session
		engine.CaptureRequest(parentSessionID, policy.CapturedRequest{
			Timestamp:   snap.StartTime,
			Method:      "VOICE",
			Path:        "/voice/" + vs.ID,
			RequestBody: transcript,
		})

		// Add voice session metadata to help with investigation
		vs.SetMetadata("policy_violations", "true")
		vs.SetMetadata("max_severity", string(maxSeverity))
	}
}

// computeMaxSeverity returns the highest severity from a list of violations
func computeMaxSeverity(violations []policy.Violation) policy.Severity {
	if len(violations) == 0 {
		return policy.SeverityInfo
	}

	severityOrder := map[policy.Severity]int{
		policy.SeverityInfo:     0,
		policy.SeverityWarning:  1,
		policy.SeverityCritical: 2,
	}

	maxSeverity := policy.SeverityInfo
	maxOrder := 0

	for _, v := range violations {
		if order, ok := severityOrder[v.Severity]; ok && order > maxOrder {
			maxOrder = order
			maxSeverity = v.Severity
		}
	}

	return maxSeverity
}

// EndActiveSession terminates the current active session
func (m *VoiceSessionManager) EndActiveSession(reason string) bool {
	m.mu.RLock()
	active := m.activeSession
	m.mu.RUnlock()

	if active == nil {
		return false
	}
	return m.EndSession(active.ID, reason)
}

// HoldSession puts a session on hold
func (m *VoiceSessionManager) HoldSession(id string) bool {
	m.mu.RLock()
	vs, ok := m.sessions[id]
	m.mu.RUnlock()

	if !ok {
		return false
	}
	return vs.Hold()
}

// ResumeSession takes a session off hold
func (m *VoiceSessionManager) ResumeSession(id string) bool {
	m.mu.RLock()
	vs, ok := m.sessions[id]
	m.mu.RUnlock()

	if !ok {
		return false
	}
	return vs.Resume()
}

// GetSession returns a session by ID
func (m *VoiceSessionManager) GetSession(id string) (*VoiceSession, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	vs, ok := m.sessions[id]
	return vs, ok
}

// GetActiveSession returns the current active session
func (m *VoiceSessionManager) GetActiveSession() *VoiceSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeSession
}

// ListSessions returns all active sessions
func (m *VoiceSessionManager) ListSessions() []*VoiceSession {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*VoiceSession, 0, len(m.sessions))
	for _, vs := range m.sessions {
		result = append(result, vs)
	}
	return result
}

// ListHistory returns completed sessions
func (m *VoiceSessionManager) ListHistory() []*VoiceSession {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*VoiceSession, len(m.history))
	copy(result, m.history)
	return result
}

// EndAll terminates all active sessions (WebSocket closing)
func (m *VoiceSessionManager) EndAll(reason string) {
	m.mu.Lock()
	sessions := make([]*VoiceSession, 0, len(m.sessions))
	for _, vs := range m.sessions {
		sessions = append(sessions, vs)
	}
	m.mu.Unlock()

	for _, vs := range sessions {
		m.EndSession(vs.ID, reason)
	}
}

// Stats returns aggregate statistics
func (m *VoiceSessionManager) Stats() VoiceSessionStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := VoiceSessionStats{}

	// Active sessions
	for _, vs := range m.sessions {
		snap := vs.Snapshot()
		stats.ActiveSessions++
		stats.TotalAudioBytesIn += snap.AudioBytesIn
		stats.TotalAudioBytesOut += snap.AudioBytesOut
		stats.TotalAudioDurationMs += snap.AudioDurationMs
		stats.TotalTurns += snap.TurnCount
	}

	// Historical sessions
	for _, vs := range m.history {
		stats.CompletedSessions++
		stats.TotalAudioBytesIn += vs.AudioBytesIn
		stats.TotalAudioBytesOut += vs.AudioBytesOut
		stats.TotalAudioDurationMs += vs.AudioDurationMs
		stats.TotalTurns += vs.TurnCount
	}

	return stats
}

// VoiceSessionStats holds aggregate voice session statistics
type VoiceSessionStats struct {
	ActiveSessions       int   `json:"active_sessions"`
	CompletedSessions    int   `json:"completed_sessions"`
	TotalAudioBytesIn    int64 `json:"total_audio_bytes_in"`
	TotalAudioBytesOut   int64 `json:"total_audio_bytes_out"`
	TotalAudioDurationMs int64 `json:"total_audio_duration_ms"`
	TotalTurns           int   `json:"total_turns"`
}

// Errors
type voiceSessionError string

func (e voiceSessionError) Error() string { return string(e) }

const (
	ErrMaxConcurrentSessions voiceSessionError = "maximum concurrent voice sessions reached"
	ErrSessionNotFound       voiceSessionError = "voice session not found"
	ErrSessionNotActive      voiceSessionError = "voice session is not active"
)
