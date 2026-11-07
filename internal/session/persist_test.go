package session

import (
	"context"
	"testing"
	"time"
)

func TestCopyStore_GetReturnsIndependentCopies(t *testing.T) {
	store := NewCopyStore()
	s := NewSession("cs-1", "http://b", "127.0.0.1")
	s.AddTokens(3, 4)
	store.Put(s)

	a, ok := store.Get("cs-1")
	if !ok {
		t.Fatal("want the session")
	}
	b, _ := store.Get("cs-1")
	if a == s || a == b {
		t.Fatal("CopyStore.Get must return a fresh object each call, like RedisStore")
	}
	a.AddTokens(100, 100)
	if in, _ := b.GetTokens(); in != 3 {
		t.Fatalf("mutating one copy leaked into another: %d", in)
	}
	s.Kill()
	store.Put(s)
	k, _ := store.Get("cs-1")
	select {
	case <-k.KillChan():
	default:
		t.Fatal("a killed session's copy must carry a closed kill channel")
	}
}

// The bug class this spec fixes: request-scoped state mutated on a Get copy
// is invisible to the next Get unless something writes it back.
func TestManagerPersist_WritesRequestScopedStateBack(t *testing.T) {
	store := NewCopyStore()
	m := NewManager(store, time.Minute)
	sess := m.GetOrCreate("p-1", "http://b", "127.0.0.1")
	sess.AddTokens(10, 20)
	sess.RecordToolCall("bash", "function", "req-1", `{}`)
	sess.RecordSemanticShadow(SemanticShadow{DecisionID: "d1"})
	sess.RecordCorrelationShadow(CorrelationShadow{MatchID: "m1", EventIDs: []string{"d1"}})

	if got, _ := store.Get("p-1"); got.TokensIn != 0 || len(got.SemanticShadow) != 0 {
		t.Fatal("fixture: the copying store must lose state that was never written back")
	}

	m.Persist(sess)
	m.Complete("p-1")

	got, _ := store.Get("p-1")
	if in, out := got.GetTokens(); in != 10 || out != 20 {
		t.Fatalf("tokens = %d/%d", in, out)
	}
	if len(got.SemanticShadow) != 1 || len(got.CorrelationShadow) != 1 || len(got.ToolCallHistory) != 1 || got.ToolCalls != 1 {
		t.Fatalf("shadows/tool calls not persisted: %+v", got)
	}
	if got.GetState() != Completed {
		t.Fatalf("state = %v, want Completed (Complete re-reads the persisted state and keeps it)", got.GetState())
	}
}

func TestManagerPersist_KeepsAConcurrentKillAndTerminate(t *testing.T) {
	for _, terminate := range []bool{false, true} {
		store := NewCopyStore()
		m := NewManager(store, time.Minute)
		sess := m.GetOrCreate("p-kill", "http://b", "127.0.0.1")
		if terminate {
			m.Terminate("p-kill")
		} else {
			m.Kill("p-kill")
		}
		sess.AddTokens(1, 1) // the in-flight request finishes after the kill
		m.Persist(sess)

		// The live session of the still-running request is never touched.
		if !sess.IsActive() || sess.EndTime != nil || sess.IsTerminated() {
			t.Fatalf("terminate=%v: Persist mutated the live session: state=%v end=%v terminated=%v", terminate, sess.GetState(), sess.EndTime, sess.IsTerminated())
		}

		got, _ := store.Get("p-kill")
		if got.GetState() != Killed || got.EndTime == nil {
			t.Fatalf("terminate=%v: Persist clobbered the kill: state=%v end=%v", terminate, got.GetState(), got.EndTime)
		}
		if got.IsTerminated() != terminate {
			t.Fatalf("terminate=%v: Terminated = %v", terminate, got.IsTerminated())
		}
		if in, _ := got.GetTokens(); in != 1 {
			t.Fatalf("terminate=%v: the request's accounting must still be written: %d", terminate, in)
		}
	}
}

func TestManagerPersist_DoesNotResurrectADeletedSession(t *testing.T) {
	store := NewCopyStore()
	m := NewManager(store, time.Minute)
	sess := m.GetOrCreate("p-del", "http://b", "127.0.0.1")
	store.Delete("p-del")
	m.Persist(sess)
	if _, ok := store.Get("p-del"); ok {
		t.Fatal("Persist must not recreate a deleted or expired session")
	}
}

func TestManagerPersist_SkipsAReusedID(t *testing.T) {
	store := NewCopyStore()
	m := NewManager(store, time.Minute)
	old := m.GetOrCreate("p-reuse", "http://b", "127.0.0.1")
	m.Complete("p-reuse")
	time.Sleep(2 * time.Millisecond) // distinct start times
	fresh := m.GetOrCreate("p-reuse", "http://b", "127.0.0.1")
	if fresh.StartTime.Equal(old.StartTime) {
		t.Fatal("fixture: want two instances")
	}
	old.AddTokens(9, 9)
	m.Persist(old)
	got, _ := store.Get("p-reuse")
	if !got.StartTime.Equal(fresh.StartTime) {
		t.Fatal("the new instance must stay stored")
	}
	if in, _ := got.GetTokens(); in != 0 {
		t.Fatalf("the old instance's late write leaked into the new one: %d", in)
	}
}

// A stored lifecycle that moved on (another replica completed or timed the
// session out) must reach the stored copy as-is and must never flip the
// session a request is still using: Runner.Bind refuses non-Active sessions
// and Kill/Terminate gate on IsActive.
func TestManagerPersist_StoredNonActiveStateDoesNotFlipTheLiveSession(t *testing.T) {
	store := NewCopyStore()
	m := NewManager(store, time.Minute)
	sess := m.GetOrCreate("p-live", "http://b", "127.0.0.1")
	m.Complete("p-live") // stored lifecycle is now Completed

	sess.AddTokens(3, 3)
	m.Persist(sess)

	if !sess.IsActive() || sess.EndTime != nil {
		t.Fatalf("the running request's session was flipped: state=%v end=%v", sess.GetState(), sess.EndTime)
	}
	got, _ := store.Get("p-live")
	if got.GetState() != Completed || got.EndTime == nil {
		t.Fatalf("stored lifecycle must survive the write-back: state=%v end=%v", got.GetState(), got.EndTime)
	}
	if in, _ := got.GetTokens(); in != 3 {
		t.Fatalf("the accounting must still be written: %d", in)
	}
}

// putCountingMemoryStore counts Put calls on a MemoryStore.
type putCountingMemoryStore struct {
	*MemoryStore
	puts int
}

func (s *putCountingMemoryStore) Put(sess *Session) {
	s.puts++
	s.MemoryStore.Put(sess)
}

func TestManagerPersist_IsANoOpForMemoryStore(t *testing.T) {
	store := &putCountingMemoryStore{MemoryStore: NewMemoryStore()}
	m := NewManager(store, time.Minute)
	sess := m.GetOrCreate("p-mem", "http://b", "127.0.0.1")
	sess.AddTokens(2, 2)
	puts := store.puts
	m.Persist(sess)
	if store.puts != puts {
		t.Fatalf("Persist wrote to a MemoryStore: puts %d->%d", puts, store.puts)
	}
	got, _ := store.Get("p-mem")
	if got != sess {
		t.Fatal("memory store must keep handing out the shared live pointer")
	}
	if in, _ := got.GetTokens(); in != 2 {
		t.Fatalf("tokens = %d", in)
	}
}

func TestManagerPersist_NilSessionIsANoOp(t *testing.T) {
	store := NewCopyStore()
	m := NewManager(store, time.Minute)
	m.GetOrCreate("p-nil", "http://b", "127.0.0.1")
	puts, count := store.Puts(), store.Count(nil)
	m.Persist(nil)
	if store.Puts() != puts || store.Count(nil) != count {
		t.Fatalf("Persist(nil) wrote: puts %d->%d, count %d->%d", puts, store.Puts(), count, store.Count(nil))
	}
}

func TestCheckTimeouts_PersistsTimedOutState(t *testing.T) {
	store := NewCopyStore()
	m := NewManager(store, time.Millisecond)
	m.GetOrCreate("p-to", "http://b", "127.0.0.1")
	time.Sleep(10 * time.Millisecond)
	m.checkTimeouts()
	got, _ := store.Get("p-to")
	if got.GetState() != TimedOut || got.EndTime == nil {
		t.Fatalf("a timed-out session must be written back: state=%v end=%v", got.GetState(), got.EndTime)
	}
}

func TestCheckTimeouts_MemoryStoreUnchanged(t *testing.T) {
	store := NewMemoryStore()
	m := NewManager(store, time.Millisecond)
	sess := m.GetOrCreate("p-to-mem", "http://b", "127.0.0.1")
	time.Sleep(10 * time.Millisecond)
	m.checkTimeouts()
	if sess.GetState() != TimedOut {
		t.Fatalf("state = %v", sess.GetState())
	}
}

// staleListStore hands checkTimeouts a List snapshot that predates a request's
// write-back: after taking the snapshot it persists fresh activity, so only the
// re-read before the timeout write can see it.
type staleListStore struct {
	*CopyStore
	beforeReturn func()
}

func (s *staleListStore) List(filter func(*Session) bool) []*Session {
	out := s.CopyStore.List(filter)
	if s.beforeReturn != nil {
		s.beforeReturn()
	}
	return out
}

func TestCheckTimeouts_ReReadGuardKeepsASessionTouchedAfterTheList(t *testing.T) {
	store := &staleListStore{CopyStore: NewCopyStore()}
	m := NewManager(store, 50*time.Millisecond)
	m.GetOrCreate("p-fresh", "http://b", "127.0.0.1")
	time.Sleep(80 * time.Millisecond) // idle past the timeout in the snapshot

	store.beforeReturn = func() {
		store.beforeReturn = nil
		cur, _ := store.Get("p-fresh")
		cur.Touch() // a request lands right after the snapshot was taken
		store.Put(cur)
	}
	m.checkTimeouts()

	got, _ := store.Get("p-fresh")
	if got.GetState() != Active || got.EndTime != nil {
		t.Fatalf("a session touched after the List snapshot was timed out from stale data: state=%v end=%v", got.GetState(), got.EndTime)
	}
}

func TestCheckTimeouts_ReReadGuardKeepsASessionKilledAfterTheList(t *testing.T) {
	store := &staleListStore{CopyStore: NewCopyStore()}
	m := NewManager(store, 50*time.Millisecond)
	m.GetOrCreate("p-killed", "http://b", "127.0.0.1")
	time.Sleep(80 * time.Millisecond)

	store.beforeReturn = func() {
		store.beforeReturn = nil
		m.Kill("p-killed")
	}
	m.checkTimeouts()

	got, _ := store.Get("p-killed")
	if got.GetState() != Killed {
		t.Fatalf("a kill landing after the List snapshot was overwritten with TimedOut: state=%v", got.GetState())
	}
}

func TestTrackRequest_PersistsInflightSessionsUntilTheyEnd(t *testing.T) {
	store := NewCopyStore()
	m := NewManager(store, time.Minute)
	m.SetPersistInterval(time.Hour) // enabled; the test drives persistInflight itself
	sess := m.GetOrCreate("t-1", "http://b", "127.0.0.1")
	end := m.TrackRequest(sess)

	sess.AddTokens(4, 5) // mid-request: not yet written back
	m.persistInflight()
	got, _ := store.Get("t-1")
	if in, out := got.GetTokens(); in != 4 || out != 5 {
		t.Fatalf("an in-flight session must be written by the periodic persist: %d/%d", in, out)
	}

	end() // request done: final write-back and unregistered
	puts := store.Puts()
	m.persistInflight()
	if store.Puts() != puts {
		t.Fatal("an ended request must no longer be persisted periodically")
	}
	end() // idempotent
}

func TestTrackRequest_PeriodicPersistKeepsAConcurrentKill(t *testing.T) {
	store := NewCopyStore()
	m := NewManager(store, time.Minute)
	m.SetPersistInterval(time.Hour)
	sess := m.GetOrCreate("t-kill", "http://b", "127.0.0.1")
	end := m.TrackRequest(sess)
	defer end()
	m.Kill("t-kill")
	sess.AddTokens(1, 1)
	m.persistInflight()
	got, _ := store.Get("t-kill")
	if got.GetState() != Killed {
		t.Fatalf("the periodic persist clobbered a kill: %v", got.GetState())
	}
}

func TestTrackRequest_DisabledRegistersNothing(t *testing.T) {
	store := NewCopyStore()
	m := NewManager(store, time.Minute) // interval 0
	sess := m.GetOrCreate("t-off", "http://b", "127.0.0.1")
	end := m.TrackRequest(sess)
	sess.AddTokens(2, 2)
	puts := store.Puts()
	m.persistInflight()
	if store.Puts() != puts {
		t.Fatal("with no persist interval nothing may be tracked, so the periodic persist writes nothing")
	}
	end() // still writes back once at request end
	got, _ := store.Get("t-off")
	if in, _ := got.GetTokens(); in != 2 {
		t.Fatalf("end must still write back: %d", in)
	}
}

func TestManagerRun_PersistsInflightOnTheTicker(t *testing.T) {
	store := NewCopyStore()
	m := NewManager(store, time.Minute)
	m.SetPersistInterval(10 * time.Millisecond)
	sess := m.GetOrCreate("t-run", "http://b", "127.0.0.1")
	end := m.TrackRequest(sess)
	defer end()
	sess.AddTokens(8, 9)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	deadline := time.After(3 * time.Second)
	for {
		got, _ := store.Get("t-run")
		if in, _ := got.GetTokens(); in == 8 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("the periodic persist never wrote the in-flight session")
		case <-time.After(5 * time.Millisecond):
		}
	}
}
