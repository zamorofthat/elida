package unit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"elida/internal/control"
	"elida/internal/session"
	"elida/internal/storage"
)

func TestHandler_HistoryIntegrity(t *testing.T) {
	sqliteStore := newTempIntegrityStore(t)
	defer sqliteStore.Close()

	sessionID := "control-integrity"
	ctx := context.Background()
	if err := sqliteStore.SaveSession(storage.SessionRecord{
		ID:        sessionID,
		State:     "completed",
		StartTime: time.Now().Add(-time.Minute),
		EndTime:   time.Now(),
		Backend:   "anthropic",
	}); err != nil {
		t.Fatal(err)
	}
	if err := sqliteStore.RecordEvent(ctx, storage.EventSessionEnded, sessionID, "", storage.SessionEndedData{State: "completed"}); err != nil {
		t.Fatal(err)
	}
	integrity, err := sqliteStore.ComputeAndStoreSDRIntegrity(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}

	handler := newHistoryHandler(sqliteStore)
	req := httptest.NewRequest("GET", "/control/history/"+sessionID+"/integrity", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp storage.SDRIntegrity
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.RootHash != integrity.RootHash {
		t.Fatalf("expected root %s, got %s", integrity.RootHash, resp.RootHash)
	}
	if resp.EventCount != 1 {
		t.Fatalf("expected event_count 1, got %d", resp.EventCount)
	}
}

func TestHandler_EventProof(t *testing.T) {
	sqliteStore := newTempIntegrityStore(t)
	defer sqliteStore.Close()

	sessionID := "control-proof"
	ctx := context.Background()
	if err := sqliteStore.RecordEvent(ctx, storage.EventSessionEnded, sessionID, "", storage.SessionEndedData{State: "completed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sqliteStore.ComputeAndStoreSDRIntegrity(ctx, sessionID); err != nil {
		t.Fatal(err)
	}

	events, err := sqliteStore.GetSessionEvents(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	handler := newHistoryHandler(sqliteStore)
	req := httptest.NewRequest("GET", "/control/events/"+sessionID+"/"+strconv.FormatInt(events[0].ID, 10)+"/proof", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var proof storage.SDRProof
	if err := json.NewDecoder(w.Result().Body).Decode(&proof); err != nil {
		t.Fatal(err)
	}
	if !storage.VerifySDRProof(proof) {
		t.Fatal("expected API proof to verify")
	}
}

func TestHandler_IntegrityNotFound(t *testing.T) {
	sqliteStore := newTempIntegrityStore(t)
	defer sqliteStore.Close()
	handler := newHistoryHandler(sqliteStore)

	req := httptest.NewRequest("GET", "/control/history/missing/integrity", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing integrity, got %d", w.Code)
	}

	req = httptest.NewRequest("GET", "/control/events/missing/1/proof", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing proof, got %d", w.Code)
	}
}

func TestHandler_EventsOmitProofByDefault(t *testing.T) {
	sqliteStore := newTempIntegrityStore(t)
	defer sqliteStore.Close()

	sessionID := "control-events-no-proof"
	ctx := context.Background()
	if err := sqliteStore.RecordEvent(ctx, storage.EventSessionEnded, sessionID, "", storage.SessionEndedData{State: "completed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sqliteStore.ComputeAndStoreSDRIntegrity(ctx, sessionID); err != nil {
		t.Fatal(err)
	}

	handler := newHistoryHandler(sqliteStore)
	req := httptest.NewRequest("GET", "/control/events?session_id="+sessionID, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := w.Body.String(); json.Valid([]byte(body)) && (strings.Contains(body, "sibling_path") || strings.Contains(body, "event_hash")) {
		t.Fatalf("default event listing should omit proof metadata, got %s", body)
	}
}

func newHistoryHandler(sqliteStore *storage.SQLiteStore) *control.Handler {
	memStore := session.NewMemoryStore()
	manager := session.NewManager(memStore, 5*time.Minute)
	return control.New(memStore, manager, control.WithHistory(sqliteStore))
}
