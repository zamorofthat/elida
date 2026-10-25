package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/correlation"
	"elida/internal/policy"
	"elida/internal/session"
)

func heavyTaxonomy() []correlation.ToolCategoryRule {
	// Eight NFA-heavy patterns of about 200 instructions each: valid, and
	// over the soft limit (WarnTaxonomyInstructions).
	pats := make([]string, correlation.MaxPatternsPerEntry)
	for i := range pats {
		pats[i] = "[a-h]{200}" + string(rune('0'+i))
	}
	return []correlation.ToolCategoryRule{{Category: "heavy", Tools: []string{"*"}, ArgumentPatterns: pats}}
}

// Task 4 binding note 3: a heavy taxonomy starts, with one WARN that names
// the instruction count and the estimated worst-case cost per call.
func TestCorrelationTaxonomy_HeavyTaxonomyWarnsOnceAtStartup(t *testing.T) {
	logs := captureLogs(t)
	cfg := correlationTestConfig(t)
	cfg.Correlation.ToolCategories = heavyTaxonomy()
	a := &app{cfg: cfg}
	if err := a.setupPolicyEngine(); err != nil {
		t.Fatalf("a valid heavy taxonomy must start: %v", err)
	}
	out := logs.String()
	if got := strings.Count(out, "correlation tool taxonomy is expensive to evaluate"); got != 1 {
		t.Fatalf("want one taxonomy WARN, got %d:\n%s", got, out)
	}
	for _, want := range []string{"level=WARN", "instructions=", "estimated_worst_case_per_call=", "field=correlation.tool_categories"} {
		if !strings.Contains(out, want) {
			t.Errorf("taxonomy WARN lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "[a-h]{200}") {
		t.Errorf("the WARN must not echo patterns:\n%s", out)
	}
}

func TestCorrelationTaxonomy_LightTaxonomyDoesNotWarn(t *testing.T) {
	logs := captureLogs(t)
	cfg := correlationTestConfig(t)
	cfg.Correlation.ToolCategories = []correlation.ToolCategoryRule{{Category: "shell", Tools: []string{"bash"}}}
	a := &app{cfg: cfg}
	if err := a.setupPolicyEngine(); err != nil {
		t.Fatalf("setupPolicyEngine: %v", err)
	}
	if strings.Contains(logs.String(), "taxonomy is expensive") {
		t.Fatalf("a light taxonomy must not warn:\n%s", logs.String())
	}
}

// An invalid taxonomy fails startup like any other invalid correlation
// block (wiring review M-1), and fails config loading from YAML.
func TestCorrelationTaxonomy_InvalidTaxonomyFailsStartup(t *testing.T) {
	quietLogs(t)
	for name, mutate := range map[string]func(*config.Config){
		"bad category": func(c *config.Config) {
			c.Correlation.ToolCategories = []correlation.ToolCategoryRule{{Category: "Bad-Name", Tools: []string{"x"}}}
		},
		"no tools": func(c *config.Config) {
			c.Correlation.ToolCategories = []correlation.ToolCategoryRule{{Category: "ok"}}
		},
		"bad pattern": func(c *config.Config) {
			c.Correlation.ToolCategories = []correlation.ToolCategoryRule{{Category: "ok", Tools: []string{"x"}, ArgumentPatterns: []string{"("}}}
		},
		"args too big":  func(c *config.Config) { c.Correlation.MaxArgumentBytes = correlation.MaxArgumentBytesCeiling + 1 },
		"negative args": func(c *config.Config) { c.Correlation.MaxArgumentBytes = -1 },
	} {
		cfg := correlationTestConfig(t)
		mutate(cfg)
		a := &app{cfg: cfg}
		err := a.setupPolicyEngine()
		if err == nil || !strings.Contains(err.Error(), "correlation") {
			t.Errorf("%s: want a startup error naming correlation, got %v", name, err)
		}
		if a.policyEngine != nil {
			t.Errorf("%s: a failed startup must not leave a policy engine", name)
		}
	}
}

func TestCorrelationTaxonomy_InvalidTaxonomyInYAMLFailsLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "elida.yaml")
	body := "correlation:\n  enabled: true\n  tool_categories:\n    - category: shell\n      tools: ['[z-a]']\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil || !strings.Contains(err.Error(), "correlation.tool_categories[0].tools[0]") {
		t.Fatalf("an invalid glob must fail config loading, got %v", err)
	}
}

// The taxonomy reaches the policy engine from YAML: tool calls through the
// engine main builds complete a sequence rule.
func TestCorrelationTaxonomy_YAMLTaxonomyReachesThePolicyEngine(t *testing.T) {
	quietLogs(t)
	path := filepath.Join(t.TempDir(), "elida.yaml")
	body := `policy:
  enabled: true
correlation:
  enabled: true
  max_argument_bytes: 4096
  rules:
    - name: cred_then_send
      shape: sequence
      sequence: [credential_discovery, external_transmission]
      window: 10m
  tool_categories:
    - category: credential_discovery
      tools: [read_file]
      argument_patterns: ['\.aws/credentials']
    - category: external_transmission
      tools: ['http_*']
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.Correlation.MaxArgumentBytes != 4096 || len(cfg.Correlation.ToolCategories) != 2 {
		t.Fatalf("taxonomy not loaded: %+v", cfg.Correlation.TaxonomyConfig)
	}
	a := &app{cfg: cfg}
	if err := a.setupPolicyEngine(); err != nil {
		t.Fatalf("setupPolicyEngine: %v", err)
	}
	e := a.policyEngine
	e.EvaluateToolCalls("sess-yaml", []policy.ToolCall{{ID: "a", Name: "read_file", Arguments: `{"path":"~/.aws/credentials"}`}})
	e.EvaluateToolCalls("sess-yaml", []policy.ToolCall{{ID: "b", Name: "http_post", Arguments: `{}`}})
	ctx := t.Context()
	if err := e.FlushToolCalls(ctx); err != nil {
		t.Fatal(err)
	}
	fs := e.GetFlaggedSession("sess-yaml")
	if fs == nil {
		t.Fatal("want a correlation match from the YAML taxonomy")
	}
	var found bool
	for _, v := range fs.Violations {
		if v.RuleName == "correlation.cred_then_send" && v.EvidenceOnly {
			found = true
		}
		for _, s := range []string{v.Description, v.MatchedText, v.SourceContent} {
			if strings.Contains(s, ".aws") {
				t.Fatalf("a match must carry no tool arguments: %q", s)
			}
		}
	}
	if !found {
		t.Fatalf("want an audit correlation.cred_then_send match, got %+v", fs.Violations)
	}
}

// slowTaxonomy is a valid taxonomy that takes on the order of 100 ms or
// more per 64 KiB call (NFA-heavy patterns over every argument byte).
func slowTaxonomy() correlation.TaxonomyConfig {
	pat := `(a|b|c|d|e|f|g|h)+[0-9]{2,8}(x|y|z)*`
	var cats []correlation.ToolCategoryRule
	for i := 0; i < 8; i++ {
		cats = append(cats, correlation.ToolCategoryRule{
			Category: "slow_" + string(rune('a'+i)), Tools: []string{"*"},
			ArgumentPatterns: []string{pat, pat + "q", pat + "r", pat + "s", pat + "t", pat + "u", pat + "v", pat + "w"},
		})
	}
	return correlation.TaxonomyConfig{MaxArgumentBytes: correlation.MaxArgumentBytesCeiling, ToolCategories: cats}
}

// Review I-1: shutdown closes the tool lane within its own sub-deadline
// (min(10 s, half the remaining budget)): a lane stuck on a slow taxonomy
// cannot consume the session drain's and telemetry flush's share, and the
// closed lane refuses batches a detached async scan sends afterwards.
func TestCorrelationTaxonomy_ShutdownClosesTheToolLaneWithinItsShare(t *testing.T) {
	quietLogs(t)
	cfg := correlationTestConfig(t)
	cfg.Correlation.TaxonomyConfig = slowTaxonomy()
	cfg.Correlation.Rules = []correlation.Rule{{
		Name: "slow_twice", Shape: correlation.ShapeRepeated, Category: "slow_a", Count: 2, Window: time.Minute,
	}}
	cfg.ShutdownTimeout = 2 * time.Second
	a := &app{cfg: cfg, proxyServer: &http.Server{}, manager: session.NewManager(session.NewMemoryStore(), time.Minute)}
	if err := a.setupPolicyEngine(); err != nil {
		t.Fatalf("setupPolicyEngine: %v", err)
	}
	arg := strings.Repeat("abcdefgh1", correlation.MaxArgumentBytesCeiling/9+1)
	calls := make([]policy.ToolCall, policy.MaxToolCallsPerBatch)
	for i := range calls {
		calls[i] = policy.ToolCall{Name: "bash", Arguments: arg}
	}
	for i := 0; i < 2; i++ {
		a.policyEngine.EvaluateToolCalls("sess-slow", calls)
	}

	start := time.Now()
	a.shutdown(func() {})
	if d := time.Since(start); d > 1600*time.Millisecond {
		t.Fatalf("shutdown took %v with a stuck tool lane; the lane's share is at most half of %v", d, cfg.ShutdownTimeout)
	}
	a.policyEngine.EvaluateToolCalls("sess-late", calls[:1])
	st, _ := a.policyEngine.Stats()["correlation"].(policy.CorrelationStats)
	if st.ToolBatchesRefused != 1 {
		t.Fatalf("shutdown must close the tool lane: a late batch is refused, got %+v", st)
	}
}

func TestCorrelationTaxonomy_ToolLaneDrainShare(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	ctx, cancel2 := drainContext(parent, toolLaneDrainLimit)
	defer cancel2()
	dl, _ := ctx.Deadline()
	if left := time.Until(dl); left > 2100*time.Millisecond || left < 1500*time.Millisecond {
		t.Fatalf("tool-lane share of a 4 s budget = %v, want about 2 s", left)
	}
	ctx3, cancel3 := drainContext(context.Background(), toolLaneDrainLimit)
	defer cancel3()
	if dl, ok := ctx3.Deadline(); !ok || time.Until(dl) > toolLaneDrainLimit {
		t.Fatalf("with no parent deadline the 10 s limit applies, got %v %v", dl, ok)
	}
}
