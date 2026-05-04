package mcp

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

const approvalTimeout = 10 * time.Minute

// ApprovalStatus represents the state of an approval request.
type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "pending"
	ApprovalApproved ApprovalStatus = "approved"
	ApprovalDenied   ApprovalStatus = "denied"
	ApprovalExpired  ApprovalStatus = "expired"
)

// ApprovalRequest holds a pending approval.
type ApprovalRequest struct {
	ID        string         `json:"id"`
	Action    string         `json:"action"`
	SessionID string         `json:"session_id"`
	Reason    string         `json:"reason,omitempty"`
	TokenName string         `json:"token_name"`
	Status    ApprovalStatus `json:"status"`
	CreatedAt time.Time      `json:"created_at"`
	ResolvedAt *time.Time    `json:"resolved_at,omitempty"`
}

// ApprovalQueue manages pending approval requests.
type ApprovalQueue struct {
	mu       sync.RWMutex
	requests map[string]*ApprovalRequest
}

// NewApprovalQueue creates an approval queue.
func NewApprovalQueue() *ApprovalQueue {
	q := &ApprovalQueue{
		requests: make(map[string]*ApprovalRequest),
	}
	go q.cleanup()
	return q
}

// Submit creates a new pending approval and returns its ID.
func (q *ApprovalQueue) Submit(action, sessionID, reason, tokenName string) string {
	id := uuid.New().String()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.requests[id] = &ApprovalRequest{
		ID:        id,
		Action:    action,
		SessionID: sessionID,
		Reason:    reason,
		TokenName: tokenName,
		Status:    ApprovalPending,
		CreatedAt: time.Now(),
	}
	return id
}

// Get returns an approval request by ID.
func (q *ApprovalQueue) Get(id string) *ApprovalRequest {
	q.mu.RLock()
	defer q.mu.RUnlock()
	r, ok := q.requests[id]
	if !ok {
		return nil
	}
	// Check expiry
	if r.Status == ApprovalPending && time.Since(r.CreatedAt) > approvalTimeout {
		r.Status = ApprovalExpired
	}
	copy := *r
	return &copy
}

// Approve approves a pending request.
func (q *ApprovalQueue) Approve(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	r, ok := q.requests[id]
	if !ok || r.Status != ApprovalPending {
		return false
	}
	if time.Since(r.CreatedAt) > approvalTimeout {
		r.Status = ApprovalExpired
		return false
	}
	now := time.Now()
	r.Status = ApprovalApproved
	r.ResolvedAt = &now
	return true
}

// Deny denies a pending request.
func (q *ApprovalQueue) Deny(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	r, ok := q.requests[id]
	if !ok || r.Status != ApprovalPending {
		return false
	}
	now := time.Now()
	r.Status = ApprovalDenied
	r.ResolvedAt = &now
	return true
}

// ListPending returns all pending approval requests.
func (q *ApprovalQueue) ListPending() []ApprovalRequest {
	q.mu.RLock()
	defer q.mu.RUnlock()
	var result []ApprovalRequest
	for _, r := range q.requests {
		if r.Status == ApprovalPending && time.Since(r.CreatedAt) <= approvalTimeout {
			result = append(result, *r)
		}
	}
	return result
}

func (q *ApprovalQueue) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		q.mu.Lock()
		for id, r := range q.requests {
			if time.Since(r.CreatedAt) > 2*approvalTimeout {
				delete(q.requests, id)
			}
		}
		q.mu.Unlock()
	}
}
