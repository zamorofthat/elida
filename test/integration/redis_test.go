package integration

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"elida/internal/session"
)

// skipIfNoRedis skips the test if Redis is not available
func skipIfNoRedis(t *testing.T) {
	addr := getRedisAddr()

	client := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available, skipping test")
	}
	client.Close()
}

func getRedisAddr() string {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	return addr
}

func newTestRedisStore(t *testing.T) *session.RedisStore {
	addr := getRedisAddr()

	store, err := session.NewRedisStore(session.RedisConfig{
		Addr:      addr,
		KeyPrefix: "elida:integration-test:",
	}, 5*time.Minute)
	if err != nil {
		t.Fatalf("failed to create Redis store: %v", err)
	}

	// Clean up test keys before and after
	cleanupTestKeys(t, addr)
	t.Cleanup(func() {
		cleanupTestKeys(t, addr)
		store.Close()
	})

	return store
}

func cleanupTestKeys(_ *testing.T, addr string) {
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()

	ctx := context.Background()
	keys, _ := client.Keys(ctx, "elida:integration-test:*").Result()
	if len(keys) > 0 {
		client.Del(ctx, keys...)
	}
}

func TestRedisStore_BasicOperations(t *testing.T) {
	skipIfNoRedis(t)
	store := newTestRedisStore(t)

	// Test Put and Get
	sess := session.NewSession("redis-basic-test", "http://backend", "127.0.0.1")
	sess.AddBytes(100, 200)

	store.Put(sess)

	retrieved, ok := store.Get("redis-basic-test")
	if !ok {
		t.Fatal("expected to find session")
	}
	if retrieved.ID != sess.ID {
		t.Errorf("expected ID %s, got %s", sess.ID, retrieved.ID)
	}
	if retrieved.BytesIn != 100 {
		t.Errorf("expected BytesIn 100, got %d", retrieved.BytesIn)
	}
	if retrieved.BytesOut != 200 {
		t.Errorf("expected BytesOut 200, got %d", retrieved.BytesOut)
	}
}

func TestRedisStore_GetNotFound(t *testing.T) {
	skipIfNoRedis(t)
	store := newTestRedisStore(t)

	_, ok := store.Get("nonexistent-session")
	if ok {
		t.Error("expected session not to be found")
	}
}

func TestRedisStore_Delete(t *testing.T) {
	skipIfNoRedis(t)
	store := newTestRedisStore(t)

	sess := session.NewSession("delete-test", "http://backend", "127.0.0.1")
	store.Put(sess)
	store.Delete("delete-test")

	_, ok := store.Get("delete-test")
	if ok {
		t.Error("expected session to be deleted")
	}
}

func TestRedisStore_List(t *testing.T) {
	skipIfNoRedis(t)
	store := newTestRedisStore(t)

	sess1 := session.NewSession("list-1", "http://backend", "127.0.0.1")
	sess2 := session.NewSession("list-2", "http://backend", "127.0.0.1")
	sess3 := session.NewSession("list-3", "http://backend", "127.0.0.1")
	sess3.SetState(session.Completed)

	store.Put(sess1)
	store.Put(sess2)
	store.Put(sess3)

	// List all
	all := store.List(nil)
	if len(all) != 3 {
		t.Errorf("expected 3 sessions, got %d", len(all))
	}

	// List active only
	active := store.List(session.ActiveFilter)
	if len(active) != 2 {
		t.Errorf("expected 2 active sessions, got %d", len(active))
	}
}

func TestRedisStore_Count(t *testing.T) {
	skipIfNoRedis(t)
	store := newTestRedisStore(t)

	sess1 := session.NewSession("count-1", "http://backend", "127.0.0.1")
	sess2 := session.NewSession("count-2", "http://backend", "127.0.0.1")
	sess2.SetState(session.Killed)

	store.Put(sess1)
	store.Put(sess2)

	if count := store.Count(nil); count != 2 {
		t.Errorf("expected count 2, got %d", count)
	}

	if count := store.Count(session.ActiveFilter); count != 1 {
		t.Errorf("expected active count 1, got %d", count)
	}
}

func TestRedisStore_SessionState(t *testing.T) {
	skipIfNoRedis(t)
	store := newTestRedisStore(t)

	sess := session.NewSession("state-test", "http://backend", "127.0.0.1")
	store.Put(sess)

	// Update state
	sess.SetState(session.Killed)
	store.Put(sess)

	// Retrieve and verify
	retrieved, _ := store.Get("state-test")
	if retrieved.GetState() != session.Killed {
		t.Errorf("expected state Killed, got %s", retrieved.GetState())
	}
}

func TestRedisStore_Metadata(t *testing.T) {
	skipIfNoRedis(t)
	store := newTestRedisStore(t)

	sess := session.NewSession("metadata-test", "http://backend", "127.0.0.1")
	sess.SetMetadata("key1", "value1")
	sess.SetMetadata("key2", "value2")
	store.Put(sess)

	retrieved, _ := store.Get("metadata-test")
	if retrieved.Metadata["key1"] != "value1" {
		t.Error("metadata key1 mismatch")
	}
	if retrieved.Metadata["key2"] != "value2" {
		t.Error("metadata key2 mismatch")
	}
}

func TestRedisStore_KillPersistsAcrossRestart(t *testing.T) {
	skipIfNoRedis(t)
	store := newTestRedisStore(t)

	// Create manager and session
	manager := session.NewManager(store, 5*time.Minute)
	manager.GetOrCreate("restart-kill-test", "http://backend", "127.0.0.1")

	// Kill the session
	manager.Kill("restart-kill-test")

	// Simulate restart: create new manager with same store
	manager2 := session.NewManager(store, 5*time.Minute)

	// Try to reuse the killed session - should return nil
	sess := manager2.GetOrCreate("restart-kill-test", "http://backend", "127.0.0.1")
	if sess != nil {
		t.Error("expected killed session to be rejected after simulated restart")
	}
}

func TestRedisStore_KilledStateLoadsCorrectly(t *testing.T) {
	skipIfNoRedis(t)
	store := newTestRedisStore(t)

	// Create and kill a session
	sess := session.NewSession("load-killed", "http://backend", "127.0.0.1")
	sess.Kill()
	store.Put(sess)

	// Create a new store instance to simulate fresh load
	store2, err := session.NewRedisStore(session.RedisConfig{
		Addr:      getRedisAddr(),
		KeyPrefix: "elida:integration-test:",
	}, 5*time.Minute)
	if err != nil {
		t.Fatalf("failed to create second Redis store: %v", err)
	}
	defer store2.Close()

	// Retrieve from Redis
	retrieved, ok := store2.Get("load-killed")
	if !ok {
		t.Fatal("expected to find session")
	}

	// Verify state
	if retrieved.GetState() != session.Killed {
		t.Errorf("expected state Killed, got %s", retrieved.GetState())
	}

	// Verify kill channel is closed
	select {
	case <-retrieved.KillChan():
		// Expected - channel should be closed for killed session
	default:
		t.Error("expected kill channel to be closed for killed session")
	}
}

func TestRedisStore_KillChannel(t *testing.T) {
	skipIfNoRedis(t)
	store := newTestRedisStore(t)

	sess := session.NewSession("kill-chan-test", "http://backend", "127.0.0.1")
	store.Put(sess)

	// Get kill channel
	killChan := store.GetKillChan("kill-chan-test")

	// Verify it's not closed
	select {
	case <-killChan:
		t.Error("kill channel should not be closed yet")
	default:
		// Expected
	}

	// Publish kill via the store's method
	_ = store.PublishKill("kill-chan-test")

	// Wait a bit for the pub/sub message to be processed
	time.Sleep(100 * time.Millisecond)

	// Verify channel is now closed
	select {
	case <-killChan:
		// Expected - channel is closed
	default:
		t.Error("expected kill channel to be closed after publish")
	}
}

func TestRedisStore_ShadowAndAccountingSurviveRoundTrip(t *testing.T) {
	skipIfNoRedis(t)
	store := newTestRedisStore(t)
	manager := session.NewManager(store, 5*time.Minute)

	sess := manager.GetOrCreate("redis-parity-1", "http://backend", "127.0.0.1")
	sess.TouchAndRecord(10, "b1")
	sess.AddTokens(7, 9)
	sess.RecordToolCall("bash", "function", "req-1", `{"cmd":"SENTINEL-ARG"}`)
	sess.RecordSemanticShadow(session.SemanticShadow{DecisionID: "d1", Signal: "injection", Probability: 0.4, Model: "m"})
	sess.RecordCorrelationShadow(session.CorrelationShadow{MatchID: "m1", Rule: "r", EventIDs: []string{"d1"}})
	sess.RecordMessage("user", "SENTINEL-MSG", "b1")
	manager.Persist(sess)

	got, ok := store.Get("redis-parity-1")
	if !ok {
		t.Fatal("expected to find session")
	}
	if in, out := got.GetTokens(); in != 7 || out != 9 {
		t.Errorf("tokens = %d/%d", in, out)
	}
	if got.GetBackendsUsed()["b1"] != 1 {
		t.Errorf("backends used = %v", got.GetBackendsUsed())
	}
	if s := got.GetSemanticShadow(); len(s) != 1 || s[0].DecisionID != "d1" {
		t.Errorf("semantic shadow = %+v", s)
	}
	if c := got.GetCorrelationShadow(); len(c) != 1 || c[0].MatchID != "m1" {
		t.Errorf("correlation shadow = %+v", c)
	}
	if h := got.GetToolCallHistory(); len(h) != 1 || h[0].ToolName != "bash" || h[0].Arguments != nil {
		t.Errorf("tool history = %+v, want the content-free projection", h)
	}
	if len(got.GetMessages()) != 0 {
		t.Error("conversation content must not reach Redis")
	}

	client := redis.NewClient(&redis.Options{Addr: getRedisAddr()})
	defer client.Close()
	raw, err := client.Get(context.Background(), "elida:integration-test:redis-parity-1").Result()
	if err != nil {
		t.Fatalf("raw get: %v", err)
	}
	if strings.Contains(raw, "SENTINEL") {
		t.Errorf("content reached the Redis blob: %s", raw)
	}
}

func TestRedisStore_PersistKeepsAKillAndTerminate(t *testing.T) {
	skipIfNoRedis(t)
	store := newTestRedisStore(t)
	manager := session.NewManager(store, 5*time.Minute)

	sess := manager.GetOrCreate("redis-parity-kill", "http://backend", "127.0.0.1")
	manager.Terminate("redis-parity-kill")
	sess.AddTokens(1, 1) // the in-flight request finishes after the terminate
	manager.Persist(sess)

	got, ok := store.Get("redis-parity-kill")
	if !ok {
		t.Fatal("expected to find session")
	}
	if got.GetState() != session.Killed || !got.IsTerminated() {
		t.Errorf("state = %s terminated = %v; a terminated session must stay terminated (and unresumable) across Redis", got.GetState(), got.IsTerminated())
	}
	if !sess.IsActive() {
		t.Errorf("Persist must not change the live request's session: state = %s", sess.GetState())
	}
	if manager.Resume("redis-parity-kill") {
		t.Error("a terminated session must not resume")
	}
}

func TestRedisStore_SessionEndRecordIsCompleteUnderRedis(t *testing.T) {
	skipIfNoRedis(t)
	store := newTestRedisStore(t)
	manager := session.NewManager(store, 5*time.Minute)

	var gotIn, gotOut int64
	var gotShadows, gotCorrelation int
	manager.SetSessionEndCallback(func(s *session.Session) {
		gotIn, gotOut = s.GetTokens()
		gotShadows = len(s.GetSemanticShadow())
		gotCorrelation = len(s.GetCorrelationShadow())
	})

	sess := manager.GetOrCreate("redis-parity-end", "http://backend", "127.0.0.1")
	sess.AddTokens(3, 4)
	sess.RecordSemanticShadow(session.SemanticShadow{DecisionID: "d1"})
	sess.RecordCorrelationShadow(session.CorrelationShadow{MatchID: "m1"})
	manager.Persist(sess)
	manager.Kill("redis-parity-end") // exports the end-of-session record from a fresh read

	if gotIn != 3 || gotOut != 4 || gotShadows != 1 || gotCorrelation != 1 {
		t.Fatalf("end-of-session record saw tokens %d/%d, %d shadows, %d matches", gotIn, gotOut, gotShadows, gotCorrelation)
	}
}
