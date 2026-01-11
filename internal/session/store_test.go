package session

import (
	"testing"
)

func TestMemoryStore_PutAndGet(t *testing.T) {
	store := NewMemoryStore()
	sess := NewSession("test-id", "http://backend", "127.0.0.1")

	store.Put(sess)

	retrieved, ok := store.Get("test-id")
	if !ok {
		t.Fatal("expected to find session")
	}
	if retrieved.ID != sess.ID {
		t.Errorf("expected ID %s, got %s", sess.ID, retrieved.ID)
	}
}

func TestMemoryStore_GetNotFound(t *testing.T) {
	store := NewMemoryStore()

	_, ok := store.Get("nonexistent")
	if ok {
		t.Error("expected session not to be found")
	}
}

func TestMemoryStore_Delete(t *testing.T) {
	store := NewMemoryStore()
	sess := NewSession("test-id", "http://backend", "127.0.0.1")

	store.Put(sess)
	store.Delete("test-id")

	_, ok := store.Get("test-id")
	if ok {
		t.Error("expected session to be deleted")
	}
}

func TestMemoryStore_List(t *testing.T) {
	store := NewMemoryStore()

	sess1 := NewSession("id-1", "http://backend", "127.0.0.1")
	sess2 := NewSession("id-2", "http://backend", "127.0.0.1")
	sess3 := NewSession("id-3", "http://backend", "127.0.0.1")
	sess3.SetState(Completed)

	store.Put(sess1)
	store.Put(sess2)
	store.Put(sess3)

	// List all
	all := store.List(nil)
	if len(all) != 3 {
		t.Errorf("expected 3 sessions, got %d", len(all))
	}

	// List active only
	active := store.List(ActiveFilter)
	if len(active) != 2 {
		t.Errorf("expected 2 active sessions, got %d", len(active))
	}
}

func TestMemoryStore_Count(t *testing.T) {
	store := NewMemoryStore()

	sess1 := NewSession("id-1", "http://backend", "127.0.0.1")
	sess2 := NewSession("id-2", "http://backend", "127.0.0.1")
	sess2.SetState(Killed)

	store.Put(sess1)
	store.Put(sess2)

	// Count all
	if count := store.Count(nil); count != 2 {
		t.Errorf("expected count 2, got %d", count)
	}

	// Count active
	if count := store.Count(ActiveFilter); count != 1 {
		t.Errorf("expected active count 1, got %d", count)
	}
}

func TestActiveFilter(t *testing.T) {
	active := NewSession("active", "http://backend", "127.0.0.1")
	killed := NewSession("killed", "http://backend", "127.0.0.1")
	killed.Kill()

	if !ActiveFilter(active) {
		t.Error("expected ActiveFilter to return true for active session")
	}
	if ActiveFilter(killed) {
		t.Error("expected ActiveFilter to return false for killed session")
	}
}
