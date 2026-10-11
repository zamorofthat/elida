package unit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/control"
	"elida/internal/policy"
	"elida/internal/session"
)

// decisionViolation is semanticViolation with the fields Plan C-1 tests
// vary: a distinct decision ID, message index and description per call.
//
//nolint:unparam // rule is part of the shared helper signature; later Plan C-1 tests vary it.
func decisionViolation(rule string, sev policy.Severity, evidenceOnly bool, prob float64, decisionID string, msgIndex int, desc string) policy.Violation {
	v := semanticViolation(rule, sev, evidenceOnly, prob)
	v.Semantic.DecisionID = decisionID
	v.MessageIndex = msgIndex
	v.Description = desc
	v.Timestamp = time.Now()
	return v
}

// settingsHandler is a control handler with a fresh settings store.
func settingsHandler(t *testing.T, engine *policy.Engine) (*control.Handler, *config.SettingsStore) {
	t.Helper()
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	ss, err := config.NewSettingsStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSettingsStore: %v", err)
	}
	var opts []control.Option
	if engine != nil {
		opts = append(opts, control.WithPolicy(engine))
	}
	h := control.New(store, manager, opts...)
	h.SetSettingsStore(ss)
	return h, ss
}

// doJSON sends one JSON request through h. A string body is sent verbatim.
func doJSON(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload string
	switch b := body.(type) {
	case nil:
	case string:
		payload = b
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		payload = string(raw)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// KEEP. Every semantic decision is its own event and contributes (or not)
// on its own: warning 3.0 + critical 10.0 from user content, evidence 0.
// The per-rule display entry stays one Violation.
func TestBaselineC1_SemanticFiringsScorePerDecision(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	e.RecordSemanticViolation("sess-c1-score", decisionViolation("semantic_injection", policy.SeverityWarning, false, 0.6, "dec_a", 1, "first"))
	e.RecordSemanticViolation("sess-c1-score", decisionViolation("semantic_injection", policy.SeverityCritical, false, 0.9, "dec_b", 2, "second"))
	e.RecordSemanticViolation("sess-c1-score", decisionViolation("semantic_injection", policy.SeverityCritical, true, 0.95, "dec_c", 3, "third"))

	score, _, _ := e.GetSessionRiskScore("sess-c1-score")
	if !approxEqual(score, 13.0, 0.1) {
		t.Fatalf("score = %v, want ~13.0 (3 + 10, evidence adds nothing)", score)
	}
	points := e.ComputeRiskCurve("sess-c1-score")
	if len(points) == 0 || !approxEqual(points[len(points)-1].Score, score, 0.1) {
		t.Fatalf("curve tail disagrees with the score: %+v vs %v", points, score)
	}
	fs := e.GetFlaggedSession("sess-c1-score")
	if len(fs.Violations) != 1 {
		t.Fatalf("display violations = %d, want 1 per rule", len(fs.Violations))
	}
	if fs.ViolationCounts["semantic_injection"] != 3 {
		t.Fatalf("ViolationCounts = %d, want 3", fs.ViolationCounts["semantic_injection"])
	}
	if len(fs.ViolationEvents) != 3 {
		t.Fatalf("events = %d, want 3", len(fs.ViolationEvents))
	}
	seen := map[string]bool{}
	for _, ev := range fs.ViolationEvents {
		if ev.EventID == "" || seen[ev.EventID] {
			t.Fatalf("event IDs must be present and distinct: %+v", fs.ViolationEvents)
		}
		seen[ev.EventID] = true
	}
}

// KEEP. Once a rule has contributed, a later evidence-only firing never
// relabels its display entry as evidence-only (Task 29).
func TestBaselineC1_LaterEvidenceDoesNotRelabelAContributingRule(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	e.RecordSemanticViolation("sess-c1-keep", decisionViolation("semantic_injection", policy.SeverityWarning, false, 0.6, "dec_a", 1, "first"))
	e.RecordSemanticViolation("sess-c1-keep", decisionViolation("semantic_injection", policy.SeverityCritical, true, 0.95, "dec_b", 2, "second"))

	v := e.GetFlaggedSession("sess-c1-keep").Violations[0]
	if v.EvidenceOnly {
		t.Fatal("a contributing rule must not be relabelled evidence-only")
	}
	if v.Semantic == nil || v.Semantic.DecisionID != "dec_a" {
		t.Fatalf("the contributing decision must stay on the entry: %+v", v.Semantic)
	}
}

// KEEP. An evidence-only first firing followed by a contributing one
// relabels the entry as contributing, with the contributing decision.
func TestBaselineC1_ContributingAfterEvidenceRelabels(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	e.RecordSemanticViolation("sess-c1-up", decisionViolation("semantic_injection", policy.SeverityWarning, true, 0.6, "dec_a", 1, "first"))
	e.RecordSemanticViolation("sess-c1-up", decisionViolation("semantic_injection", policy.SeverityWarning, false, 0.7, "dec_b", 2, "second"))

	v := e.GetFlaggedSession("sess-c1-up").Violations[0]
	if v.EvidenceOnly || v.Semantic == nil || v.Semantic.DecisionID != "dec_b" {
		t.Fatalf("entry = %+v (semantic %+v), want contributing dec_b", v, v.Semantic)
	}
}

// KEEP. The dashboard GETs the merged settings and PUTs them back whole.
// That round trip must stay a no-op: 200, nothing modified.
func TestBaselineC1_SettingsRoundTripIsANoOp(t *testing.T) {
	engine := policy.NewEngine(policy.Config{Enabled: true, Mode: "enforce"})
	h, _ := settingsHandler(t, engine)

	got := doJSON(t, h, http.MethodGet, "/control/settings", nil)
	if got.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", got.Code, got.Body.String())
	}
	put := doJSON(t, h, http.MethodPut, "/control/settings", got.Body.String())
	if put.Code != http.StatusOK {
		t.Fatalf("PUT of the unchanged merged object: %d %s", put.Code, put.Body.String())
	}
	diff := doJSON(t, h, http.MethodGet, "/control/settings/diff", nil)
	var d struct {
		ModifiedCount int `json:"modified_count"`
	}
	if err := json.Unmarshal(diff.Body.Bytes(), &d); err != nil {
		t.Fatalf("diff: %v", err)
	}
	if d.ModifiedCount != 0 {
		t.Fatalf("a round trip modified %d settings: %s", d.ModifiedCount, diff.Body.String())
	}
}

// KEEP. A settings PUT reloads the policy engine at runtime.
func TestBaselineC1_SettingsPUTReloadsPolicyMode(t *testing.T) {
	engine := policy.NewEngine(policy.Config{Enabled: true, Mode: "enforce"})
	h, _ := settingsHandler(t, engine)
	if engine.IsAuditMode() {
		t.Fatal("fixture: engine should start in enforce")
	}
	w := doJSON(t, h, http.MethodPut, "/control/settings", map[string]any{
		"policy": map[string]any{"enabled": true, "mode": "audit"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if !engine.IsAuditMode() {
		t.Fatal("policy.mode audit must apply at runtime")
	}
}

// FLIP in Task 6. Today a PUT may change a restart-only decision key; it
// is saved to settings.yaml and silently never applied.
func TestBaselineC1_SettingsPUTAcceptsRestartOnlyDecisionKeys(t *testing.T) {
	h, ss := settingsHandler(t, nil)
	w := doJSON(t, h, http.MethodPut, "/control/settings", map[string]any{
		"decision": map[string]any{"max_concurrency": 2},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if got := ss.GetMerged().Decision.MaxConcurrency; got == nil || *got != 2 {
		t.Fatalf("today the value is saved: %v", got)
	}
}
