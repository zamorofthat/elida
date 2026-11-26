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
// ended session on its next pass (a killed one too: its kill block is only a
// millisecond), and the recorder wired as its end callback.
func endManager(store Store) (*Manager, *endRecorder) {
	m := NewManagerWithKillBlock(store, 50*time.Millisecond, KillBlockConfig{Mode: KillBlockDuration, Duration: time.Millisecond})
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

// bothStores runs fn against the memory store and a copying store.
func bothStores(t *testing.T, fn func(t *testing.T, store Store)) {
	t.Helper()
	for name, store := range map[string]Store{"memory": NewMemoryStore(), "copying": NewCopyStore()} {
		t.Run(name, func(t *testing.T) { fn(t, store) })
	}
}

func TestCleanup_DoesNotReportAKilledSessionAgain(t *testing.T) {
	for name, end := range map[string]func(*Manager, string) bool{
		"kill":      (*Manager).Kill,
		"terminate": (*Manager).Terminate,
	} {
		t.Run(name, func(t *testing.T) {
			bothStores(t, func(t *testing.T, store Store) {
				m, rec := endManager(store)
				m.GetOrCreate("e-kill", "http://b", "127.0.0.1")
				if !end(m, "e-kill") {
					t.Fatal("fixture: the session was not ended")
				}
				if n := len(rec.ends("e-kill")); n != 1 {
					t.Fatalf("end callbacks at %s = %d, want 1", name, n)
				}
				time.Sleep(5 * time.Millisecond) // past the retention period

				m.cleanup()

				if n := len(rec.ends("e-kill")); n != 1 {
					t.Fatalf("end callbacks after cleanup = %d, want 1 (a second report replaces the saved record with one that has lost its captured content)", n)
				}
				if _, ok := store.Get("e-kill"); ok {
					t.Fatal("cleanup must still remove the session")
				}
				if len(m.ends) != 0 {
					t.Fatalf("end marks kept after cleanup: %v", m.ends)
				}
			})
		})
	}
}

func TestTerminate_AfterKillDoesNotReportAgain(t *testing.T) {
	bothStores(t, func(t *testing.T, store Store) {
		m, rec := endManager(store)
		m.retentionPeriod = 50 * time.Millisecond
		m.cleanupInterval = time.Millisecond
		m.GetOrCreate("e-upgrade", "http://b", "127.0.0.1")
		m.Kill("e-upgrade")
		time.Sleep(60 * time.Millisecond)

		if !m.Terminate("e-upgrade") {
			t.Fatal("a killed session must be upgradable to terminated")
		}

		sess, _ := m.Get("e-upgrade")
		if !sess.IsTerminated() {
			t.Fatal("the session must be terminated")
		}
		if n := len(rec.ends("e-upgrade")); n != 1 {
			t.Fatalf("end callbacks after kill then terminate = %d, want 1", n)
		}
		// Terminating restarts the retention period. The session outlives
		// the pass below and must still not be reported by the one after.
		m.cleanup()
		if _, ok := store.Get("e-upgrade"); !ok {
			t.Fatal("fixture: the session must outlive the first cleanup pass")
		}
		time.Sleep(60 * time.Millisecond)
		m.cleanup()
		if n := len(rec.ends("e-upgrade")); n != 1 {
			t.Fatalf("end callbacks after cleanup = %d, want 1", n)
		}
	})
}

func TestCheckTimeouts_AutoTerminatingAKilledSessionDoesNotReportAgain(t *testing.T) {
	bothStores(t, func(t *testing.T, store Store) {
		m, rec := endManager(store)
		m.retentionPeriod = time.Hour
		m.SetKillResumeTimeout(10 * time.Millisecond)
		m.GetOrCreate("e-auto", "http://b", "127.0.0.1")
		m.Kill("e-auto")
		time.Sleep(20 * time.Millisecond)

		m.checkTimeouts()

		sess, _ := m.Get("e-auto")
		if !sess.IsTerminated() {
			t.Fatal("fixture: the killed session must be auto-terminated")
		}
		m.retentionPeriod = time.Millisecond
		time.Sleep(5 * time.Millisecond)
		m.cleanup()
		if n := len(rec.ends("e-auto")); n != 1 {
			t.Fatalf("end callbacks = %d, want 1 (the kill's)", n)
		}
	})
}

func TestResume_AResumedSessionIsReportedAgainWhenItEnds(t *testing.T) {
	bothStores(t, func(t *testing.T, store Store) {
		m, rec := endManager(store)
		m.GetOrCreate("e-resume", "http://b", "127.0.0.1")
		m.Kill("e-resume")
		if !m.Resume("e-resume") {
			t.Fatal("fixture: the killed session must resume")
		}
		idlePastTimeout()

		m.checkTimeouts()

		got := rec.ends("e-resume")
		if len(got) != 2 || got[0].State != Killed || got[1].State != TimedOut {
			t.Fatalf("a resumed session ends a second time and that end must be reported too: %+v", got)
		}
		time.Sleep(5 * time.Millisecond)
		m.cleanup()
		if n := len(rec.ends("e-resume")); n != 2 {
			t.Fatalf("end callbacks after cleanup = %d, want 2", n)
		}
	})
}

// A cleanup pass can take a long time: its callbacks save and export. A
// session reported shortly before the pass must not lose its mark to the
// pass's stale-mark sweep while it is still in the store.
func TestCleanup_ASlowPassDoesNotDropTheMarkOfASessionStillInTheStore(t *testing.T) {
	store := NewMemoryStore()
	m, rec := endManager(store)
	m.retentionPeriod = 50 * time.Millisecond
	m.cleanupInterval = time.Millisecond
	m.SetSessionEndCallback(func(s *Session) {
		if s.ID == "e-slow" {
			time.Sleep(100 * time.Millisecond)
		}
		rec.record(s)
	})
	m.GetOrCreate("e-fast", "http://b", "127.0.0.1")
	idlePastTimeout()
	m.checkTimeouts() // e-fast reported here
	slow := NewSession("e-slow", "http://b", "127.0.0.1")
	slow.SetState(TimedOut)
	longAgo := time.Now().Add(-time.Hour)
	slow.EndTime = &longAgo
	store.Put(slow)

	time.Sleep(30 * time.Millisecond)
	m.cleanup() // reports e-slow; e-fast is not due yet
	time.Sleep(60 * time.Millisecond)
	m.cleanup() // e-fast is due

	if n := len(rec.ends("e-slow")); n != 1 {
		t.Fatalf("fixture: e-slow end callbacks = %d, want 1", n)
	}
	if n := len(rec.ends("e-fast")); n != 1 {
		t.Fatalf("end callbacks for the session reported at its timeout = %d, want 1", n)
	}
}

// killBlockManager is endManager with a kill-block setting. Retention is one
// millisecond, so any retention beyond it is the kill block's doing.
func killBlockManager(store Store, kb KillBlockConfig) (*Manager, *endRecorder) {
	m := NewManagerWithKillBlock(store, time.Hour, kb)
	m.retentionPeriod = time.Millisecond
	m.cleanupInterval = time.Millisecond
	rec := &endRecorder{}
	m.SetSessionEndCallback(rec.record)
	return m, rec
}

// A kill lasts as long as kill_block says. Cleanup used to delete every
// ended session once its retention period (5 minutes) was over, killed ones
// included, so the next request got a fresh session: "permanent" lasted five
// minutes and a longer duration was cut to the same five.
func TestCleanup_KeepsAKilledSessionWhileTheKillBlockHolds(t *testing.T) {
	for name, kb := range map[string]KillBlockConfig{
		"permanent":         {Mode: KillBlockPermanent},
		"long duration":     {Mode: KillBlockDuration, Duration: time.Hour},
		"until hour change": {Mode: KillBlockUntilHourChange},
	} {
		t.Run(name, func(t *testing.T) {
			bothStores(t, func(t *testing.T, store Store) {
				m, _ := killBlockManager(store, kb)
				m.GetOrCreate("kb-id", "http://b", "127.0.0.1")
				m.GetOrCreateByClient("10.1.1.1:5000", "local", "http://b")
				clientID := m.generateClientSessionID("10.1.1.1", "local")
				for _, id := range []string{"kb-id", clientID} {
					if !m.Kill(id) {
						t.Fatalf("Kill(%s) = false", id)
					}
				}
				time.Sleep(20 * time.Millisecond) // well past retention
				m.cleanup()

				for _, id := range []string{"kb-id", clientID} {
					if _, ok := store.Get(id); !ok {
						t.Errorf("%s was cleaned up while the kill block still holds", id)
					}
				}
				if s := m.GetOrCreate("kb-id", "http://b", "127.0.0.1"); s != nil {
					t.Error("a request for the killed session ID was accepted")
				}
				if s := m.GetOrCreateByClient("10.1.1.1:5000", "local", "http://b"); s != nil {
					t.Error("a request from the killed client was accepted")
				}
			})
		})
	}
}

// Once the kill block is over, the killed session is retained no longer.
func TestCleanup_RemovesAKilledSessionWhenTheKillBlockEnds(t *testing.T) {
	bothStores(t, func(t *testing.T, store Store) {
		m, _ := killBlockManager(store, KillBlockConfig{Mode: KillBlockDuration, Duration: 60 * time.Millisecond})
		m.GetOrCreate("kb-end", "http://b", "127.0.0.1")
		m.Kill("kb-end")

		time.Sleep(20 * time.Millisecond)
		m.cleanup()
		if _, ok := store.Get("kb-end"); !ok {
			t.Fatal("removed before the 60 ms block was over")
		}

		time.Sleep(60 * time.Millisecond)
		m.cleanup()
		if _, ok := store.Get("kb-end"); ok {
			t.Fatal("still stored after the kill block ended")
		}
	})
}

// Keeping the session must not cost its end report: the mark that stops
// cleanup reporting a killed session a second time has to outlast the
// session, however long that is.
func TestCleanup_KeepsTheEndMarkOfAKilledSessionItRetains(t *testing.T) {
	bothStores(t, func(t *testing.T, store Store) {
		m, rec := killBlockManager(store, KillBlockConfig{Mode: KillBlockDuration, Duration: 80 * time.Millisecond})
		m.GetOrCreate("kb-mark", "http://b", "127.0.0.1")
		m.Kill("kb-mark")

		time.Sleep(30 * time.Millisecond) // past retention + 2 cleanup intervals
		m.cleanup()
		time.Sleep(70 * time.Millisecond) // block over
		m.cleanup()

		if _, ok := store.Get("kb-mark"); ok {
			t.Fatal("fixture: session should be gone")
		}
		if n := len(rec.ends("kb-mark")); n != 1 {
			t.Fatalf("end callbacks = %d, want 1: dropping the mark while the session was retained reported it again", n)
		}
	})
}

// Under until_hour_change the block ends with the hour of the kill: a client
// session ID carries the hour, so the next hour's requests use a new one.
func TestKillBlockRetains_UntilHourChangeEndsWithTheHourOfTheKill(t *testing.T) {
	m := NewManagerWithKillBlock(NewMemoryStore(), time.Hour, KillBlockConfig{Mode: KillBlockUntilHourChange})
	killedAt := func(at time.Time) *Session {
		s := NewSession("hc", "http://b", "127.0.0.1")
		s.Kill()
		s.mu.Lock()
		s.EndTime = &at
		s.mu.Unlock()
		return s
	}
	if !m.killBlockRetains(killedAt(time.Now())) {
		t.Error("a session killed just now is not retained")
	}
	if m.killBlockRetains(killedAt(time.Now().Add(-2 * time.Hour))) {
		t.Error("a session killed two hours ago is still retained")
	}
	if m.killBlockRetains(NewSession("active", "http://b", "127.0.0.1")) {
		t.Error("a session that was not killed is retained")
	}
}
