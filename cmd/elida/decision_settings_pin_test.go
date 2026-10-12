package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"elida/internal/config"
)

// dashboardSave does what the dashboard does: GET the merged settings,
// apply edit to the decoded body, and PUT the whole object back.
func dashboardSave(t *testing.T, a *app, edit func(body map[string]any)) {
	t.Helper()
	get := httptest.NewRecorder()
	a.controlHandler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/control/settings", nil))
	var body map[string]any
	if err := json.Unmarshal(get.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET /control/settings: %d %s", get.Code, get.Body.String())
	}
	edit(body)
	raw, _ := json.Marshal(body)
	put := httptest.NewRecorder()
	a.controlHandler.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/control/settings", strings.NewReader(string(raw))))
	if put.Code != http.StatusOK {
		t.Fatalf("PUT /control/settings: %d %s", put.Code, put.Body.String())
	}
}

// storedSettings reads the settings.yaml a save wrote, raw and parsed.
func storedSettings(t *testing.T, a *app) (string, config.Settings) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(a.configPath), "settings.yaml"))
	if err != nil {
		t.Fatalf("read settings.yaml: %v", err)
	}
	var s config.Settings
	if err := yaml.Unmarshal(raw, &s); err != nil {
		t.Fatalf("parse settings.yaml: %v", err)
	}
	return string(raw), s
}

// An unrelated dashboard save must not pin elida.yaml's decision values in
// settings.yaml: after elida.yaml goes from enforce to audit and ELIDA
// restarts, audit runs (final review I-1).
func TestDecisionSettingsPin_UnrelatedSaveDoesNotPinElidaYaml(t *testing.T) {
	a := settingsApp(t, config.DecisionModeEnforce, "", nil)
	if err := a.applyStartupDecisionSettings(); err != nil {
		t.Fatal(err)
	}
	a.initControlAPI()
	if got := a.decisionRunner.EffectiveMode(); got != config.DecisionModeEnforce {
		t.Fatalf("first run effective mode = %q, want enforce", got)
	}
	dashboardSave(t, a, func(body map[string]any) {
		fo, _ := body["failover"].(map[string]any)
		if fo == nil {
			fo = map[string]any{}
			body["failover"] = fo
		}
		fo["max_retries"] = 3
	})
	raw, stored := storedSettings(t, a)
	if stored.Decision.Mode != nil || stored.Decision.Preprocessing != nil || stored.Decision.MaxConcurrency != nil || stored.Decision.ThresholdSet != nil {
		t.Fatalf("an unrelated save stored decision values:\n%s", raw)
	}
	if stored.Failover.MaxRetries == nil || *stored.Failover.MaxRetries != 3 {
		t.Fatalf("the unrelated change was not saved:\n%s", raw)
	}

	// The operator lowers elida.yaml to audit and restarts.
	b := settingsApp(t, config.DecisionModeAudit, raw, nil)
	logs := captureLogs(t)
	if err := b.applyStartupDecisionSettings(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if live, eff := b.decisionRunner.Live().Mode, b.decisionRunner.EffectiveMode(); live != config.DecisionModeAudit || eff != config.DecisionModeAudit {
		t.Fatalf("after restart live=%q effective=%q, want audit (elida.yaml)", live, eff)
	}
	if strings.Contains(logs.String(), "settings.yaml overrides elida.yaml") {
		t.Fatalf("no override expected:\n%s", logs.String())
	}
}

// An explicit decision.mode change is an override: it is stored, applied at
// restart, and logged at WARN with both values.
func TestDecisionSettingsPin_ExplicitModeChangeIsStoredAndWarnedAtStartup(t *testing.T) {
	a := settingsApp(t, config.DecisionModeAudit, "", nil)
	if err := a.applyStartupDecisionSettings(); err != nil {
		t.Fatal(err)
	}
	a.initControlAPI()
	dashboardSave(t, a, func(body map[string]any) {
		dec, ok := body["decision"].(map[string]any)
		if !ok {
			t.Fatalf("GET body has no decision section: %v", body)
		}
		dec["mode"] = config.DecisionModeShadow
	})
	raw, stored := storedSettings(t, a)
	if stored.Decision.Mode == nil || *stored.Decision.Mode != config.DecisionModeShadow {
		t.Fatalf("the explicit mode change was not stored:\n%s", raw)
	}
	if stored.Decision.Preprocessing != nil || stored.Decision.MaxConcurrency != nil {
		t.Fatalf("only the changed decision key may be stored:\n%s", raw)
	}

	b := settingsApp(t, config.DecisionModeAudit, raw, nil)
	logs := captureLogs(t)
	if err := b.applyStartupDecisionSettings(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if got := b.decisionRunner.Live().Mode; got != config.DecisionModeShadow {
		t.Fatalf("after restart live=%q, want shadow (the stored override)", got)
	}
	out := logs.String()
	for _, want := range []string{"level=WARN", "settings.yaml overrides elida.yaml", "key=decision.mode", "settings_value=shadow", "elida_yaml_value=audit"} {
		if !strings.Contains(out, want) {
			t.Fatalf("startup log lacks %q:\n%s", want, out)
		}
	}
}

// /control/decision explains a policy.mode audit cap on a configured enforce
// (final review L-1).
func TestDecisionStatus_ReasonNamesThePolicyAuditCap(t *testing.T) {
	a := settingsApp(t, config.DecisionModeEnforce, "", func(c *config.Config) { c.Policy.Mode = "audit" })
	st := a.DecisionStatus()
	if st.Mode != config.DecisionModeEnforce || st.EffectiveMode != config.DecisionModeAudit {
		t.Fatalf("mode=%q effective=%q, want enforce capped to audit", st.Mode, st.EffectiveMode)
	}
	if !strings.Contains(st.Reason, "capped by policy.mode=audit") {
		t.Fatalf("reason = %q, want it to name the policy.mode audit cap", st.Reason)
	}

	b := settingsApp(t, config.DecisionModeEnforce, "", nil)
	if st := b.DecisionStatus(); strings.Contains(st.Reason, "capped by policy.mode=audit") {
		t.Fatalf("an uncapped enforce must not report the cap: %q", st.Reason)
	}
}
