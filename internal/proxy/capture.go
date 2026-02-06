package proxy

import (
	"sync"
	"time"
)

// CapturedRequest stores request/response content for capture-all mode.
// Matches the storage.CapturedRequest structure for easy conversion.
type CapturedRequest struct {
	Timestamp    time.Time
	Method       string
	Path         string
	RequestBody  string
	ResponseBody string
	StatusCode   int
}

// CaptureBuffer stores request/response bodies per session for capture-all mode.
// Thread-safe. Independent of the policy engine.
type CaptureBuffer struct {
	mu                   sync.Mutex
	sessions             map[string][]CapturedRequest
	maxCaptureSize       int
	maxCapturedPerSession int
}

// NewCaptureBuffer creates a new CaptureBuffer with the given limits.
func NewCaptureBuffer(maxCaptureSize, maxCapturedPerSession int) *CaptureBuffer {
	if maxCaptureSize <= 0 {
		maxCaptureSize = 10000 // 10KB default
	}
	if maxCapturedPerSession <= 0 {
		maxCapturedPerSession = 100
	}
	return &CaptureBuffer{
		sessions:             make(map[string][]CapturedRequest),
		maxCaptureSize:       maxCaptureSize,
		maxCapturedPerSession: maxCapturedPerSession,
	}
}

// Capture stores a request body for the given session.
// The body is truncated if it exceeds maxCaptureSize.
// No-op if the session has already reached maxCapturedPerSession entries.
func (cb *CaptureBuffer) Capture(sessionID string, req CapturedRequest) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	entries := cb.sessions[sessionID]
	if len(entries) >= cb.maxCapturedPerSession {
		return // Drop new captures once limit is reached
	}

	req.RequestBody = cb.truncate(req.RequestBody)
	cb.sessions[sessionID] = append(entries, req)
}

// UpdateLastResponse attaches a response body and status code to the most recent capture for the session.
func (cb *CaptureBuffer) UpdateLastResponse(sessionID, responseBody string, statusCode int) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	entries := cb.sessions[sessionID]
	if len(entries) == 0 {
		return
	}

	last := &entries[len(entries)-1]
	last.ResponseBody = cb.truncate(responseBody)
	last.StatusCode = statusCode
	cb.sessions[sessionID] = entries
}

// GetContent returns and removes all captured content for the session.
func (cb *CaptureBuffer) GetContent(sessionID string) []CapturedRequest {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	entries := cb.sessions[sessionID]
	delete(cb.sessions, sessionID)
	return entries
}

// PeekContent returns captured content without removing it.
func (cb *CaptureBuffer) PeekContent(sessionID string) []CapturedRequest {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	entries := cb.sessions[sessionID]
	if entries == nil {
		return nil
	}

	// Return a copy to avoid concurrent mutation
	result := make([]CapturedRequest, len(entries))
	copy(result, entries)
	return result
}

// HasContent returns true if there is captured content for the session.
func (cb *CaptureBuffer) HasContent(sessionID string) bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	return len(cb.sessions[sessionID]) > 0
}

// Remove deletes all captured content for the session.
func (cb *CaptureBuffer) Remove(sessionID string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	delete(cb.sessions, sessionID)
}

// truncate limits the string to maxCaptureSize bytes.
func (cb *CaptureBuffer) truncate(s string) string {
	if len(s) <= cb.maxCaptureSize {
		return s
	}
	return s[:cb.maxCaptureSize] + "...[truncated]"
}
