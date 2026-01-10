package session

import (
	"sync"
)

// Store defines the interface for session storage
type Store interface {
	// Get retrieves a session by ID
	Get(id string) (*Session, bool)
	
	// Put stores a session
	Put(session *Session)
	
	// Delete removes a session
	Delete(id string)
	
	// List returns all sessions matching the filter
	List(filter func(*Session) bool) []*Session
	
	// Count returns the number of sessions matching the filter
	Count(filter func(*Session) bool) int
}

// MemoryStore is an in-memory session store
type MemoryStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewMemoryStore creates a new in-memory session store
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions: make(map[string]*Session),
	}
}

// Get retrieves a session by ID
func (s *MemoryStore) Get(id string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	return sess, ok
}

// Put stores a session
func (s *MemoryStore) Put(session *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.ID] = session
}

// Delete removes a session
func (s *MemoryStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

// List returns all sessions matching the filter
func (s *MemoryStore) List(filter func(*Session) bool) []*Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	
	var result []*Session
	for _, sess := range s.sessions {
		if filter == nil || filter(sess) {
			result = append(result, sess)
		}
	}
	return result
}

// Count returns the number of sessions matching the filter
func (s *MemoryStore) Count(filter func(*Session) bool) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	
	count := 0
	for _, sess := range s.sessions {
		if filter == nil || filter(sess) {
			count++
		}
	}
	return count
}

// ActiveFilter returns a filter for active sessions
func ActiveFilter(s *Session) bool {
	return s.GetState() == Active
}
