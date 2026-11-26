package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"elida/internal/session"
)

// An operator can query how many times a session's system prompt changed, on
// the single-session detail and in the session list, so a session whose rules
// moved mid-conversation can be found and alerted on.
func TestSessionsReportSystemPromptChanges(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 30*time.Second)
	h := New(store, manager)

	sess := manager.GetOrCreate("spc-1", "http://b", "127.0.0.1")
	sess.RecordSystemPromptChange()
	sess.RecordSystemPromptChange()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/control/sessions/spc-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("detail: status %d: %s", rec.Code, rec.Body.String())
	}
	var detail struct {
		SystemPromptChanges int `json:"system_prompt_changes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.SystemPromptChanges != 2 {
		t.Errorf("detail system_prompt_changes = %d, want 2", detail.SystemPromptChanges)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/control/sessions", nil))
	var list struct {
		Sessions []struct {
			ID                  string `json:"id"`
			SystemPromptChanges int    `json:"system_prompt_changes"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("list: %v: %s", err, rec.Body.String())
	}
	found := false
	for _, s := range list.Sessions {
		if s.ID == "spc-1" {
			found = true
			if s.SystemPromptChanges != 2 {
				t.Errorf("list system_prompt_changes = %d, want 2", s.SystemPromptChanges)
			}
		}
	}
	if !found {
		t.Errorf("session spc-1 missing from the list: %s", rec.Body.String())
	}
}
