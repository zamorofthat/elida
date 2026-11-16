package session

import (
	"sync"
	"testing"
	"time"
)

// endSeen is what the session-end callback was shown for one session.
type endSeen struct {
	ID        string
	StartTime time.Time
	State     State
	EndTime   *time.Time
	BytesOut  int64
}

// endRecorder is a session-end callback that keeps what it was shown.
type endRecorder struct {
	mu   sync.Mutex
	seen []endSeen
}

func (r *endRecorder) record(s *Session) {
	snap := s.Snapshot()
	r.mu.Lock()
	r.seen = append(r.seen, endSeen{
		ID: snap.ID, StartTime: snap.StartTime, State: snap.State,
		EndTime: snap.EndTime, BytesOut: snap.BytesOut,
	})
	r.mu.Unlock()
}

func (r *endRecorder) ends(id string) []endSeen {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []endSeen
	for _, e := range r.seen {
		if e.ID == id {
			out = append(out, e)
		}
	}
	return out
}

// endManager is a manager with a short idle timeout whose cleanup removes an
// ended session on its next pass, and the recorder wired as its end callback.
func endManager(store Store) (*Manager, *endRecorder) {
	m := NewManager(store, 50*time.Millisecond)
	m.retentionPeriod = time.Millisecond
	rec := &endRecorder{}
	m.SetSessionEndCallback(rec.record)
	return m, rec
}

func idlePastTimeout() { time.Sleep(80 * time.Millisecond) }

func TestCheckTimeouts_ReportsTheEndWhenTheSessionTimesOut(t *testing.T) {
	for name, store := range map[string]Store{"memory": NewMemoryStore(), "copying": NewCopyStore()} {
		t.Run(name, func(t *testing.T) {
			m, rec := endManager(store)
			m.GetOrCreate("e-to", "http://b", "127.0.0.1")
			idlePastTimeout()

			m.checkTimeouts()

			got := rec.ends("e-to")
			if len(got) != 1 {
				t.Fatalf("end callbacks at timeout = %d, want 1 (the session must be saved when it times out, not when cleanup removes it)", len(got))
			}
			if got[0].State != TimedOut || got[0].EndTime == nil {
				t.Fatalf("the callback must see the timed-out session: state=%v end=%v", got[0].State, got[0].EndTime)
			}
		})
	}
}

func TestCleanup_DoesNotReportATimedOutSessionAgain(t *testing.T) {
	for name, store := range map[string]Store{"memory": NewMemoryStore(), "copying": NewCopyStore()} {
		t.Run(name, func(t *testing.T) {
			m, rec := endManager(store)
			m.GetOrCreate("e-once", "http://b", "127.0.0.1")
			idlePastTimeout()
			m.checkTimeouts()
			time.Sleep(5 * time.Millisecond) // past the retention period

			m.cleanup()

			if n := len(rec.ends("e-once")); n != 1 {
				t.Fatalf("end callbacks after cleanup = %d, want 1", n)
			}
			if _, ok := store.Get("e-once"); ok {
				t.Fatal("cleanup must still remove the session")
			}
			if len(m.ends) != 0 {
				t.Fatalf("cleanup must forget the removed session's end mark: %v", m.ends)
			}
		})
	}
}

func TestDrain_DoesNotReportATimedOutSessionAgain(t *testing.T) {
	m, rec := endManager(NewMemoryStore())
	m.GetOrCreate("e-drain", "http://b", "127.0.0.1")
	idlePastTimeout()
	m.checkTimeouts()

	if n := m.DrainActiveSessions(); n != 0 {
		t.Fatalf("drained = %d, want 0: the only session was already saved at its timeout", n)
	}
	if n := len(rec.ends("e-drain")); n != 1 {
		t.Fatalf("end callbacks after drain = %d, want 1", n)
	}
}

// A request still running when its session times out has more to add (a
// long stream, an open WebSocket). The end is reported when that request
// finishes, with what it added.
func TestCheckTimeouts_WaitsForARequestInFlightBeforeReporting(t *testing.T) {
	m, rec := endManager(NewMemoryStore())
	sess := m.GetOrCreate("e-inflight", "http://b", "127.0.0.1")
	end := m.TrackRequest(sess)
	idlePastTimeout()

	m.checkTimeouts()
	if sess.GetState() != TimedOut {
		t.Fatalf("the timeout decision itself must not change under the memory store: %v", sess.GetState())
	}
	if n := len(rec.ends("e-inflight")); n != 0 {
		t.Fatalf("end callbacks while the request is in flight = %d, want 0", n)
	}

	sess.AddBytes(0, 4096) // the response lands after the timeout
	end()

	got := rec.ends("e-inflight")
	if len(got) != 1 {
		t.Fatalf("end callbacks after the request ended = %d, want 1", len(got))
	}
	if got[0].BytesOut != 4096 || got[0].State != TimedOut {
		t.Fatalf("the record must carry what the request added: bytes_out=%d state=%v", got[0].BytesOut, got[0].State)
	}

	time.Sleep(5 * time.Millisecond)
	m.cleanup()
	if n := len(rec.ends("e-inflight")); n != 1 {
		t.Fatalf("end callbacks after cleanup = %d, want 1", n)
	}
}

func TestCheckTimeouts_WaitsForTheLastOfSeveralRequests(t *testing.T) {
	m, rec := endManager(NewMemoryStore())
	sess := m.GetOrCreate("e-two", "http://b", "127.0.0.1")
	endA, endB := m.TrackRequest(sess), m.TrackRequest(sess)
	idlePastTimeout()
	m.checkTimeouts()

	endA()
	if n := len(rec.ends("e-two")); n != 0 {
		t.Fatalf("end callbacks with one request still in flight = %d, want 0", n)
	}
	endB()
	endB()
	if n := len(rec.ends("e-two")); n != 1 {
		t.Fatalf("end callbacks after the last request ended = %d, want 1", n)
	}
}

// Cleanup stays the backstop: a request that outlives the retention period
// does not hold the record back any longer than it did before.
func TestCleanup_ReportsAnEndStillWaitingOnARequest(t *testing.T) {
	m, rec := endManager(NewMemoryStore())
	sess := m.GetOrCreate("e-stuck", "http://b", "127.0.0.1")
	end := m.TrackRequest(sess)
	idlePastTimeout()
	m.checkTimeouts()
	time.Sleep(5 * time.Millisecond)

	m.cleanup()
	if n := len(rec.ends("e-stuck")); n != 1 {
		t.Fatalf("end callbacks after cleanup = %d, want 1", n)
	}
	end()
	if n := len(rec.ends("e-stuck")); n != 1 {
		t.Fatalf("end callbacks after the late request ended = %d, want 1", n)
	}
}

func TestDrain_ReportsAnEndStillWaitingOnARequest(t *testing.T) {
	m, rec := endManager(NewMemoryStore())
	sess := m.GetOrCreate("e-drain-wait", "http://b", "127.0.0.1")
	end := m.TrackRequest(sess)
	idlePastTimeout()
	m.checkTimeouts()

	if n := m.DrainActiveSessions(); n != 1 {
		t.Fatalf("drained = %d, want 1", n)
	}
	end()
	if n := len(rec.ends("e-drain-wait")); n != 1 {
		t.Fatalf("end callbacks = %d, want 1", n)
	}
}

// A timed-out session's ID can be used again before its end was reported
// (its request is still running). The earlier session is reported before the
// new one takes its place; it used to be dropped without a record.
func TestGetOrCreate_ReportsAnUnreportedSessionBeforeReusingItsID(t *testing.T) {
	acquire := map[string]func(m *Manager) *Session{
		"by id":     func(m *Manager) *Session { return m.GetOrCreate("e-reuse", "http://b", "127.0.0.1:1") },
		"by client": func(m *Manager) *Session { return m.GetOrCreateByClient("127.0.0.1:1", "b", "http://b") },
	}
	for name, get := range acquire {
		t.Run(name, func(t *testing.T) {
			m, rec := endManager(NewMemoryStore())
			first := get(m)
			end := m.TrackRequest(first)
			idlePastTimeout()
			m.checkTimeouts()

			second := get(m)
			if second == first || !second.IsActive() {
				t.Fatalf("want a new active session under the reused ID: same=%v state=%v", second == first, second.GetState())
			}
			got := rec.ends(first.ID)
			if len(got) != 1 || !got[0].StartTime.Equal(first.StartTime) || got[0].State != TimedOut {
				t.Fatalf("the earlier session must be reported before its ID is reused: %d callbacks", len(got))
			}

			end() // the earlier session's request ends after the reuse
			if n := len(rec.ends(first.ID)); n != 1 {
				t.Fatalf("end callbacks after the earlier request ended = %d, want 1", n)
			}

			idlePastTimeout()
			m.checkTimeouts()
			got = rec.ends(first.ID)
			if len(got) != 2 || !got[1].StartTime.Equal(second.StartTime) {
				t.Fatalf("the new session must be reported when it ends in turn: %d callbacks", len(got))
			}
		})
	}
}

// Acquiring a session for a request is activity: the timeout pass must not
// end a session a request has just picked up but not yet registered.
func TestGetOrCreate_AcquiringASessionCountsAsActivity(t *testing.T) {
	m, rec := endManager(NewMemoryStore())
	m.GetOrCreate("e-acquire", "http://b", "127.0.0.1")
	idlePastTimeout()

	sess := m.GetOrCreate("e-acquire", "http://b", "127.0.0.1")
	m.checkTimeouts()

	if sess.GetState() != Active || len(rec.ends("e-acquire")) != 0 {
		t.Fatalf("a session just acquired by a request was timed out: state=%v callbacks=%d", sess.GetState(), len(rec.ends("e-acquire")))
	}
}

func TestSession_TimeOutIfIdle(t *testing.T) {
	s := NewSession("e-atomic", "http://b", "127.0.0.1")
	if s.timeOutIfIdle(time.Hour) || s.GetState() != Active {
		t.Fatalf("a session inside its timeout must stay active: %v", s.GetState())
	}
	time.Sleep(5 * time.Millisecond)
	if !s.timeOutIfIdle(time.Millisecond) || s.GetState() != TimedOut || s.EndTime == nil {
		t.Fatalf("an idle session must time out: state=%v end=%v", s.GetState(), s.EndTime)
	}
	if s.timeOutIfIdle(time.Millisecond) {
		t.Fatal("a session that already ended must not time out again")
	}
	if s.touchIfActive() {
		t.Fatal("an ended session must not be acquired as active")
	}
}

// An end mark whose session left the store by another route (a Redis expiry,
// another replica's cleanup) must not stay in memory for the process lifetime.
func TestCleanup_ForgetsEndMarksOfSessionsThatLeftTheStore(t *testing.T) {
	store := NewCopyStore()
	m, _ := endManager(store)
	m.cleanupInterval = time.Millisecond
	m.GetOrCreate("e-orphan", "http://b", "127.0.0.1")
	idlePastTimeout()
	m.checkTimeouts()
	store.Delete("e-orphan")
	time.Sleep(10 * time.Millisecond)

	m.cleanup()

	if len(m.ends) != 0 {
		t.Fatalf("stale end marks kept: %v", m.ends)
	}
}
