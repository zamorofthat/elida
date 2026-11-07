package main

import (
	"net/http"
	"strings"
	"testing"

	"elida/internal/session"
)

// The end-of-session record built from a copying (Redis-like) store must be
// as complete as the one built from the memory store: this is the bug the
// Redis parity work fixes. The copy-store arm's correlation continuity (the
// slow-burn match spans three requests) rests on runner.sameInstance matching
// across a decode: each request's freshly decoded copy must count as the same
// session instance, which needs StartTime to survive the blob at nanosecond
// precision (pinned by TestSessionWire_TimesKeepNanosecondFidelity). A
// copyRule mismatch here points at that first.
func TestRedisParity_SessionEndRecordMatchesTheMemoryStoreRecord(t *testing.T) {
	run := func(copyStore bool) (*app, int, int, string) {
		a, send := shadowStorageAppWithStore(t, copyStore)
		for _, body := range slowBurnBodies() {
			if code := send(body); code != http.StatusOK {
				t.Fatalf("status = %d", code)
			}
		}
		a.manager.DrainActiveSessions()
		rec, err := a.sqliteStore.GetSession("sess-cs-e2e")
		if err != nil || rec == nil {
			t.Fatalf("GetSession: %v %v", rec, err)
		}
		rule := ""
		if len(rec.CorrelationShadow) > 0 {
			rule = rec.CorrelationShadow[0].Rule
		}
		return a, len(rec.SemanticShadow), rec.RequestCount, rule
	}

	_, memShadows, memRequests, memRule := run(false)
	a, copyShadows, copyRequests, copyRule := run(true)

	if memShadows == 0 || memRule != "slow_burn_injection" {
		t.Fatalf("fixture: memory-store run recorded %d shadows, rule %q", memShadows, memRule)
	}
	if copyShadows != memShadows || copyRule != memRule || copyRequests != memRequests {
		t.Fatalf("copy-store record: %d shadows, rule %q, %d requests; memory-store record: %d, %q, %d",
			copyShadows, copyRule, copyRequests, memShadows, memRule, memRequests)
	}

	// Nothing in the stored (Redis-bound) blob carries request content.
	cs, ok := a.store.(*session.CopyStore)
	if !ok {
		t.Fatalf("store = %T", a.store)
	}
	if blob, found := cs.Blob("sess-cs-e2e"); found && strings.Contains(string(blob), slowBurnMarker) {
		t.Fatalf("request content reached the session blob: %s", blob)
	}
}
