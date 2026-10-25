package unit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"elida/internal/control"
	"elida/internal/session"
)

func TestDecisionControl_UnwiredStatusMatchesWiredDefaults(t *testing.T) {
	// With no provider wired, the shape matches what a disabled deployment
	// reports through main: effective_mode disabled and coverage_gaps
	// present, so a dashboard never has to special-case missing keys.
	store := session.NewMemoryStore()
	h := control.New(store, session.NewManager(store, 5*time.Minute))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/control/decision", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw["enabled"] != false || raw["effective_mode"] != "disabled" || raw["capability"] != "disabled" {
		t.Fatalf("unwired status = %s", w.Body.String())
	}
	if gaps, ok := raw["coverage_gaps"].(map[string]any); !ok || len(gaps) != 0 {
		t.Fatalf("coverage_gaps must be present and empty: %s", w.Body.String())
	}
}

func TestDecisionControl_CoverageGapsKeyAlwaysSerialized(t *testing.T) {
	b, err := json.Marshal(control.DecisionStatus{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	if _, ok := raw["coverage_gaps"]; !ok {
		t.Fatalf("coverage_gaps must never be omitted: %s", b)
	}
}
