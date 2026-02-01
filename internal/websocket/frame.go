package websocket

import (
	"time"

	"github.com/coder/websocket"
)

// Frame represents a WebSocket frame with metadata
type Frame struct {
	Type      websocket.MessageType
	Data      []byte
	Timestamp time.Time
	Direction Direction
	Size      int
}

// Direction indicates the direction of a WebSocket frame
type Direction int

const (
	// Inbound is a frame from client to backend (through proxy)
	Inbound Direction = iota
	// Outbound is a frame from backend to client (through proxy)
	Outbound
)

func (d Direction) String() string {
	switch d {
	case Inbound:
		return "inbound"
	case Outbound:
		return "outbound"
	default:
		return "unknown"
	}
}

// NewFrame creates a new Frame with the current timestamp
func NewFrame(msgType websocket.MessageType, data []byte, direction Direction) *Frame {
	return &Frame{
		Type:      msgType,
		Data:      data,
		Timestamp: time.Now(),
		Direction: direction,
		Size:      len(data),
	}
}

// IsText returns true if this is a text frame
func (f *Frame) IsText() bool {
	return f.Type == websocket.MessageText
}

// IsBinary returns true if this is a binary frame
func (f *Frame) IsBinary() bool {
	return f.Type == websocket.MessageBinary
}

// FrameScanner provides frame-level content scanning for policy evaluation
// This is a placeholder for Phase 2: Policy Integration
type FrameScanner struct {
	sessionID     string
	scanTextOnly  bool
	overlapBuffer []byte
	overlapSize   int
}

// NewFrameScanner creates a new FrameScanner for policy evaluation
func NewFrameScanner(sessionID string, scanTextOnly bool, overlapSize int) *FrameScanner {
	return &FrameScanner{
		sessionID:    sessionID,
		scanTextOnly: scanTextOnly,
		overlapSize:  overlapSize,
	}
}

// ScanResult contains the result of frame scanning
type ScanResult struct {
	SessionID       string
	ShouldBlock     bool
	ShouldTerminate bool
	Violations      []string
}

// ScanFrame scans a frame for policy violations
// This is a placeholder for Phase 2: Policy Integration
func (s *FrameScanner) ScanFrame(frame *Frame) *ScanResult {
	// Skip binary frames if configured
	if s.scanTextOnly && frame.IsBinary() {
		return nil
	}

	// Phase 2 will implement:
	// - Call policy engine to evaluate frame content
	// - Return violations if any
	// - Handle block/terminate actions

	return nil
}

// Finalize performs any final scanning with remaining buffer content
// This is a placeholder for Phase 2: Policy Integration
func (s *FrameScanner) Finalize() *ScanResult {
	return nil
}
