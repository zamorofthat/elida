package unit

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
)

func TestDecisionConfig_Defaults(t *testing.T) {
	c := config.DefaultConfig()
	d := c.Decision

	if d.Enabled {
		t.Error("decision.enabled must default to false: enabling it loads model assets")
	}
	if d.Required {
		t.Error("decision.required must default to false (degraded mode, not fail-startup)")
	}
	if d.Mode != config.DecisionModeShadow {
		t.Errorf("decision.mode default = %q, want %q", d.Mode, config.DecisionModeShadow)
	}
	if d.Provider != "embedded" {
		t.Errorf("decision.provider default = %q, want embedded", d.Provider)
	}
	if d.ModelPath != "/etc/elida/models/injection" {
		t.Errorf("decision.model_path default = %q", d.ModelPath)
	}
	if d.Endpoint != "" {
		t.Errorf("decision.endpoint must default to unset, got %q", d.Endpoint)
	}
	if d.ThresholdSet != "v1" {
		t.Errorf("decision.threshold_set default = %q, want v1", d.ThresholdSet)
	}
	if d.ElevatedThreshold != 0.3 {
		t.Errorf("decision.elevated_threshold default = %v, want 0.3", d.ElevatedThreshold)
	}
	if d.InlineTimeout != 50*time.Millisecond {
		t.Errorf("decision.inline_timeout default = %v, want 50ms", d.InlineTimeout)
	}
	if d.MaxConcurrency != 4 {
		t.Errorf("decision.max_concurrency default = %d, want 4 (inline 3 / async 1)", d.MaxConcurrency)
	}
	if d.InlineQueueWait != 0 {
		t.Errorf("decision.inline_queue_wait default = %v, want 0 (Phase 1 is zero-queue)", d.InlineQueueWait)
	}
	if d.MaxInlineTokens != 128 {
		t.Errorf("decision.max_inline_tokens default = %d, want 128", d.MaxInlineTokens)
	}
	if d.MaxInlineWindows != 1 {
		t.Errorf("decision.max_inline_windows default = %d, want 1", d.MaxInlineWindows)
	}
	if d.MaxAsyncWindows != 8 {
		t.Errorf("decision.max_async_windows default = %d, want 8", d.MaxAsyncWindows)
	}
	if d.AsyncQueueSize != 100 {
		t.Errorf("decision.async_queue_size default = %d, want 100", d.AsyncQueueSize)
	}

	a := d.InlineAdmission
	if !a.UntrustedToolResults || !a.EncodedOrObfuscated || !a.WeakInjectionSignal || !a.ElevatedSessionRisk {
		t.Errorf("inline_admission targeted triggers must default true, got %+v", a)
	}
	if a.BroadStrictMode {
		t.Error("inline_admission.broad_strict_mode must default false")
	}

	p := d.Preprocessing
	if p.MaxInputBytes != 262144 || p.MaxAnalysisBytes != 524288 || p.MaxRepresentations != 8 ||
		p.MaxDecodeDepth != 2 || p.MaxExpansionRatio != 4 {
		t.Errorf("preprocessing defaults drifted: %+v", p)
	}
}

func TestDecisionConfig_EnvOverrides(t *testing.T) {
	// Only restart-required keys get env overrides; runtime-editable keys
	// come from settings.yaml / the dashboard (see Task 3).
	t.Setenv("ELIDA_DECISION_ENABLED", "true")
	t.Setenv("ELIDA_DECISION_REQUIRED", "true")
	t.Setenv("ELIDA_DECISION_PROVIDER", "systemone")
	t.Setenv("ELIDA_DECISION_MODEL_PATH", "/opt/models/inj")
	t.Setenv("ELIDA_DECISION_ENDPOINT", "https://decide.internal/v1/score")

	path := writeTempConfig(t, "listen: \":8080\"\nbackend: \"https://api.example.com\"\n")
	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if !c.Decision.Enabled || !c.Decision.Required {
		t.Errorf("env did not enable decision: %+v", c.Decision)
	}
	if c.Decision.Provider != "systemone" {
		t.Errorf("provider = %q, want systemone", c.Decision.Provider)
	}
	if c.Decision.ModelPath != "/opt/models/inj" {
		t.Errorf("model_path = %q", c.Decision.ModelPath)
	}
	if c.Decision.Endpoint != "https://decide.internal/v1/score" {
		t.Errorf("endpoint = %q", c.Decision.Endpoint)
	}
	// Runtime-editable keys must NOT be env-overridable.
	if os.Getenv("ELIDA_DECISION_MODE") == "" && c.Decision.Mode != config.DecisionModeShadow {
		t.Errorf("mode should stay at its default, got %q", c.Decision.Mode)
	}
}

func TestDecisionConfig_Validation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*config.DecisionConfig)
		wantErr string // substring of the ValidationError field
	}{
		{"negative inline_timeout", func(d *config.DecisionConfig) { d.InlineTimeout = -1 }, "decision.inline_timeout"},
		{"zero inline_timeout", func(d *config.DecisionConfig) { d.InlineTimeout = 0 }, "decision.inline_timeout"},
		{"nonzero inline queue wait", func(d *config.DecisionConfig) { d.InlineQueueWait = time.Millisecond }, "decision.inline_queue_wait"},
		{"negative inline queue wait", func(d *config.DecisionConfig) { d.InlineQueueWait = -1 }, "decision.inline_queue_wait"},
		{"zero concurrency", func(d *config.DecisionConfig) { d.MaxConcurrency = 0 }, "decision.max_concurrency"},
		{"concurrency over ceiling", func(d *config.DecisionConfig) { d.MaxConcurrency = config.MaxConcurrencyCeiling + 1 }, "decision.max_concurrency"},
		{"bad mode", func(d *config.DecisionConfig) { d.Mode = "paranoid" }, "decision.mode"},
		{"bad provider", func(d *config.DecisionConfig) { d.Provider = "openai" }, "decision.provider"},
		{"elevated below zero", func(d *config.DecisionConfig) { d.ElevatedThreshold = -0.1 }, "decision.elevated_threshold"},
		{"elevated above one", func(d *config.DecisionConfig) { d.ElevatedThreshold = 1.1 }, "decision.elevated_threshold"},
		{"empty threshold_set", func(d *config.DecisionConfig) { d.ThresholdSet = "" }, "decision.threshold_set"},
		{"empty model_path for embedded", func(d *config.DecisionConfig) { d.ModelPath = "" }, "decision.model_path"},
		{"systemone without endpoint", func(d *config.DecisionConfig) { d.Provider = "systemone"; d.Endpoint = "" }, "decision.endpoint"},
		{"decode depth over ceiling", func(d *config.DecisionConfig) { d.Preprocessing.MaxDecodeDepth = config.MaxDecodeDepthCeiling + 1 }, "decision.preprocessing.max_decode_depth"},
		{"queue size over ceiling", func(d *config.DecisionConfig) { d.AsyncQueueSize = config.AsyncQueueSizeCeiling + 1 }, "decision.async_queue_size"},
		{"zero queue size", func(d *config.DecisionConfig) { d.AsyncQueueSize = 0 }, "decision.async_queue_size"},
		{"zero representations", func(d *config.DecisionConfig) { d.Preprocessing.MaxRepresentations = 0 }, "decision.preprocessing.max_representations"},
		{"expansion ratio below one", func(d *config.DecisionConfig) { d.Preprocessing.MaxExpansionRatio = 0 }, "decision.preprocessing.max_expansion_ratio"},
		{"analysis bytes below input bytes", func(d *config.DecisionConfig) {
			d.Preprocessing.MaxInputBytes = 1000
			d.Preprocessing.MaxAnalysisBytes = 999
		}, "decision.preprocessing.max_analysis_bytes"},
		{"inline windows zero", func(d *config.DecisionConfig) { d.MaxInlineWindows = 0 }, "decision.max_inline_windows"},
		{"inline tokens zero", func(d *config.DecisionConfig) { d.MaxInlineTokens = 0 }, "decision.max_inline_tokens"},
		{"async windows negative", func(d *config.DecisionConfig) { d.MaxAsyncWindows = -1 }, "decision.max_async_windows"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := config.DefaultConfig()
			c.Listen = ":8080"
			c.Backend = "https://api.example.com"
			c.Decision.Enabled = true
			tc.mutate(&c.Decision)

			res := c.Validate()
			if res.Valid {
				t.Fatalf("expected validation failure for %q", tc.name)
			}
			var fields []string
			for _, e := range res.Errors {
				fields = append(fields, e.Field)
			}
			found := false
			for _, f := range fields {
				if f == tc.wantErr {
					found = true
				}
			}
			if !found {
				t.Fatalf("want error on field %q, got fields %v", tc.wantErr, fields)
			}
		})
	}
}

func TestDecisionConfig_DisabledSkipsValidation(t *testing.T) {
	// A disabled feature must not be able to fail startup over its own
	// settings: `decision.enabled: false` adds no hot-path cost and loads
	// nothing, so an absurd value is inert.
	c := config.DefaultConfig()
	c.Listen = ":8080"
	c.Backend = "https://api.example.com"
	c.Decision.Enabled = false
	c.Decision.MaxConcurrency = -5
	c.Decision.Mode = "paranoid"

	res := c.Validate()
	for _, e := range res.Errors {
		if strings.HasPrefix(e.Field, "decision.") {
			t.Fatalf("disabled decision config must not error: %s: %s", e.Field, e.Message)
		}
	}
}

func TestDecisionConfig_PolicyModeCapWarns(t *testing.T) {
	c := config.DefaultConfig()
	c.Listen = ":8080"
	c.Backend = "https://api.example.com"
	c.Policy.Enabled = true
	c.Policy.Mode = "audit"
	c.Decision.Enabled = true
	c.Decision.Mode = config.DecisionModeEnforce

	res := c.Validate()
	if !res.Valid {
		t.Fatalf("the cap is a warning, not an error: %+v", res.Errors)
	}
	var found bool
	for _, w := range res.Warnings {
		if w.Field == "decision.mode" && strings.Contains(w.Message, "policy.mode") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a decision.mode warning about the policy.mode cap, got %+v", res.Warnings)
	}
}

// TestSampleConfig_DecisionMatchesDefaults keeps the shipped sample config
// honest. configs/elida.yaml documents the decision surface for operators and
// is what the Docker image runs, so a default changed in code but not in the
// sample (or the reverse) would hand operators a file that quietly overrides
// the value the code considers safe.
func TestSampleConfig_DecisionMatchesDefaults(t *testing.T) {
	cfg, err := config.Load("../../configs/elida.yaml")
	if err != nil {
		t.Fatalf("load configs/elida.yaml: %v", err)
	}
	want := config.DefaultConfig().Decision
	if !reflect.DeepEqual(cfg.Decision, want) {
		t.Fatalf("configs/elida.yaml decision block drifted from DefaultConfig:\n got %+v\nwant %+v", cfg.Decision, want)
	}
}

// writeTempConfig writes a YAML config to a temp file and returns its path.
func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/elida.yaml"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}
