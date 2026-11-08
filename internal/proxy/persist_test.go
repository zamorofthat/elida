package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"elida/internal/config"
	"elida/internal/session"
)

func newCopyStoreProxy(t *testing.T, backendURL string) (*Proxy, *session.Manager, *session.CopyStore) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Backend = backendURL
	store := session.NewCopyStore()
	manager := session.NewManager(store, cfg.Session.Timeout)
	p, err := New(cfg, store, manager)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p, manager, store
}

func sendPersistRequest(t *testing.T, p *Proxy, id string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Session-ID", id)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
}

// Under a copying store, request-scoped accounting survives only if the
// proxy writes the session back: one write-back per completed request.
func TestServeHTTP_WritesRequestStateBackUnderACopyingStore(t *testing.T) {
	var hits int64
	backend := e2eBackend(&hits) // answers with usage prompt=1 completion=1
	defer backend.Close()
	p, _, store := newCopyStoreProxy(t, backend.URL)

	sendPersistRequest(t, p, "persist-1")
	sendPersistRequest(t, p, "persist-1")

	got, ok := store.Get("persist-1")
	if !ok {
		t.Fatal("session not stored")
	}
	if in, out := got.GetTokens(); in != 2 || out != 2 {
		t.Fatalf("tokens = %d/%d, want 2/2 accumulated across requests", in, out)
	}
	if got.RequestCount != 2 {
		t.Fatalf("request count = %d, want 2", got.RequestCount)
	}
	// One write for the create, one write-back per request.
	if puts := store.Puts(); puts != 3 {
		t.Fatalf("store writes = %d, want 3 (1 create + 2 request-end write-backs)", puts)
	}
	if atomic.LoadInt64(&hits) != 2 {
		t.Fatalf("backend hits = %d", hits)
	}
}

// A policy terminate runs mid-request, and Manager.Terminate exports the
// end-of-session record from a fresh store read. The request's own state
// must be written first or the terminated session's record loses it.
func TestTerminateSession_RecordIncludesTheRequestsState(t *testing.T) {
	p, manager, store := newCopyStoreProxy(t, "http://127.0.0.1:1")
	var gotIn, gotOut int64
	var gotShadows int
	var gotTerminated bool
	manager.SetSessionEndCallback(func(s *session.Session) {
		gotIn, gotOut = s.GetTokens()
		gotShadows = len(s.GetSemanticShadow())
		gotTerminated = s.IsTerminated()
	})

	sess := manager.GetOrCreate("term-1", "http://b", "127.0.0.1")
	sess.AddTokens(5, 6)
	sess.RecordSemanticShadow(session.SemanticShadow{DecisionID: "d1"})
	p.terminateSession(sess)

	if gotIn != 5 || gotOut != 6 || gotShadows != 1 || !gotTerminated {
		t.Fatalf("end-of-session record saw tokens %d/%d, %d shadows, terminated=%v", gotIn, gotOut, gotShadows, gotTerminated)
	}
	stored, _ := store.Get("term-1")
	if !stored.IsTerminated() || stored.GetState() != session.Killed {
		t.Fatalf("stored state = %v terminated=%v", stored.GetState(), stored.IsTerminated())
	}
}
