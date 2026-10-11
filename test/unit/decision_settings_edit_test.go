package unit

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/control"
	"elida/internal/policy"
	"elida/internal/session"
)

type stubConfigurer struct {
	mu       sync.Mutex
	problems []string
	applied  []string // "mode/policyMode" per apply
}

func (s *stubConfigurer) ValidateDecisionSettings(next config.DecisionSettings, policyMode string) []string {
	return s.problems
}

func (s *stubConfigurer) ApplyDecisionSettings(next config.DecisionSettings, policyMode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	mode := ""
	if next.Mode != nil {
		mode = *next.Mode
	}
	s.applied = append(s.applied, mode+"/"+policyMode)
	return nil
}

func (s *stubConfigurer) last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.applied) == 0 {
		return ""
	}
	return s.applied[len(s.applied)-1]
}

func decisionPut(mode string) map[string]any {
	return map[string]any{"decision": map[string]any{"mode": mode}}
}

// Replaces TestBaselineC1_SettingsPUTAcceptsRestartOnlyDecisionKeys.
func TestDecisionSettingsEdit_RestartOnlyChangeRefused(t *testing.T) {
	h, ss := settingsHandler(t, nil)
	w := doJSON(t, h, http.MethodPut, "/control/settings", map[string]any{"decision": map[string]any{"max_concurrency": 2}})
	if w.Code != http.StatusBadRequest || !contains(w.Body.String(), "decision.max_concurrency") || !contains(w.Body.String(), "restart") {
		t.Fatalf("got %d %s, want 400 naming the restart-only key", w.Code, w.Body.String())
	}
	if *ss.GetMerged().Decision.MaxConcurrency == 2 {
		t.Fatal("a refused edit must not be saved")
	}
}

// Review Focus 4.
func TestDecisionSettingsEdit_StaleRestartOnlyValuesDoNotBlockSaves(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.yaml"), []byte("decision:\n  max_concurrency: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ss, err := config.NewSettingsStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := settingsHandler(t, nil)
	h.SetSettingsStore(ss)
	got := doJSON(t, h, http.MethodGet, "/control/settings", nil)
	if w := doJSON(t, h, http.MethodPut, "/control/settings", got.Body.String()); w.Code != http.StatusOK {
		t.Fatalf("saving the unchanged merged object must work with a stale value present: %d %s", w.Code, w.Body.String())
	}
}

func TestDecisionSettingsEdit_EnforceNeedsConfirmation(t *testing.T) {
	h, ss := settingsHandler(t, policy.NewEngine(policy.Config{Enabled: true, Mode: "enforce"}))
	cfgr := &stubConfigurer{}
	h.SetDecisionConfigurer(cfgr)

	w := doJSON(t, h, http.MethodPut, "/control/settings", decisionPut("enforce"))
	if w.Code != http.StatusBadRequest || !contains(w.Body.String(), "confirm=enforce") {
		t.Fatalf("got %d %s, want 400 asking for confirmation", w.Code, w.Body.String())
	}
	if *ss.GetMerged().Decision.Mode == "enforce" {
		t.Fatal("an unconfirmed enforce must not be saved")
	}
	w = doJSON(t, h, http.MethodPut, "/control/settings?confirm=enforce", decisionPut("enforce"))
	if w.Code != http.StatusOK {
		t.Fatalf("confirmed: %d %s", w.Code, w.Body.String())
	}
	if cfgr.last() != "enforce/enforce" {
		t.Fatalf("applied %q, want enforce/enforce", cfgr.last())
	}
}

func TestDecisionSettingsEdit_EnforceRefusedUnderPolicyAudit(t *testing.T) {
	h, _ := settingsHandler(t, policy.NewEngine(policy.Config{Enabled: true, Mode: "enforce"}))
	h.SetDecisionConfigurer(&stubConfigurer{})
	w := doJSON(t, h, http.MethodPut, "/control/settings?confirm=enforce", map[string]any{
		"policy": map[string]any{"enabled": true, "mode": "audit"}, "decision": map[string]any{"mode": "enforce"},
	})
	if w.Code != http.StatusBadRequest || !contains(w.Body.String(), "policy.mode") {
		t.Fatalf("got %d %s, want 400 citing policy.mode", w.Code, w.Body.String())
	}
}

// D8: a policy.mode change re-applies the decision section so the runner re-caps.
func TestDecisionSettingsEdit_PolicyModeChangeReappliesDecision(t *testing.T) {
	h, _ := settingsHandler(t, policy.NewEngine(policy.Config{Enabled: true, Mode: "enforce"}))
	cfgr := &stubConfigurer{}
	h.SetDecisionConfigurer(cfgr)
	w := doJSON(t, h, http.MethodPut, "/control/settings", map[string]any{"policy": map[string]any{"enabled": true, "mode": "audit"}})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if cfgr.last() != "shadow/audit" {
		t.Fatalf("applied %q, want shadow/audit", cfgr.last())
	}
}

// D10: one INFO line per changed decision key, with previous and new values.
func TestDecisionSettingsEdit_ChangesAreLogged(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h, _ := settingsHandler(t, nil)
	h.SetDecisionConfigurer(&stubConfigurer{})
	if w := doJSON(t, h, http.MethodPut, "/control/settings", decisionPut("audit")); w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	out := buf.String()
	// authenticated reports whether the request passed control API
	// authentication: with auth off, nothing was authenticated.
	for _, want := range []string{"decision setting changed", "key=decision.mode", "previous=shadow", "new=audit", "remote_addr=", "authenticated=false"} {
		if !contains(out, want) {
			t.Fatalf("log lacks %q: %s", want, out)
		}
	}

	// With auth on, a request reaches the handler only after passing it
	// (here through X-API-Key, not Authorization), so the line says true.
	buf.Reset()
	store := session.NewMemoryStore()
	authed := control.New(store, session.NewManager(store, 5*time.Minute), control.WithAuth("k3y"))
	ss, err := config.NewSettingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	authed.SetSettingsStore(ss)
	authed.SetDecisionConfigurer(&stubConfigurer{})
	req := httptest.NewRequest(http.MethodPut, "/control/settings", strings.NewReader(`{"decision":{"mode":"audit"}}`))
	req.Header.Set("X-API-Key", "k3y")
	w := httptest.NewRecorder()
	authed.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("authenticated PUT: %d %s", w.Code, w.Body.String())
	}
	if out := buf.String(); !contains(out, "key=decision.mode") || !contains(out, "authenticated=true") || contains(out, "k3y") {
		t.Fatalf("authenticated audit line wrong (and must never carry the key): %s", out)
	}
}

// builtinDecisionDefaults is the decision section a fresh settings store
// holds in force (its defaults layer).
func builtinDecisionDefaults(t *testing.T) config.DecisionSettings {
	t.Helper()
	ss, err := config.NewSettingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return ss.GetDefaults().Decision
}

// staleSettingsHandler is a settings handler (policy engine, stub
// configurer) whose settings.yaml holds yamlBody, as left by an earlier save.
func staleSettingsHandler(t *testing.T, yamlBody string) (*control.Handler, *config.SettingsStore) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.yaml"), []byte(yamlBody), 0o600); err != nil {
		t.Fatal(err)
	}
	ss, err := config.NewSettingsStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := settingsHandler(t, policy.NewEngine(policy.Config{Enabled: true, Mode: "enforce"}))
	h.SetSettingsStore(ss)
	h.SetDecisionConfigurer(&stubConfigurer{})
	return h, ss
}

// Review M-1: a stale restart-only value never blocks a save. Omitting it
// (a partial PUT) or setting it back to the value in force is accepted;
// any other value is still refused.
func TestDecisionSettingsEdit_StaleRestartOnlyValueCanBeOmittedOrReset(t *testing.T) {
	const stale = "decision:\n  max_concurrency: 2\n"
	inForce := *builtinDecisionDefaults(t).MaxConcurrency

	for name, body := range map[string]any{
		"partial decision PUT": decisionPut("audit"),
		"partial policy PUT":   map[string]any{"policy": map[string]any{"enabled": true, "mode": "audit"}},
		"reset to in force":    map[string]any{"decision": map[string]any{"max_concurrency": inForce}},
	} {
		h, ss := staleSettingsHandler(t, stale)
		if w := doJSON(t, h, http.MethodPut, "/control/settings", body); w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
		if got := *ss.GetMerged().Decision.MaxConcurrency; got != inForce {
			t.Fatalf("%s: merged max_concurrency = %d, want the in-force %d", name, got, inForce)
		}
	}

	h, ss := staleSettingsHandler(t, stale)
	w := doJSON(t, h, http.MethodPut, "/control/settings", map[string]any{"decision": map[string]any{"max_concurrency": 7}})
	if w.Code != http.StatusBadRequest || !contains(w.Body.String(), "decision.max_concurrency") {
		t.Fatalf("a third value: got %d %s, want 400", w.Code, w.Body.String())
	}
	if *ss.GetMerged().Decision.MaxConcurrency != 2 {
		t.Fatal("a refused edit must not be saved")
	}
}

func TestValidateDecisionEdit_AcceptsARestartOnlyKeyBackToItsValueInForce(t *testing.T) {
	inForce := builtinDecisionDefaults(t)
	one, four, seven := 1, *inForce.MaxConcurrency, 7
	prev, back, other := inForce, inForce, inForce
	prev.MaxConcurrency, back.MaxConcurrency, other.MaxConcurrency = &one, &four, &seven
	if probs := config.ValidateDecisionEdit(prev, back, inForce, "enforce"); len(probs) != 0 {
		t.Fatalf("back to the value in force must validate: %v", probs)
	}
	if probs := config.ValidateDecisionEdit(prev, other, inForce, "enforce"); len(probs) != 1 || !contains(probs[0], "decision.max_concurrency") {
		t.Fatalf("any other value must be refused: %v", probs)
	}
}

// Review M-2: a reserved custom-rule name already saved does not block
// unrelated saves (it is skipped at runtime, with a WARN); a reserved name
// new in this save is still refused.
func TestDecisionSettingsEdit_PreexistingReservedRuleNameDoesNotBlockSaves(t *testing.T) {
	var buf bytes.Buffer
	prevLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prevLog) })

	const yamlBody = "policy:\n  custom_rules:\n    - name: semantic_injection\n      type: content_match\n      patterns: [\"x\"]\n      severity: warning\n"
	h, ss := staleSettingsHandler(t, yamlBody)
	got := doJSON(t, h, http.MethodGet, "/control/settings", nil)
	if w := doJSON(t, h, http.MethodPut, "/control/settings", got.Body.String()); w.Code != http.StatusOK {
		t.Fatalf("round trip with a pre-existing reserved rule: %d %s", w.Code, w.Body.String())
	}
	if out := buf.String(); !contains(out, "level=WARN") || !contains(out, "rule=semantic_injection") {
		t.Fatalf("a pre-existing reserved rule must WARN: %s", out)
	}
	if rules := ss.GetMerged().Policy.CustomRules; len(rules) != 1 || rules[0].Name != "semantic_injection" {
		t.Fatalf("the saved body must not be rewritten: %+v", rules)
	}

	w := doJSON(t, h, http.MethodPut, "/control/settings", map[string]any{"policy": map[string]any{
		"custom_rules": []map[string]any{
			{"name": "semantic_injection", "type": "content_match", "patterns": []string{"x"}, "severity": "warning"},
			{"name": "injection_elevated", "type": "content_match", "patterns": []string{"y"}, "severity": "warning"},
		},
	}})
	if w.Code != http.StatusBadRequest || !contains(w.Body.String(), "injection_elevated") || contains(w.Body.String(), `\"semantic_injection\"`) {
		t.Fatalf("a new reserved name: got %d %s, want 400 naming only the new one", w.Code, w.Body.String())
	}
}

func TestDecisionSettingsChanges_ReportsOnlyChangedKeys(t *testing.T) {
	ss, _ := config.NewSettingsStore(t.TempDir())
	prev := ss.GetMerged().Decision
	local := config.Settings{}
	mode, depth := "audit", 1
	local.Decision.Mode = &mode
	local.Decision.Preprocessing = &config.DecisionPreprocessingSettings{MaxDecodeDepth: &depth}
	next := ss.PreviewMerged(local).Decision
	got := config.DecisionSettingsChanges(prev, next)
	if len(got) != 2 || got[0].Key != "decision.mode" || got[1].Key != "decision.preprocessing.max_decode_depth" {
		t.Fatalf("changes = %+v", got)
	}
	if probs := config.ValidateDecisionEdit(prev, next, ss.GetDefaults().Decision, "enforce"); len(probs) != 0 {
		t.Fatalf("runtime keys must validate: %v", probs)
	}
	if *ss.GetMerged().Decision.Mode != "shadow" {
		t.Fatal("PreviewMerged must not save")
	}
}

// D11 through the second PUT route: /control/settings/local shares the
// save helper.
func TestDecisionSettingsEdit_LocalRouteRefusesRestartOnlyChange(t *testing.T) {
	h, ss := settingsHandler(t, nil)
	w := doJSON(t, h, http.MethodPut, "/control/settings/local", map[string]any{"decision": map[string]any{"threshold_set": "v999"}})
	if w.Code != http.StatusBadRequest || !contains(w.Body.String(), "decision.threshold_set") {
		t.Fatalf("got %d %s, want 400 naming the restart-only key", w.Code, w.Body.String())
	}
	if *ss.GetMerged().Decision.ThresholdSet == "v999" {
		t.Fatal("a refused edit must not be saved")
	}
	w = doJSON(t, h, http.MethodPut, "/control/settings/local", decisionPut("audit"))
	if w.Code != http.StatusOK {
		t.Fatalf("the same route without restart-only keys: %d %s", w.Code, w.Body.String())
	}
}

// A custom rule saved through the control API may not take a name the
// semantic runner records under (closes the Task 1/2 review gap).
func TestDecisionSettingsEdit_ReservedCustomRuleNameRefused(t *testing.T) {
	for _, name := range []string{"semantic_injection", "injection_elevated"} {
		h, ss := settingsHandler(t, policy.NewEngine(policy.Config{Enabled: true, Mode: "enforce"}))
		w := doJSON(t, h, http.MethodPut, "/control/settings", map[string]any{"policy": map[string]any{
			"custom_rules": []map[string]any{{"name": name, "type": "content_match", "patterns": []string{"x"}, "severity": "warning"}},
		}})
		if w.Code != http.StatusBadRequest || !contains(w.Body.String(), name) {
			t.Fatalf("%s: got %d %s, want 400 naming the reserved rule", name, w.Code, w.Body.String())
		}
		if len(ss.GetMerged().Policy.CustomRules) != 0 {
			t.Fatalf("%s: a refused custom rule must not be saved", name)
		}
	}
}

// policy.mode caps the semantic runner, which accepts only enforce or audit;
// an unknown value must be refused before it is saved, not half-applied.
func TestDecisionSettingsEdit_UnknownPolicyModeRefused(t *testing.T) {
	engine := policy.NewEngine(policy.Config{Enabled: true, Mode: "enforce"})
	h, ss := settingsHandler(t, engine)
	h.SetDecisionConfigurer(&stubConfigurer{})
	w := doJSON(t, h, http.MethodPut, "/control/settings", map[string]any{"policy": map[string]any{"enabled": true, "mode": "observe"}})
	if w.Code != http.StatusBadRequest || !contains(w.Body.String(), "policy.mode") {
		t.Fatalf("got %d %s, want 400 citing policy.mode", w.Code, w.Body.String())
	}
	if *ss.GetMerged().Policy.Mode != "enforce" || engine.IsAuditMode() {
		t.Fatal("a refused policy.mode must not be saved or applied")
	}
}

// PreviewMerged runs on every PUT, including refused ones, so it must not
// write through the defaults' shared pointers.
func TestSettingsPreviewMerged_DoesNotMutateDefaults(t *testing.T) {
	ss, err := config.NewSettingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	before := *ss.GetMerged().Policy.RiskLadder.WarnScore
	beforeShape := *ss.GetMerged().Policy.InstructionIntegrity.ShapeConfidenceThreshold
	warn, shape := before+40, beforeShape/2
	local := config.Settings{}
	local.Policy.RiskLadder = &config.RiskLadderSettings{WarnScore: &warn}
	local.Policy.InstructionIntegrity = &config.InstructionIntegritySettings{ShapeConfidenceThreshold: &shape}
	if got := *ss.PreviewMerged(local).Policy.RiskLadder.WarnScore; got != warn {
		t.Fatalf("preview warn = %d, want %d", got, warn)
	}
	if got := *ss.GetMerged().Policy.RiskLadder.WarnScore; got != before {
		t.Fatalf("a preview changed the defaults' warn_score to %d", got)
	}
	if got := *ss.GetDefaults().Policy.InstructionIntegrity.ShapeConfidenceThreshold; got != beforeShape {
		t.Fatalf("a preview changed the defaults' shape threshold to %v", got)
	}
}
