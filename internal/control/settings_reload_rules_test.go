package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/policy"
	"elida/internal/session"
)

func ruleNames(e *policy.Engine) []string {
	var names []string
	for _, r := range e.GetConfig().Rules {
		names = append(names, r.Name)
	}
	return names
}

// Saving a setting reloads the policy engine. The rules the operator loaded at
// startup (the preset, plus rules in elida.yaml) are not part of the settings,
// so a reload that rebuilds the rule list from the settings file alone leaves
// the engine with no preset rules at all, silently, after any settings save.
func TestSettingsSaveKeepsTheStartupPolicyRules(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Policy.Enabled = true
	cfg.Policy.Preset = "standard"
	cfg.ApplyPolicyPreset()

	var rules []policy.Rule
	for _, r := range cfg.Policy.Rules {
		rules = append(rules, policy.Rule{
			Name: r.Name, Type: policy.RuleType(r.Type), Target: policy.RuleTarget(r.Target),
			Patterns: r.Patterns, Threshold: r.Threshold, Severity: policy.Severity(r.Severity),
			Action: r.Action, Description: r.Description,
		})
	}
	engine := policy.NewEngine(policy.Config{Enabled: true, Mode: "enforce", Rules: rules})
	before := ruleNames(engine)
	if len(before) < 5 {
		t.Fatalf("test setup: want a populated rule list, got %v", before)
	}

	store, err := config.NewSettingsStoreFromConfig(cfg, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mgr := session.NewManager(session.NewMemoryStore(), time.Minute)
	h := New(session.NewMemoryStore(), mgr, WithPolicy(engine))
	h.SetSettingsStore(store)

	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/control/settings", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET settings: %d %s", get.Code, get.Body.String())
	}
	put := httptest.NewRecorder()
	h.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/control/settings", strings.NewReader(get.Body.String())))
	if put.Code != http.StatusOK {
		t.Fatalf("PUT settings: %d %s", put.Code, put.Body.String())
	}

	after := ruleNames(engine)
	if len(after) != len(before) {
		t.Errorf("a settings save changed the rule count from %d to %d\nbefore: %v\nafter:  %v", len(before), len(after), before, after)
	}
}

// reloadFixture builds a handler whose engine holds the standard preset's
// rules, as at startup, and returns a helper that saves settings with extra
// policy fields set.
func reloadFixture(t *testing.T) (*policy.Engine, func(policyFields map[string]any)) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Policy.Enabled = true
	cfg.Policy.Preset = "standard"
	cfg.ApplyPolicyPreset()
	var rules []policy.Rule
	for _, r := range cfg.Policy.Rules {
		rules = append(rules, policy.Rule{
			Name: r.Name, Type: policy.RuleType(r.Type), Target: policy.RuleTarget(r.Target),
			Patterns: r.Patterns, Threshold: r.Threshold, Severity: policy.Severity(r.Severity),
			Action: r.Action, Description: r.Description,
		})
	}
	engine := policy.NewEngine(policy.Config{Enabled: true, Mode: "enforce", Rules: rules})
	store, err := config.NewSettingsStoreFromConfig(cfg, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := New(session.NewMemoryStore(), session.NewManager(session.NewMemoryStore(), time.Minute), WithPolicy(engine))
	h.SetSettingsStore(store)

	save := func(policyFields map[string]any) {
		t.Helper()
		get := httptest.NewRecorder()
		h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/control/settings", nil))
		var body map[string]any
		if err := json.Unmarshal(get.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		// The response wraps the settings; save the merged view with the
		// requested policy fields set.
		settings, _ := body["merged"].(map[string]any)
		if settings == nil {
			settings = body
		}
		pol, _ := settings["policy"].(map[string]any)
		if pol == nil {
			pol = map[string]any{}
			settings["policy"] = pol
		}
		for k, v := range policyFields {
			pol[k] = v
		}
		raw, _ := json.Marshal(settings)
		put := httptest.NewRecorder()
		h.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/control/settings", strings.NewReader(string(raw))))
		if put.Code != http.StatusOK {
			t.Fatalf("PUT settings: %d %s", put.Code, put.Body.String())
		}
	}
	return engine, save
}

func has(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// disabled_rules is saved and merged but was never applied: the reload now
// honors it, and only it, so every other startup rule stays.
func TestSettingsSaveHonorsDisabledRules(t *testing.T) {
	engine, save := reloadFixture(t)
	before := ruleNames(engine)
	save(map[string]any{"disabled_rules": []string{"rate_limit_high"}})
	after := ruleNames(engine)
	if has(after, "rate_limit_high") {
		t.Error("a rule listed in disabled_rules is still loaded")
	}
	if len(after) != len(before)-1 {
		t.Errorf("rule count %d -> %d, want exactly one fewer", len(before), len(after))
	}
}

// A custom rule saved through settings adds to the startup rules, and a custom
// rule with a startup rule's name replaces it (as in config loading).
func TestSettingsSaveAddsAndOverridesWithCustomRules(t *testing.T) {
	engine, save := reloadFixture(t)
	before := ruleNames(engine)
	save(map[string]any{"custom_rules": []map[string]any{
		{"name": "my_new_rule", "type": "content_match", "target": "request", "patterns": []string{"zzz"}, "severity": "warning", "action": "flag"},
		{"name": "rate_limit_high", "type": "requests_per_minute", "threshold": 5, "severity": "critical", "action": "block"},
	}})
	after := ruleNames(engine)
	if !has(after, "my_new_rule") {
		t.Error("the saved custom rule is not loaded")
	}
	if len(after) != len(before)+1 {
		t.Errorf("rule count %d -> %d, want one more (one added, one overridden)", len(before), len(after))
	}
	n := 0
	for _, r := range engine.GetConfig().Rules {
		if r.Name == "rate_limit_high" {
			n++
			if r.Threshold != 5 {
				t.Errorf("override not applied: threshold = %d, want 5", r.Threshold)
			}
		}
	}
	if n != 1 {
		t.Errorf("rate_limit_high appears %d times, want exactly 1", n)
	}
}
