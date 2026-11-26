package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"elida/internal/session"
)

// A reader of one session's shadow evidence must be able to tell the list was
// truncated: the detail response reports how many entries each list dropped.
func TestSessionDetailReportsShadowDrops(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 30*time.Second)
	h := New(store, manager)

	sess := manager.GetOrCreate("drops-1", "http://b", "127.0.0.1")
	for i := 0; i < session.MaxSemanticShadow+2; i++ {
		sess.RecordSemanticShadow(session.SemanticShadow{})
	}
	for i := 0; i < session.MaxCorrelationShadow+5; i++ {
		sess.RecordCorrelationShadow(session.CorrelationShadow{})
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/control/sessions/drops-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		SemanticShadowDropped    int `json:"semantic_shadow_dropped"`
		CorrelationShadowDropped int `json:"correlation_shadow_dropped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SemanticShadowDropped != 2 || got.CorrelationShadowDropped != 5 {
		t.Errorf("dropped = %d/%d, want 2/5", got.SemanticShadowDropped, got.CorrelationShadowDropped)
	}
}
