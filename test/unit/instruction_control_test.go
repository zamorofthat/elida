package unit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"elida/internal/control"
	"elida/internal/instruction"
	"elida/internal/session"
)

func TestControlInstructionFilesList(t *testing.T) {
	store := newInstructionTestStore(t)
	now := time.Now().UTC()

	// Insert test data
	if err := store.SaveInstructionFile(instruction.Record{
		Hash: "abc123", FileType: "claude_md", Confidence: "high",
		Content: "# Rules", ScanStatus: "clean",
		FirstSeen: now, LastSeen: now, SessionCount: 1,
	}); err != nil {
		t.Fatal(err)
	}

	memStore := session.NewMemoryStore()
	mgr := session.NewManager(memStore, 5*time.Minute)
	h := control.New(memStore, mgr, control.WithHistory(store))

	req := httptest.NewRequest(http.MethodGet, "/control/instructions", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp struct {
		Files []instruction.Record `json:"files"`
		Total int                  `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 1 {
		t.Errorf("total = %d, want 1", resp.Total)
	}
}

func TestControlInstructionFileDetail(t *testing.T) {
	store := newInstructionTestStore(t)
	now := time.Now().UTC()

	if err := store.SaveInstructionFile(instruction.Record{
		Hash: "abc123", FileType: "claude_md", Confidence: "high",
		Content: "# Rules", ScanStatus: "clean",
		FirstSeen: now, LastSeen: now, SessionCount: 1,
	}); err != nil {
		t.Fatal(err)
	}

	memStore := session.NewMemoryStore()
	mgr := session.NewManager(memStore, 5*time.Minute)
	h := control.New(memStore, mgr, control.WithHistory(store))

	req := httptest.NewRequest(http.MethodGet, "/control/instructions/abc123", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var record instruction.Record
	if err := json.Unmarshal(w.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record.Hash != "abc123" {
		t.Errorf("hash = %q, want %q", record.Hash, "abc123")
	}
}

func TestControlInstructionFileNotFound(t *testing.T) {
	store := newInstructionTestStore(t)
	memStore := session.NewMemoryStore()
	mgr := session.NewManager(memStore, 5*time.Minute)
	h := control.New(memStore, mgr, control.WithHistory(store))

	req := httptest.NewRequest(http.MethodGet, "/control/instructions/nonexistent", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestControlInstructionFilesEmpty(t *testing.T) {
	store := newInstructionTestStore(t)
	memStore := session.NewMemoryStore()
	mgr := session.NewManager(memStore, 5*time.Minute)
	h := control.New(memStore, mgr, control.WithHistory(store))

	req := httptest.NewRequest(http.MethodGet, "/control/instructions", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp struct {
		Files []interface{} `json:"files"`
		Total int           `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 0 {
		t.Errorf("total = %d, want 0", resp.Total)
	}
}
