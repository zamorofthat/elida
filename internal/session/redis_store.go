package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisConfig holds Redis connection configuration
type RedisConfig struct {
	Addr      string `yaml:"addr"`
	Password  string `yaml:"password"`
	DB        int    `yaml:"db"`
	KeyPrefix string `yaml:"key_prefix"`
}

// RedisStore implements Store interface using Redis
type RedisStore struct {
	client    *redis.Client
	keyPrefix string
	ttl       time.Duration

	// Local cache of kill channels (can't store channels in Redis)
	mu        sync.RWMutex
	killChans map[string]chan struct{}

	// Pub/sub for kill signals across instances
	pubsub    *redis.PubSub
	killTopic string
}

// NewRedisStore creates a new Redis-backed session store
func NewRedisStore(cfg RedisConfig, sessionTTL time.Duration) (*RedisStore, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	keyPrefix := cfg.KeyPrefix
	if keyPrefix == "" {
		keyPrefix = "elida:session:"
	}

	store := &RedisStore{
		client:    client,
		keyPrefix: keyPrefix,
		ttl:       sessionTTL + 5*time.Minute, // Keep slightly longer than session timeout
		killChans: make(map[string]chan struct{}),
		killTopic: "elida:kill",
	}

	// Subscribe to kill signals
	store.pubsub = client.Subscribe(ctx, store.killTopic)

	// Start listening for kill signals in background
	go store.listenForKillSignals()

	slog.Info("Redis store initialized",
		"addr", cfg.Addr,
		"key_prefix", keyPrefix,
	)

	return store, nil
}

// sessionKey returns the Redis key for a session
func (s *RedisStore) sessionKey(id string) string {
	return s.keyPrefix + id
}

// indexKey returns the Redis key for the session index
func (s *RedisStore) indexKey() string {
	return s.keyPrefix + "_index"
}

// Get retrieves a session by ID
func (s *RedisStore) Get(id string) (*Session, bool) {
	ctx := context.Background()

	data, err := s.client.Get(ctx, s.sessionKey(id)).Bytes()
	if err == redis.Nil {
		return nil, false
	}
	if err != nil {
		slog.Error("Redis Get error", "session_id", id, "error", err)
		return nil, false
	}

	var sd sessionData
	if err := json.Unmarshal(data, &sd); err != nil {
		slog.Error("Failed to unmarshal session", "session_id", id, "error", err)
		return nil, false
	}

	sess := s.sessionFromData(&sd)
	return sess, true
}

// Put stores a session
func (s *RedisStore) Put(session *Session) {
	ctx := context.Background()

	sd := sessionToData(session)
	data, err := json.Marshal(sd)
	if err != nil {
		slog.Error("Failed to marshal session", "session_id", session.ID, "error", err)
		return
	}

	// Store session with TTL
	if err := s.client.Set(ctx, s.sessionKey(session.ID), data, s.ttl).Err(); err != nil {
		slog.Error("Redis Set error", "session_id", session.ID, "error", err)
		return
	}

	// Add to index
	if err := s.client.SAdd(ctx, s.indexKey(), session.ID).Err(); err != nil {
		slog.Error("Redis SAdd error", "session_id", session.ID, "error", err)
	}

	// Ensure we have a local kill channel
	s.mu.Lock()
	if _, ok := s.killChans[session.ID]; !ok {
		s.killChans[session.ID] = make(chan struct{})
	}
	s.mu.Unlock()
}

// Delete removes a session
func (s *RedisStore) Delete(id string) {
	ctx := context.Background()

	if err := s.client.Del(ctx, s.sessionKey(id)).Err(); err != nil {
		slog.Error("Redis Del error", "session_id", id, "error", err)
	}

	if err := s.client.SRem(ctx, s.indexKey(), id).Err(); err != nil {
		slog.Error("Redis SRem error", "session_id", id, "error", err)
	}

	// Clean up local kill channel
	s.mu.Lock()
	if ch, ok := s.killChans[id]; ok {
		select {
		case <-ch:
			// Already closed
		default:
			close(ch)
		}
		delete(s.killChans, id)
	}
	s.mu.Unlock()
}

// List returns all sessions matching the filter
func (s *RedisStore) List(filter func(*Session) bool) []*Session {
	ctx := context.Background()

	// Get all session IDs from index
	ids, err := s.client.SMembers(ctx, s.indexKey()).Result()
	if err != nil {
		slog.Error("Redis SMembers error", "error", err)
		return nil
	}

	var result []*Session
	for _, id := range ids {
		sess, ok := s.Get(id)
		if !ok {
			// Session expired, remove from index
			s.client.SRem(ctx, s.indexKey(), id)
			continue
		}

		if filter == nil || filter(sess) {
			result = append(result, sess)
		}
	}

	return result
}

// Count returns the number of sessions matching the filter
func (s *RedisStore) Count(filter func(*Session) bool) int {
	sessions := s.List(filter)
	return len(sessions)
}

// PublishKill broadcasts a kill signal to all instances
func (s *RedisStore) PublishKill(sessionID string) error {
	ctx := context.Background()
	return s.client.Publish(ctx, s.killTopic, sessionID).Err()
}

// listenForKillSignals listens for kill signals from other instances
func (s *RedisStore) listenForKillSignals() {
	ch := s.pubsub.Channel()

	for msg := range ch {
		sessionID := msg.Payload
		slog.Debug("Received kill signal", "session_id", sessionID)

		s.mu.Lock()
		if ch, ok := s.killChans[sessionID]; ok {
			select {
			case <-ch:
				// Already closed
			default:
				close(ch)
			}
		}
		s.mu.Unlock()
	}
}

// GetKillChan returns the kill channel for a session
func (s *RedisStore) GetKillChan(id string) <-chan struct{} {
	s.mu.RLock()
	ch, ok := s.killChans[id]
	s.mu.RUnlock()

	if !ok {
		s.mu.Lock()
		ch = make(chan struct{})
		s.killChans[id] = ch
		s.mu.Unlock()
	}

	return ch
}

// Close closes the Redis connection
func (s *RedisStore) Close() error {
	if s.pubsub != nil {
		_ = s.pubsub.Close()
	}
	return s.client.Close()
}

// sessionFromData converts Redis data to a Session wired to the store's
// local kill channel for the session.
func (s *RedisStore) sessionFromData(sd *sessionData) *Session {
	// Get or create kill channel
	s.mu.Lock()
	ch, ok := s.killChans[sd.ID]
	if !ok {
		ch = make(chan struct{})
		s.killChans[sd.ID] = ch

		// If session is already killed, close the channel
		if sd.State == Killed {
			close(ch)
		}
	}
	s.mu.Unlock()

	return newSessionFromData(sd, ch)
}

// UpdateSession updates a session in Redis (call after modifying session)
func (s *RedisStore) UpdateSession(sess *Session) {
	s.Put(sess)
}
