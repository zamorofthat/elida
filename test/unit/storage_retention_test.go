package unit

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/storage"
)

// retentionStore returns an empty store that is closed when the test ends.
func retentionStore(t *testing.T) *storage.SQLiteStore {
	t.Helper()
	store := newTempIntegrityStore(t)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// recordEventsAt records n events for a session and moves them to the given
// time, the way they would look had they been recorded then.
func recordEventsAt(t *testing.T, store *storage.SQLiteStore, sessionID string, n int, at time.Time) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if err := store.RecordEvent(ctx, storage.EventSessionStarted, sessionID, "info", map[string]int{"n": i}); err != nil {
			t.Fatalf("RecordEvent: %v", err)
		}
	}
	if _, err := store.DB().Exec(
		`UPDATE events SET timestamp = ? WHERE id IN (
			SELECT id FROM events WHERE session_id = ? ORDER BY id DESC LIMIT ?)`,
		at, sessionID, n); err != nil {
		t.Fatalf("backdate events: %v", err)
	}
}

func countRows(t *testing.T, store *storage.SQLiteStore, table string) int {
	t.Helper()
	var n int
	if err := store.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestRetentionSweep_DeletesHistoryPastRetention(t *testing.T) {
	store := retentionStore(t)
	now := time.Now()
	old := now.AddDate(0, 0, -40)
	recent := now.AddDate(0, 0, -2)

	for id, end := range map[string]time.Time{"old-session": old, "recent-session": recent} {
		if err := store.SaveSession(storage.SessionRecord{ID: id, State: "completed", StartTime: end.Add(-time.Minute), EndTime: end}); err != nil {
			t.Fatalf("SaveSession(%s): %v", id, err)
		}
	}
	voice := []storage.VoiceSessionRecord{
		{ID: "old-voice", ParentSessionID: "old-session", State: "ended", StartTime: old.Add(-time.Minute), EndTime: &old},
		{ID: "old-voice-never-ended", ParentSessionID: "old-session", State: "active", StartTime: old},
		{ID: "recent-voice", ParentSessionID: "recent-session", State: "ended", StartTime: recent.Add(-time.Minute), EndTime: &recent},
	}
	for _, v := range voice {
		if err := store.SaveVoiceSession(v); err != nil {
			t.Fatalf("SaveVoiceSession(%s): %v", v.ID, err)
		}
	}
	for id, at := range map[string]time.Time{"old-tts": old, "recent-tts": recent} {
		if err := store.SaveTTSRequest(storage.TTSRequest{ID: id, SessionID: "s", Timestamp: at, Provider: "openai"}); err != nil {
			t.Fatalf("SaveTTSRequest(%s): %v", id, err)
		}
	}

	got, err := store.SweepRetention(context.Background(), storage.RetentionPolicy{HistoryDays: 30})
	if err != nil {
		t.Fatalf("SweepRetention: %v", err)
	}
	want := storage.RetentionResult{Sessions: 1, VoiceSessions: 2, TTSRequests: 1}
	if got != want {
		t.Errorf("result = %+v, want %+v", got, want)
	}

	if rec, _ := store.GetSession("old-session"); rec != nil {
		t.Error("old session survived the sweep")
	}
	if rec, _ := store.GetSession("recent-session"); rec == nil {
		t.Error("recent session was deleted")
	}
	if rec, _ := store.GetVoiceSession("recent-voice"); rec == nil {
		t.Error("recent voice session was deleted")
	}
	if n := countRows(t, store, "voice_sessions"); n != 1 {
		t.Errorf("voice_sessions rows = %d, want 1", n)
	}
	if n := countRows(t, store, "tts_requests"); n != 1 {
		t.Errorf("tts_requests rows = %d, want 1", n)
	}
}

func TestRetentionSweep_ZeroOrNegativeKeepsEverything(t *testing.T) {
	for _, days := range []int{0, -1} {
		t.Run(fmt.Sprintf("days=%d", days), func(t *testing.T) {
			store := retentionStore(t)
			old := time.Now().AddDate(-5, 0, 0)
			if err := store.SaveSession(storage.SessionRecord{ID: "ancient", State: "completed", StartTime: old, EndTime: old}); err != nil {
				t.Fatalf("SaveSession: %v", err)
			}
			recordEventsAt(t, store, "ancient", 3, old)

			got, err := store.SweepRetention(context.Background(), storage.RetentionPolicy{HistoryDays: days, EventDays: days})
			if err != nil {
				t.Fatalf("SweepRetention: %v", err)
			}
			if got != (storage.RetentionResult{}) {
				t.Errorf("result = %+v, want nothing deleted", got)
			}
			if rec, _ := store.GetSession("ancient"); rec == nil {
				t.Error("session deleted although retention is off")
			}
			if n := countRows(t, store, "events"); n != 3 {
				t.Errorf("events rows = %d, want 3", n)
			}
		})
	}
}

// Events and history have separate periods: an event log that must outlive
// the session records (or the reverse) is a supported setup.
func TestRetentionSweep_HistoryAndEventsAreIndependent(t *testing.T) {
	store := retentionStore(t)
	old := time.Now().AddDate(0, 0, -40)
	if err := store.SaveSession(storage.SessionRecord{ID: "s", State: "completed", StartTime: old, EndTime: old}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	recordEventsAt(t, store, "s", 2, old)

	got, err := store.SweepRetention(context.Background(), storage.RetentionPolicy{HistoryDays: 30, EventDays: 90})
	if err != nil {
		t.Fatalf("SweepRetention: %v", err)
	}
	if got.Sessions != 1 || got.Events != 0 {
		t.Errorf("result = %+v, want the session gone and the events kept", got)
	}
	if n := countRows(t, store, "events"); n != 2 {
		t.Errorf("events rows = %d, want 2", n)
	}
}

// A stored integrity root covers every event of its session. Once the sweep
// removes any of them the root can no longer be proven, so it must go too;
// leaving it would make every later proof fail as "possible data corruption".
func TestRetentionSweep_DropsIntegrityRootsItInvalidates(t *testing.T) {
	store := retentionStore(t)
	ctx := context.Background()
	now := time.Now()
	old := now.AddDate(0, 0, -100)
	recent := now.AddDate(0, 0, -2)

	recordEventsAt(t, store, "all-old", 3, old)
	recordEventsAt(t, store, "straddles", 2, old)
	recordEventsAt(t, store, "straddles", 2, recent)
	recordEventsAt(t, store, "all-recent", 3, recent)
	for _, id := range []string{"all-old", "straddles", "all-recent"} {
		if _, err := store.ComputeAndStoreSDRIntegrity(ctx, id); err != nil {
			t.Fatalf("ComputeAndStoreSDRIntegrity(%s): %v", id, err)
		}
	}

	got, err := store.SweepRetention(ctx, storage.RetentionPolicy{EventDays: 90})
	if err != nil {
		t.Fatalf("SweepRetention: %v", err)
	}
	if got.Events != 5 || got.IntegrityRoots != 2 {
		t.Errorf("result = %+v, want 5 events and 2 integrity roots", got)
	}

	for _, id := range []string{"all-old", "straddles"} {
		integrity, getErr := store.GetSDRIntegrity(ctx, id)
		if getErr != nil {
			t.Fatalf("GetSDRIntegrity(%s): %v", id, getErr)
		}
		if integrity != nil {
			t.Errorf("%s: integrity root kept after its events were swept", id)
		}
	}

	// The untouched session still proves.
	events, err := store.GetSessionEvents("all-recent")
	if err != nil || len(events) != 3 {
		t.Fatalf("GetSessionEvents(all-recent) = %d events, err %v; want 3", len(events), err)
	}
	proof, err := store.GetSDRProof(ctx, "all-recent", events[0].ID)
	if err != nil || proof == nil {
		t.Fatalf("GetSDRProof(all-recent) = %v, err %v; want a proof", proof, err)
	}

	// The straddling session keeps its recent events and proves again once
	// its record is finalized over what is left.
	left, err := store.GetSessionEvents("straddles")
	if err != nil || len(left) != 2 {
		t.Fatalf("GetSessionEvents(straddles) = %d events, err %v; want 2", len(left), err)
	}
	if _, err = store.ComputeAndStoreSDRIntegrity(ctx, "straddles"); err != nil {
		t.Fatalf("recompute integrity: %v", err)
	}
	proof, err = store.GetSDRProof(ctx, "straddles", left[0].ID)
	if err != nil || proof == nil {
		t.Fatalf("GetSDRProof(straddles) after recompute = %v, err %v; want a proof", proof, err)
	}
}

// The sweep deletes in bounded batches so that a large backlog (the first
// run on a database that was never swept) does not hold the single SQLite
// connection for one long statement. Every expired row must still go.
func TestRetentionSweep_ClearsABacklogLargerThanOneBatch(t *testing.T) {
	store := retentionStore(t)
	old := time.Now().AddDate(0, 0, -40)
	total := 2*storage.RetentionBatchSize + 7

	var sb strings.Builder
	sb.WriteString("INSERT INTO events (timestamp, event_type, session_id, severity, data) VALUES ")
	args := make([]any, 0, total*2)
	for i := 0; i < total; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(?, 'session_started', ?, 'info', '{}')")
		args = append(args, old, fmt.Sprintf("s-%d", i%5))
	}
	if _, err := store.DB().Exec(sb.String(), args...); err != nil {
		t.Fatalf("seed events: %v", err)
	}
	for i := 0; i < total; i++ {
		rec := storage.SessionRecord{ID: fmt.Sprintf("old-%d", i), State: "completed", StartTime: old, EndTime: old}
		if err := store.SaveSession(rec); err != nil {
			t.Fatalf("SaveSession: %v", err)
		}
	}

	got, err := store.SweepRetention(context.Background(), storage.RetentionPolicy{HistoryDays: 30, EventDays: 30})
	if err != nil {
		t.Fatalf("SweepRetention: %v", err)
	}
	if got.Sessions != int64(total) || got.Events != int64(total) {
		t.Errorf("result = %+v, want %d sessions and %d events", got, total, total)
	}
	if n := countRows(t, store, "sessions"); n != 0 {
		t.Errorf("sessions rows = %d, want 0", n)
	}
	if n := countRows(t, store, "events"); n != 0 {
		t.Errorf("events rows = %d, want 0", n)
	}
}

func TestRetentionSweep_StopsWhenContextIsCanceled(t *testing.T) {
	store := retentionStore(t)
	old := time.Now().AddDate(0, 0, -40)
	if err := store.SaveSession(storage.SessionRecord{ID: "old", State: "completed", StartTime: old, EndTime: old}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.SweepRetention(ctx, storage.RetentionPolicy{HistoryDays: 30}); err == nil {
		t.Fatal("SweepRetention on a canceled context returned no error")
	}
	if rec, _ := store.GetSession("old"); rec == nil {
		t.Error("a canceled sweep deleted a row")
	}
}

// RunRetention is what main starts: it must sweep once right away (a process
// that restarts more often than the interval would otherwise never sweep) and
// return when its context ends.
func TestRunRetention_SweepsAtStartAndStopsOnCancel(t *testing.T) {
	store := retentionStore(t)
	old := time.Now().AddDate(0, 0, -40)
	if err := store.SaveSession(storage.SessionRecord{ID: "old", State: "completed", StartTime: old, EndTime: old}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		store.RunRetention(ctx, storage.RetentionPolicy{HistoryDays: 30}, time.Hour)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		rec, _ := store.GetSession("old")
		if rec == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("the first sweep did not run within 5s of start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunRetention did not return after its context was canceled")
	}
}

func TestRunRetention_SweepsAgainEachInterval(t *testing.T) {
	store := retentionStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go store.RunRetention(ctx, storage.RetentionPolicy{HistoryDays: 30}, 20*time.Millisecond)

	// Saved after the start-up sweep, so only a later tick can remove it.
	time.Sleep(60 * time.Millisecond)
	old := time.Now().AddDate(0, 0, -40)
	if err := store.SaveSession(storage.SessionRecord{ID: "late", State: "completed", StartTime: old, EndTime: old}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		rec, _ := store.GetSession("late")
		if rec == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a row that expired after start was never swept")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStorageConfig_EventRetentionDays(t *testing.T) {
	cases := []struct {
		name            string
		history, events int
		want            int
	}{
		{"unset events follow history", 30, 0, 30},
		{"unset events follow history kept forever", 0, 0, 0},
		{"explicit events period wins when longer", 30, 90, 90},
		{"explicit events period wins when shorter", 365, 7, 7},
		{"negative keeps events forever while history is pruned", 30, -1, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.StorageConfig{RetentionDays: tc.history, Events: config.EventsConfig{RetentionDays: tc.events}}
			if got := cfg.EventRetentionDays(); got != tc.want {
				t.Errorf("EventRetentionDays() = %d, want %d", got, tc.want)
			}
		})
	}
}
