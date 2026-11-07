package session

import (
	"encoding/json"
	"sync"
)

// CopyStore is an in-process Store with RedisStore's semantics: every Get
// decodes a fresh session from the stored wire blob, so state mutated on a
// returned session is lost unless it is written back with Put, and nothing
// outside the wire format survives. It exists so unit tests can reproduce the
// Redis-only bug class without Redis or a new dependency. Production code
// never constructs it.
type CopyStore struct {
	mu    sync.RWMutex
	blobs map[string][]byte
	puts  int
}

var (
	_ Store      = (*CopyStore)(nil)
	_ dataPutter = (*CopyStore)(nil)
)

// NewCopyStore creates an empty CopyStore.
func NewCopyStore() *CopyStore {
	return &CopyStore{blobs: make(map[string][]byte)}
}

func decodeBlob(blob []byte) (*Session, bool) {
	var sd sessionData
	if err := json.Unmarshal(blob, &sd); err != nil {
		return nil, false
	}
	ch := make(chan struct{})
	if sd.State == Killed {
		close(ch)
	}
	return newSessionFromData(&sd, ch), true
}

// Get decodes a fresh copy of the stored session.
func (s *CopyStore) Get(id string) (*Session, bool) {
	s.mu.RLock()
	blob, ok := s.blobs[id]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return decodeBlob(blob)
}

// Put stores the session's wire form.
func (s *CopyStore) Put(sess *Session) {
	s.putData(sessionToData(sess))
}

// putData stores already-converted wire data (see Manager.Persist).
func (s *CopyStore) putData(sd *sessionData) {
	blob, err := json.Marshal(sd)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.blobs[sd.ID] = blob
	s.puts++
	s.mu.Unlock()
}

// Delete removes a session.
func (s *CopyStore) Delete(id string) {
	s.mu.Lock()
	delete(s.blobs, id)
	s.mu.Unlock()
}

// List returns fresh copies of the sessions matching the filter.
func (s *CopyStore) List(filter func(*Session) bool) []*Session {
	s.mu.RLock()
	blobs := make([][]byte, 0, len(s.blobs))
	for _, b := range s.blobs {
		blobs = append(blobs, b)
	}
	s.mu.RUnlock()

	var out []*Session
	for _, b := range blobs {
		sess, ok := decodeBlob(b)
		if ok && (filter == nil || filter(sess)) {
			out = append(out, sess)
		}
	}
	return out
}

// Count returns the number of sessions matching the filter.
func (s *CopyStore) Count(filter func(*Session) bool) int {
	return len(s.List(filter))
}

// Puts reports how many writes (Put or putData) happened.
func (s *CopyStore) Puts() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.puts
}

// Blob returns the stored JSON for a session, for content assertions.
func (s *CopyStore) Blob(id string) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.blobs[id]
	return append([]byte(nil), b...), ok
}
