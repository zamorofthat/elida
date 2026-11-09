package session

import (
	"maps"
	"slices"
	"time"
)

// sessionData is the JSON-serializable session data for Redis.
//
// It holds the durable, content-free session state: lifecycle, accounting,
// tool-call projection, WebSocket counters and both shadow lists. Request
// and conversation content (Messages, SystemPrompt, ToolCallRecord
// Arguments and Result) is deliberately absent, and so is RequestTimes (the
// rate-limit window). Session history fields reach SQLite through the
// session-end callback, not through Redis; a reused session ID starts clean
// here, and history across sessions that shared an ID is merged only by
// storage.SQLiteStore.SaveSession.
//
// Times are time.Time, whose JSON form is RFC3339Nano: StartTime must keep
// sub-second precision because session instance identity (Manager.Persist,
// runner.sameInstance, runner.shadowKey) is StartTime equality.
//
// Every key but the first eleven is omitempty, so a blob written by an older
// replica still decodes. A replica that predates this format drops the newer
// keys when it rewrites a session: upgrade replicas together.
type sessionData struct {
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

	BackendsUsed map[string]int `json:"backends_used,omitempty"`
	Terminated   bool           `json:"terminated,omitempty"`

	IsWebSocket  bool  `json:"is_websocket,omitempty"`
	FrameCount   int64 `json:"frame_count,omitempty"`
	TextFrames   int64 `json:"text_frames,omitempty"`
	BinaryFrames int64 `json:"binary_frames,omitempty"`

	TokensIn        int64          `json:"tokens_in,omitempty"`
	TokensOut       int64          `json:"tokens_out,omitempty"`
	ToolCalls       int            `json:"tool_calls,omitempty"`
	ToolCallCounts  map[string]int `json:"tool_call_counts,omitempty"`
	ToolCallHistory []toolCallWire `json:"tool_call_history,omitempty"`
	FailedBackends  []string       `json:"failed_backends,omitempty"`

	// The shadow structs are numbers, enums, identifiers and timestamps;
	// TestSemanticShadow_CarriesNoContentFields proves the first content-free.
	SemanticShadow    []SemanticShadow    `json:"semantic_shadow,omitempty"`
	CorrelationShadow []CorrelationShadow `json:"correlation_shadow,omitempty"`
}

// toolCallWire is the content-free projection of a ToolCallRecord: exactly
// the fields the tool_sequence fingerprint features use. Arguments and
// Result can carry content and never ride.
type toolCallWire struct {
	Timestamp time.Time `json:"timestamp"`
	ToolName  string    `json:"tool_name"`
	ToolType  string    `json:"tool_type,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
}

// maxToolCallHistory caps the tool-call history, both in memory
// (RecordToolCall) and when a blob is decoded (newSessionFromData).
const maxToolCallHistory = 100

// sessionToData converts a session to its wire form. Every map and slice is
// copied under the session lock: the caller marshals after the lock is
// released, while request goroutines keep mutating the session.
func sessionToData(sess *Session) *sessionData {
	sess.mu.RLock()
	defer sess.mu.RUnlock()

	sd := &sessionData{
		ID:                sess.ID,
		State:             sess.State,
		StartTime:         sess.StartTime,
		LastActivity:      sess.LastActivity,
		RequestCount:      sess.RequestCount,
		BytesIn:           sess.BytesIn,
		BytesOut:          sess.BytesOut,
		Backend:           sess.Backend,
		ClientAddr:        sess.ClientAddr,
		Metadata:          maps.Clone(sess.Metadata),
		BackendsUsed:      maps.Clone(sess.BackendsUsed),
		Terminated:        sess.Terminated,
		IsWebSocket:       sess.IsWebSocket,
		FrameCount:        sess.FrameCount,
		TextFrames:        sess.TextFrames,
		BinaryFrames:      sess.BinaryFrames,
		TokensIn:          sess.TokensIn,
		TokensOut:         sess.TokensOut,
		ToolCalls:         sess.ToolCalls,
		ToolCallCounts:    maps.Clone(sess.ToolCallCounts),
		FailedBackends:    slices.Clone(sess.FailedBackends),
		SemanticShadow:    slices.Clone(sess.SemanticShadow),
		CorrelationShadow: cloneCorrelationShadow(sess.CorrelationShadow),
	}
	if sess.EndTime != nil {
		t := *sess.EndTime
		sd.EndTime = &t
	}
	if n := len(sess.ToolCallHistory); n > 0 {
		sd.ToolCallHistory = make([]toolCallWire, n)
		for i, r := range sess.ToolCallHistory {
			sd.ToolCallHistory[i] = toolCallWire{Timestamp: r.Timestamp, ToolName: r.ToolName, ToolType: r.ToolType, RequestID: r.RequestID}
		}
	}
	return sd
}

// newSessionFromData converts wire data to a session using the given kill
// channel. The lists are clamped to the in-memory caps so a tampered or
// foreign blob cannot grow a session past them: the shadow lists are newest
// first (keep the head), the tool history is oldest first (keep the tail).
func newSessionFromData(sd *sessionData, killChan chan struct{}) *Session {
	sess := &Session{
		ID:                sd.ID,
		State:             sd.State,
		StartTime:         sd.StartTime,
		LastActivity:      sd.LastActivity,
		EndTime:           sd.EndTime,
		RequestCount:      sd.RequestCount,
		BytesIn:           sd.BytesIn,
		BytesOut:          sd.BytesOut,
		Backend:           sd.Backend,
		ClientAddr:        sd.ClientAddr,
		Metadata:          sd.Metadata,
		BackendsUsed:      sd.BackendsUsed,
		Terminated:        sd.Terminated,
		IsWebSocket:       sd.IsWebSocket,
		FrameCount:        sd.FrameCount,
		TextFrames:        sd.TextFrames,
		BinaryFrames:      sd.BinaryFrames,
		killChan:          killChan,
		TokensIn:          sd.TokensIn,
		TokensOut:         sd.TokensOut,
		ToolCalls:         sd.ToolCalls,
		ToolCallCounts:    sd.ToolCallCounts,
		FailedBackends:    sd.FailedBackends,
		SemanticShadow:    clampHead(sd.SemanticShadow, MaxSemanticShadow),
		CorrelationShadow: clampHead(sd.CorrelationShadow, MaxCorrelationShadow),
	}
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]string)
	}
	if sess.BackendsUsed == nil {
		sess.BackendsUsed = make(map[string]int)
	}
	hist := sd.ToolCallHistory
	if len(hist) > maxToolCallHistory {
		hist = hist[len(hist)-maxToolCallHistory:]
	}
	if len(hist) > 0 {
		sess.ToolCallHistory = make([]ToolCallRecord, len(hist))
		for i, r := range hist {
			sess.ToolCallHistory[i] = ToolCallRecord{Timestamp: r.Timestamp, ToolName: r.ToolName, ToolType: r.ToolType, RequestID: r.RequestID}
		}
	}
	return sess
}

func clampHead[T any](in []T, limit int) []T {
	if len(in) > limit {
		return in[:limit]
	}
	return in
}
