package session

import (
	"testing"
	"time"
)

// A kill can carry its own block, which replaces the configured kill_block
// for that session only.

func TestKillWithBlock_ReplacesTheConfiguredMode(t *testing.T) {
	bothStores(t, func(t *testing.T, store Store) {
		// Configured: a one-millisecond block. This kill asks for permanent.
		m, _ := killBlockManager(store, KillBlockConfig{Mode: KillBlockDuration, Duration: time.Millisecond})
		m.GetOrCreate("ov-perm", "http://b", "127.0.0.1")
		if !m.KillWithBlock("ov-perm", &KillBlockConfig{Mode: KillBlockPermanent}) {
			t.Fatal("KillWithBlock = false")
		}

		time.Sleep(20 * time.Millisecond)
		m.cleanup()

		if _, ok := store.Get("ov-perm"); !ok {
			t.Fatal("a permanent kill was cleaned up")
		}
		if s := m.GetOrCreate("ov-perm", "http://b", "127.0.0.1"); s != nil {
			t.Fatal("a request for a permanently killed session was accepted")
		}
	})
}

func TestKillWithBlock_ASetDurationEndsBeforeAPermanentConfiguration(t *testing.T) {
	bothStores(t, func(t *testing.T, store Store) {
		// Configured: permanent. This kill asks for 60 ms.
		m, _ := killBlockManager(store, KillBlockConfig{Mode: KillBlockPermanent})
		m.GetOrCreateByClient("10.2.2.2:1", "local", "http://b")
		id := m.generateClientSessionID("10.2.2.2", "local")
		m.KillWithBlock(id, &KillBlockConfig{Mode: KillBlockDuration, Duration: 60 * time.Millisecond})

		if s := m.GetOrCreateByClient("10.2.2.2:1", "local", "http://b"); s != nil {
			t.Fatal("blocked client was accepted inside its 60 ms block")
		}
		time.Sleep(80 * time.Millisecond)
		if s := m.GetOrCreateByClient("10.2.2.2:1", "local", "http://b"); s == nil {
			t.Fatal("client is still blocked after its 60 ms block ended")
		}
	})
}

func TestKill_WithoutABlockUsesTheConfiguredMode(t *testing.T) {
	bothStores(t, func(t *testing.T, store Store) {
		m, _ := killBlockManager(store, KillBlockConfig{Mode: KillBlockPermanent})
		m.GetOrCreate("ov-default", "http://b", "127.0.0.1")
		m.Kill("ov-default")
		time.Sleep(20 * time.Millisecond)
		m.cleanup()
		if _, ok := store.Get("ov-default"); !ok {
			t.Fatal("a plain kill under permanent was cleaned up")
		}
	})
}

func TestResume_ForgetsTheBlockOfTheEarlierKill(t *testing.T) {
	bothStores(t, func(t *testing.T, store Store) {
		m, _ := killBlockManager(store, KillBlockConfig{Mode: KillBlockDuration, Duration: time.Millisecond})
		m.GetOrCreate("ov-resume", "http://b", "127.0.0.1")
		m.KillWithBlock("ov-resume", &KillBlockConfig{Mode: KillBlockPermanent})
		if !m.Resume("ov-resume") {
			t.Fatal("Resume = false")
		}
		m.Kill("ov-resume") // no block of its own: the configured 1 ms

		time.Sleep(20 * time.Millisecond)
		m.cleanup()
		if _, ok := store.Get("ov-resume"); ok {
			t.Fatal("the permanent block of the first kill outlived the resume")
		}
	})
}

func TestTerminate_KeepsTheBlockOfTheKill(t *testing.T) {
	bothStores(t, func(t *testing.T, store Store) {
		m, _ := killBlockManager(store, KillBlockConfig{Mode: KillBlockDuration, Duration: time.Millisecond})
		m.GetOrCreate("ov-term", "http://b", "127.0.0.1")
		m.KillWithBlock("ov-term", &KillBlockConfig{Mode: KillBlockPermanent})
		m.Terminate("ov-term")

		time.Sleep(20 * time.Millisecond)
		m.cleanup()
		if _, ok := store.Get("ov-term"); !ok {
			t.Fatal("terminating a permanently killed session dropped its block")
		}
	})
}

// The block is lifecycle state: it has to survive a trip through the wire
// form that Redis uses, and Persist must not lose it.
func TestKillBlock_SurvivesTheWireForm(t *testing.T) {
	s := NewSession("wire", "http://b", "127.0.0.1")
	s.killWith(&KillBlockConfig{Mode: KillBlockDuration, Duration: 90 * time.Second})

	back := newSessionFromData(sessionToData(s), make(chan struct{}))
	l := back.lifecycleState()
	if l.KillBlockMode != KillBlockDuration || l.KillBlockDuration != 90*time.Second {
		t.Fatalf("after the wire form: mode=%q duration=%v", l.KillBlockMode, l.KillBlockDuration)
	}
}

func TestPersist_KeepsTheStoredKillBlock(t *testing.T) {
	store := NewCopyStore()
	m, _ := killBlockManager(store, KillBlockConfig{Mode: KillBlockDuration, Duration: time.Millisecond})
	live := m.GetOrCreate("ov-persist", "http://b", "127.0.0.1")
	m.KillWithBlock("ov-persist", &KillBlockConfig{Mode: KillBlockPermanent})

	m.Persist(live) // the request goroutine's end, with its older copy

	cur, _ := store.Get("ov-persist")
	if l := cur.lifecycleState(); l.KillBlockMode != KillBlockPermanent {
		t.Fatalf("stored mode after Persist = %q, want permanent", l.KillBlockMode)
	}
}

func TestParseKillBlock(t *testing.T) {
	for in, want := range map[string]struct {
		set bool
		cfg KillBlockConfig
		err bool
	}{
		"":                  {set: false},
		"default":           {set: false},
		"permanent":         {set: true, cfg: KillBlockConfig{Mode: KillBlockPermanent}},
		"until_hour_change": {set: true, cfg: KillBlockConfig{Mode: KillBlockUntilHourChange}},
		"90s":               {set: true, cfg: KillBlockConfig{Mode: KillBlockDuration, Duration: 90 * time.Second}},
		"2h":                {set: true, cfg: KillBlockConfig{Mode: KillBlockDuration, Duration: 2 * time.Hour}},
		"0s":                {err: true},
		"-5m":               {err: true},
		"forever":           {err: true},
		"10":                {err: true},
	} {
		cfg, set, err := ParseKillBlock(in)
		if (err != nil) != want.err || set != want.set || (set && cfg != want.cfg) {
			t.Errorf("ParseKillBlock(%q) = %+v, %v, %v; want %+v", in, cfg, set, err, want)
		}
	}
}
