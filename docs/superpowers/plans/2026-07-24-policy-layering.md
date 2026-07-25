# Policy Layering Implementation Plan (Branch 1 of 4: `fix/policy-layering`)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix integration-feedback items #1 (custom rules can't override preset rules), #2 (audit mode still blocks via the risk ladder), and #3 (no coding-agent-safe preset) in the ELIDA policy layer.

**Architecture:** All changes live in `internal/config/config.go` (preset merge, new preset, new rule field), `internal/policy/policy.go` (audit-mode ladder clamp, shadow-rule scoring exclusion), and `cmd/elida/main.go` (one field in the config→engine conversion). Semantics follow "local overrides default" layering (Splunk/Cribl-style): a same-named custom rule replaces the preset rule.

**Tech Stack:** Go 1.26, stdlib testing (in-package tests, matching `internal/config/security_test.go`).

**Spec:** `docs/superpowers/specs/2026-07-24-integration-feedback-fixes-design.md` (Branch 1 section).

## Global Constraints

- Branch: `fix/policy-layering` (already created; spec committed as `57ac030`).
- Commit messages: conventional-commit style, **no** `Co-Authored-By` trailer (user preference).
- Defaults must preserve current behavior except where current behavior *is* the bug: (a) audit mode no longer blocks via the ladder; (b) a same-named custom rule now replaces the preset rule instead of coexisting.
- TDD: every behavior change lands with a test written first and observed failing.
- Run `go build ./...` and `go test ./...` before each commit.

---

### Task 1: Local-overrides-default rule merge + `disabled_rules` (#1)

**Files:**
- Modify: `internal/config/config.go` — `PolicyConfig` struct (line ~117), `ApplyPolicyPreset()` (line ~988)
- Test: `internal/config/policy_preset_test.go` (create)

**Interfaces:**
- Consumes: existing `PolicyRule`, `getMinimalPreset()`, `getStandardPreset()`, `getStrictPreset()`, `getMCPPreset()`.
- Produces: `PolicyConfig.DisabledRules []string` (yaml `disabled_rules`); `ApplyPolicyPreset()` with replace-by-name merge semantics. Task 4 relies on this merge behavior; no signature changes.

- [ ] **Step 1: Write the failing tests**

Create `internal/config/policy_preset_test.go`:

```go
package config

import "testing"

func findRule(rules []PolicyRule, name string) *PolicyRule {
	for i := range rules {
		if rules[i].Name == name {
			return &rules[i]
		}
	}
	return nil
}

func countRule(rules []PolicyRule, name string) int {
	n := 0
	for i := range rules {
		if rules[i].Name == name {
			n++
		}
	}
	return n
}

// Feedback #1: a same-named custom rule must REPLACE the preset rule
// (local-overrides-default), not coexist with it.
func TestCustomRuleOverridesPresetRule(t *testing.T) {
	cfg := &Config{}
	cfg.Policy.Preset = "standard"
	cfg.Policy.Rules = []PolicyRule{
		{Name: "shell_execution", Type: "content_match", Target: "response",
			Patterns: []string{"bash\\s+-c\\s+"}, Severity: "warning", Action: "flag",
			Description: "softened for trusted coding agent"},
	}

	cfg.ApplyPolicyPreset()

	if got := countRule(cfg.Policy.Rules, "shell_execution"); got != 1 {
		t.Fatalf("shell_execution appears %d times, want exactly 1 (custom replaces preset)", got)
	}
	r := findRule(cfg.Policy.Rules, "shell_execution")
	if r.Action != "flag" {
		t.Errorf("shell_execution action = %q, want %q (custom rule must win)", r.Action, "flag")
	}
}

// Custom rules with unique names are still appended alongside preset rules.
func TestUniqueCustomRulesStillAppended(t *testing.T) {
	cfg := &Config{}
	cfg.Policy.Preset = "minimal"
	cfg.Policy.Rules = []PolicyRule{
		{Name: "my_custom_rule", Type: "content_match", Target: "request",
			Patterns: []string{"foo"}, Severity: "info", Action: "flag"},
	}

	cfg.ApplyPolicyPreset()

	if findRule(cfg.Policy.Rules, "my_custom_rule") == nil {
		t.Error("unique custom rule was dropped by the merge")
	}
	if findRule(cfg.Policy.Rules, "rate_limit_high") == nil {
		t.Error("preset rule rate_limit_high missing after merge")
	}
}

// Feedback #1: disabled_rules drops rules by name without redefining them.
func TestDisabledRulesRemovesPresetRule(t *testing.T) {
	cfg := &Config{}
	cfg.Policy.Preset = "standard"
	cfg.Policy.DisabledRules = []string{"destructive_file_ops", "compound_anomaly"}

	cfg.ApplyPolicyPreset()

	for _, name := range []string{"destructive_file_ops", "compound_anomaly"} {
		if findRule(cfg.Policy.Rules, name) != nil {
			t.Errorf("rule %q present after being listed in disabled_rules", name)
		}
	}
	// Sibling rules survive.
	if findRule(cfg.Policy.Rules, "destructive_file_ops_request") == nil {
		t.Error("destructive_file_ops_request was wrongly removed")
	}
}

// disabled_rules also applies to generated circuit-breaker rules and works
// with no preset set.
func TestDisabledRulesAppliesToCircuitBreakerAndNoPreset(t *testing.T) {
	cfg := &Config{}
	cfg.Policy.Preset = "minimal"
	cfg.Policy.CircuitBreaker.Enabled = true
	cfg.Policy.CircuitBreaker.MaxToolFanout = 30
	cfg.Policy.DisabledRules = []string{"circuit_breaker_tool_fanout"}

	cfg.ApplyPolicyPreset()

	if findRule(cfg.Policy.Rules, "circuit_breaker_tool_fanout") != nil {
		t.Error("generated circuit_breaker_tool_fanout present despite disabled_rules")
	}

	cfg2 := &Config{}
	cfg2.Policy.Rules = []PolicyRule{
		{Name: "noisy_rule", Type: "content_match", Patterns: []string{"x"}, Action: "flag"},
	}
	cfg2.Policy.DisabledRules = []string{"noisy_rule"}
	cfg2.ApplyPolicyPreset()
	if findRule(cfg2.Policy.Rules, "noisy_rule") != nil {
		t.Error("disabled_rules ignored when no preset is set")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run 'TestCustomRuleOverridesPresetRule|TestUniqueCustomRulesStillAppended|TestDisabledRulesRemovesPresetRule|TestDisabledRulesAppliesToCircuitBreakerAndNoPreset' -v`

Expected: `TestCustomRuleOverridesPresetRule` FAILS (`shell_execution appears 2 times`); `TestDisabledRules*` FAIL (rules still present). `TestUniqueCustomRulesStillAppended` may already pass — that's fine, it pins existing behavior.

- [ ] **Step 3: Implement the merge**

In `internal/config/config.go`, add to `PolicyConfig` (after the `Rules` field, line ~123):

```go
	DisabledRules        []string                   `yaml:"disabled_rules"`   // Rule names to drop after merge (preset, custom, or generated)
```

Replace the body of `ApplyPolicyPreset()` (line ~989). The current early-`return`s for empty/unknown preset must NOT skip the disabled_rules filter, so restructure:

```go
// ApplyPolicyPreset applies a policy preset with local-overrides-default
// layering: a custom rule with the same name as a preset rule REPLACES the
// preset rule (like Splunk/Cribl local vs default configs). Rules named in
// policy.disabled_rules are dropped after the merge — this also covers
// generated circuit-breaker rules.
func (c *Config) ApplyPolicyPreset() {
	var presetRules []PolicyRule
	switch c.Policy.Preset {
	case "":
		// no preset — custom rules only
	case "minimal":
		presetRules = getMinimalPreset()
	case "standard":
		presetRules = getStandardPreset()
	case "strict":
		presetRules = getStrictPreset()
	case "mcp":
		presetRules = getMCPPreset()
	default:
		slog.Warn("unknown policy preset, using custom rules only", "preset", c.Policy.Preset)
	}

	// Local overrides default: same-named custom rule replaces the preset rule.
	customNames := make(map[string]bool, len(c.Policy.Rules))
	for _, r := range c.Policy.Rules {
		customNames[r.Name] = true
	}
	var overridden []string
	merged := make([]PolicyRule, 0, len(presetRules)+len(c.Policy.Rules))
	for _, pr := range presetRules {
		if customNames[pr.Name] {
			overridden = append(overridden, pr.Name)
			continue
		}
		merged = append(merged, pr)
	}
	merged = append(merged, c.Policy.Rules...)
	c.Policy.Rules = merged

	// Generate rules from circuit breaker config (if enabled)
	// ... EXISTING circuit-breaker block moves here UNCHANGED (the
	// `if c.Policy.CircuitBreaker.Enabled { ... }` block currently at
	// lines ~1012-1044) ...

	// Drop rules named in disabled_rules (applies to preset, custom, and
	// generated rules alike).
	if len(c.Policy.DisabledRules) > 0 {
		disabled := make(map[string]bool, len(c.Policy.DisabledRules))
		for _, name := range c.Policy.DisabledRules {
			disabled[name] = true
		}
		kept := c.Policy.Rules[:0]
		var dropped []string
		for _, r := range c.Policy.Rules {
			if disabled[r.Name] {
				dropped = append(dropped, r.Name)
				continue
			}
			kept = append(kept, r)
		}
		c.Policy.Rules = kept
		if len(dropped) > 0 {
			slog.Info("policy rules disabled by config", "rules", dropped)
		}
	}

	if len(overridden) > 0 {
		slog.Info("preset rules overridden by custom rules", "preset", c.Policy.Preset, "rules", overridden)
	}
}
```

Notes for the implementer:
- The circuit-breaker block is moved verbatim, not rewritten. Do not change its rule contents.
- `log/slog` is already imported in this file (check the import block; add it if not).
- Behavior change vs. today: with an unknown or empty preset, circuit-breaker rules are now generated too (previously the early `return` skipped them). This is a bug-adjacent cleanup — a `circuit_breaker.enabled: true` config with no preset silently generated nothing. Keep it; it's covered by `TestDisabledRulesAppliesToCircuitBreakerAndNoPreset`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -v` (whole package — catches regressions in `security_test.go` too)
Expected: all PASS.

- [ ] **Step 5: Build and run the full suite**

Run: `go build ./... && go test ./...`
Expected: PASS everywhere. If `cmd/elida` or `internal/control` fail to compile, you changed a signature you shouldn't have — `ApplyPolicyPreset()` keeps its exact signature.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/policy_preset_test.go
git commit -m "fix(policy): custom rules override same-named preset rules; add disabled_rules

Local-overrides-default layering (feedback #1): a custom rule with the
same name as a preset rule now replaces it instead of coexisting, and
policy.disabled_rules drops any named rule after the merge."
```

---

### Task 2: Audit mode gates the risk ladder (#2)

**Files:**
- Modify: `internal/policy/policy.go` — `determineRiskAction()` (line ~1212)
- Test: `internal/policy/risk_ladder_test.go` (create)

**Interfaces:**
- Consumes: `Engine.auditMode` (set in `NewEngine` line ~277 and `ReloadConfig` line ~371), `RiskLadderAction` constants, `AddExternalRiskPoints()` (line ~1435), `ShouldBlockByRisk()` (line ~1484).
- Produces: no signature changes. In audit mode `determineRiskAction` never returns `throttle`/`block`/`terminate` — all ladder consumers (`ShouldBlockByRisk`, `ShouldTerminateByRisk`, `ShouldThrottle`, the persisted `CurrentAction`) inherit the clamp because they all read what it computed.

- [ ] **Step 1: Write the failing tests**

Create `internal/policy/risk_ladder_test.go`:

```go
package policy

import "testing"

func ladderEngine(mode string) *Engine {
	return NewEngine(Config{
		Enabled: true,
		Mode:    mode,
		RiskLadder: RiskLadderConfig{
			Enabled: true, // default thresholds: 5 warn, 15 throttle, 30 block, 50 terminate
		},
	})
}

// Feedback #2: "dry run" must not block. In audit mode the ladder may
// observe/warn but never throttle, block, or terminate — this is the exact
// "HTTP 403: Session risk score too high in mode: audit" bug.
func TestAuditModeLadderNeverBlocks(t *testing.T) {
	e := ladderEngine("audit")
	e.AddExternalRiskPoints("sess-audit", 100, "test") // far past every threshold

	if e.ShouldBlockByRisk("sess-audit") {
		t.Error("audit mode: ShouldBlockByRisk = true, want false")
	}
	if e.ShouldTerminateByRisk("sess-audit") {
		t.Error("audit mode: ShouldTerminateByRisk = true, want false")
	}
	if throttled, _ := e.ShouldThrottle("sess-audit"); throttled {
		t.Error("audit mode: ShouldThrottle = true, want false")
	}
}

// The score itself must still be computed and visible in audit mode —
// audit means observe, not off.
func TestAuditModeStillComputesRiskScore(t *testing.T) {
	e := ladderEngine("audit")
	e.AddExternalRiskPoints("sess-audit", 40, "test")

	score, action, _ := e.GetSessionRiskScore("sess-audit")
	if score <= 0 {
		t.Errorf("audit mode: risk score = %v, want > 0 (score must still accumulate)", score)
	}
	if action == string(ActionBlock) || action == string(ActionTerminate) || action == string(ActionThrottle) {
		t.Errorf("audit mode: current action = %q, want observe/warn only", action)
	}
}

// Enforce mode is unchanged: same points, ladder blocks.
func TestEnforceModeLadderStillBlocks(t *testing.T) {
	e := ladderEngine("enforce")
	e.AddExternalRiskPoints("sess-enforce", 100, "test")

	if !e.ShouldBlockByRisk("sess-enforce") {
		t.Error("enforce mode: ShouldBlockByRisk = false, want true")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/policy/ -run 'TestAuditMode|TestEnforceMode' -v`
Expected: `TestAuditModeLadderNeverBlocks` and `TestAuditModeStillComputesRiskScore` FAIL (block/terminate actions returned in audit mode). `TestEnforceModeLadderStillBlocks` PASSES (pins existing behavior).

- [ ] **Step 3: Implement the clamp**

In `internal/policy/policy.go`, `determineRiskAction()` (line ~1212) — add the clamp before the return. Callers already hold `e.mu`, and `auditMode` is only mutated under the same lock, so reading it here is race-free:

```go
// determineRiskAction determines the appropriate action based on risk score
func (e *Engine) determineRiskAction(score float64) (string, int) {
	action := string(ActionObserve)
	throttleRate := 0

	// Find the highest threshold that the score exceeds
	for _, threshold := range e.riskThresholds {
		if score >= threshold.Score {
			action = string(threshold.Action)
			if threshold.Action == ActionThrottle {
				throttleRate = threshold.ThrottleRate
			}
		}
	}

	// mode: audit is a dry run — the ladder may observe and warn but must
	// never act. Without this, accumulated flags escalate to 403s while
	// every individual rule is audit-only (feedback #2).
	if e.auditMode {
		switch action {
		case string(ActionThrottle), string(ActionBlock), string(ActionTerminate):
			slog.Info("risk ladder action suppressed (audit mode)",
				"computed_action", action, "risk_score", score)
			action = string(ActionWarn)
			throttleRate = 0
		}
	}

	return action, throttleRate
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/policy/ -v`
Expected: all PASS.

- [ ] **Step 5: Build and full suite**

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/policy/policy.go internal/policy/risk_ladder_test.go
git commit -m "fix(policy): audit mode gates the risk ladder to observe/warn

mode: audit previously dry-ran individual rule actions while the risk
ladder still escalated accumulated flags to throttle/block/terminate,
producing 403s in a supposed dry run (feedback #2). The score is still
computed and logged; it just can't act."
```

---

### Task 3: Shadow rules — flag and capture, zero ladder contribution

**Files:**
- Modify: `internal/config/config.go` — `PolicyRule` struct (line ~203), end of `ApplyPolicyPreset()` (normalization)
- Modify: `internal/policy/policy.go` — `Rule` struct (line ~68), `Engine` struct (line ~230s), `NewEngine` (line ~271), `ReloadConfig` (line ~342), `recordViolations` (line ~1026)
- Modify: `cmd/elida/main.go` — config→policy rule conversion (line ~620)
- Test: `internal/policy/shadow_test.go` (create), plus one test appended to `internal/config/policy_preset_test.go`

**Interfaces:**
- Consumes: Task 1's merge (shadow rules in presets must be overridable by name).
- Produces: `config.PolicyRule.Shadow bool` (yaml `shadow`), `policy.Rule.Shadow bool`. Semantics: a shadow rule's action is forced to `flag` at config load, and its violations are recorded on the session (visible in `/control/flagged`) but excluded from `ViolationEvents`, so they contribute **0** to `calculateRiskScore`. Task 4's preset sets `Shadow: true` on its statistical/content rules.

- [ ] **Step 1: Write the failing tests**

Create `internal/policy/shadow_test.go`:

```go
package policy

import (
	"testing"
	"time"
)

// Shadow rules flag and capture but contribute zero to the risk ladder
// (feedback #3: statistical/PII rules are noise on coding-agent traffic —
// they must not drive escalation).
func TestShadowRuleViolationsDoNotFeedRiskLadder(t *testing.T) {
	e := NewEngine(Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []Rule{
			{Name: "shadow_shell", Type: RuleTypeContentMatch, Target: TargetRequest,
				Patterns: []string{"sudo\\s+rm"}, Severity: SeverityCritical,
				Action: "flag", Shadow: true},
		},
		RiskLadder: RiskLadderConfig{Enabled: true},
	})

	// Record many critical shadow violations — enough that, if they fed the
	// ladder, the session would be far past the block threshold (30).
	for i := 0; i < 50; i++ {
		e.recordViolations("sess-shadow", []Violation{{
			RuleName:  "shadow_shell",
			Severity:  SeverityCritical,
			Action:    "flag",
			Timestamp: time.Now(),
		}})
	}

	// Still flagged and visible…
	if !e.IsFlagged("sess-shadow") {
		t.Fatal("shadow violations must still flag the session")
	}
	// …but the ladder never moved.
	score, _, _ := e.GetSessionRiskScore("sess-shadow")
	if score != 0 {
		t.Errorf("risk score = %v, want 0 (shadow rules contribute nothing)", score)
	}
	if e.ShouldBlockByRisk("sess-shadow") {
		t.Error("ShouldBlockByRisk = true from shadow-only violations, want false")
	}
}

// Non-shadow rules are unaffected.
func TestNonShadowRulesStillFeedRiskLadder(t *testing.T) {
	e := NewEngine(Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []Rule{
			{Name: "real_rule", Type: RuleTypeContentMatch, Target: TargetRequest,
				Patterns: []string{"x"}, Severity: SeverityCritical, Action: "flag"},
		},
		RiskLadder: RiskLadderConfig{Enabled: true},
	})

	for i := 0; i < 50; i++ {
		e.recordViolations("sess-real", []Violation{{
			RuleName:  "real_rule",
			Severity:  SeverityCritical,
			Action:    "flag",
			Timestamp: time.Now(),
		}})
	}

	score, _, _ := e.GetSessionRiskScore("sess-real")
	if score <= 0 {
		t.Errorf("risk score = %v, want > 0 for non-shadow violations", score)
	}
}
```

Note: if `RuleTypeContentMatch`, `TargetRequest`, or `SeverityCritical` are named differently, use the actual constants from `policy.go` (see `RuleType`/`RuleTarget`/`Severity` declarations near the top of the file) — do not invent new ones.

Append to `internal/config/policy_preset_test.go`:

```go
// A shadow rule can never block: its action is normalized to flag at load.
func TestShadowRuleActionNormalizedToFlag(t *testing.T) {
	cfg := &Config{}
	cfg.Policy.Rules = []PolicyRule{
		{Name: "shadowed", Type: "content_match", Patterns: []string{"x"},
			Action: "block", Shadow: true},
	}
	cfg.ApplyPolicyPreset()
	r := findRule(cfg.Policy.Rules, "shadowed")
	if r == nil || r.Action != "flag" {
		t.Fatalf("shadow rule action = %v, want flag", r)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/policy/ -run TestShadow -v && go test ./internal/config/ -run TestShadowRuleActionNormalizedToFlag -v`
Expected: compile FAIL (`Shadow` field doesn't exist) — that's the failing state for structural changes.

- [ ] **Step 3: Implement**

3a. `internal/config/config.go` — add to `PolicyRule` (after `Action`, line ~213):

```go
	Shadow         bool     `yaml:"shadow"` // Flag + capture only: never enforces, contributes 0 to the risk ladder
```

3b. Same file, at the very end of `ApplyPolicyPreset()` (after the disabled_rules filter from Task 1):

```go
	// Shadow rules observe only — normalize their action to flag so a
	// misconfigured shadow+block rule can never enforce.
	for i := range c.Policy.Rules {
		if c.Policy.Rules[i].Shadow {
			c.Policy.Rules[i].Action = "flag"
		}
	}
```

3c. `internal/policy/policy.go` — add to `Rule` (after `Action`, line ~78):

```go
	Shadow         bool       `yaml:"shadow" json:"shadow,omitempty"` // Flag + capture only: excluded from risk scoring
```

3d. Same file — add to the `Engine` struct (near `riskLadderEnabled`, line ~241):

```go
	shadowRules       map[string]bool // rule name -> shadow (excluded from risk scoring)
```

In `NewEngine` (inside the `e := &Engine{...}` literal, line ~290s) add `shadowRules: shadowRulesFrom(cfg.Rules),` and in `ReloadConfig` (line ~371 area, alongside `e.auditMode = ...`) add `e.shadowRules = shadowRulesFrom(cfg.Rules)`. Add the helper near `NewEngine`:

```go
// shadowRulesFrom indexes which rule names are shadow (observe-only).
func shadowRulesFrom(rules []Rule) map[string]bool {
	m := make(map[string]bool)
	for _, r := range rules {
		if r.Shadow {
			m[r.Name] = true
		}
	}
	return m
}
```

3e. Same file, in `recordViolations` (line ~1057), guard the event append — the violation list, counts, and capture behavior stay untouched; only the decay-scored event stream skips shadow rules:

```go
		// Record event for decay calculation — shadow rules are visible in
		// the violation list but contribute nothing to the risk score.
		if !e.shadowRules[v.RuleName] {
			flagged.ViolationEvents = append(flagged.ViolationEvents, ViolationEvent{
				RuleName:   v.RuleName,
				Severity:   v.Severity,
				SourceRole: v.SourceRole,
				Timestamp:  v.Timestamp,
			})
		}
```

(Wrap the existing append exactly as shown; keep any existing fields the `ViolationEvent{...}` literal already sets.)

3f. `cmd/elida/main.go` — in the conversion loop (line ~622), add to the `policy.Rule{...}` literal:

```go
			Shadow:         r.Shadow,
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/policy/ -v && go test ./internal/config/ -v`
Expected: all PASS.

- [ ] **Step 5: Build and full suite**

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/policy/policy.go cmd/elida/main.go \
        internal/policy/shadow_test.go internal/config/policy_preset_test.go
git commit -m "feat(policy): shadow rules — flag and capture without feeding the risk ladder

A rule with shadow: true is normalized to action: flag and its violations
are excluded from risk scoring. Groundwork for the coding-agent preset
(feedback #3): statistical/content heuristics stay observable without
driving escalation."
```

---

### Task 4: `coding-agent` preset + anomaly-message rewording (#3)

**Files:**
- Modify: `internal/config/config.go` — `ApplyPolicyPreset()` switch, new `getCodingAgentPreset()` (after `getMinimalPreset()`, line ~1053), `compound_anomaly` description (line ~1068), `Preset` field comment (line ~122)
- Test: `internal/config/policy_preset_test.go` (append)

**Interfaces:**
- Consumes: Task 1's merge (users tune this preset by redefining rules by name), Task 3's `Shadow` field.
- Produces: preset name `"coding-agent"`. Enforced structural rules: `block_dangerous_tools`, `dangerous_tool_arguments` (downgraded terminate→block), `tool_credential_access`, minimal rate/count rules. Shadow rules: the shell/agency/injection content heuristics + `rate_anomaly`/`compound_anomaly` + `pii_patterns`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/policy_preset_test.go`:

```go
// Feedback #3: the coding-agent preset enforces deterministic structural
// rules and shadows the content/statistical heuristics that false-fire on
// legitimate agent traffic.
func TestCodingAgentPreset(t *testing.T) {
	cfg := &Config{}
	cfg.Policy.Preset = "coding-agent"
	cfg.ApplyPolicyPreset()

	if len(cfg.Policy.Rules) == 0 {
		t.Fatal("coding-agent preset produced no rules")
	}

	// Structural rules enforce.
	enforced := map[string]string{
		"block_dangerous_tools":    "block",
		"dangerous_tool_arguments": "block", // block, not terminate: a tool-arg pattern is not a confirmed breach
		"tool_credential_access":   "block",
	}
	for name, wantAction := range enforced {
		r := findRule(cfg.Policy.Rules, name)
		if r == nil {
			t.Errorf("enforced rule %q missing", name)
			continue
		}
		if r.Shadow {
			t.Errorf("rule %q is shadow, must enforce", name)
		}
		if r.Action != wantAction {
			t.Errorf("rule %q action = %q, want %q", name, r.Action, wantAction)
		}
	}

	// Content/statistical heuristics are shadow — the exact rules that
	// broke real agent turns under `standard` (bash -c, sudo, rm -rf,
	// curl|sh) plus the measured-noisy anomaly rules.
	shadowed := []string{
		"shell_execution", "privilege_escalation", "destructive_file_ops",
		"network_exfiltration", "rate_anomaly", "compound_anomaly",
	}
	for _, name := range shadowed {
		r := findRule(cfg.Policy.Rules, name)
		if r == nil {
			t.Errorf("shadow rule %q missing", name)
			continue
		}
		if !r.Shadow {
			t.Errorf("rule %q must be shadow in coding-agent preset", name)
		}
		if r.Action != "flag" {
			t.Errorf("rule %q action = %q, want flag", name, r.Action)
		}
	}

	// No rule in this preset may terminate — a coding agent's own output
	// must never kill the session.
	for _, r := range cfg.Policy.Rules {
		if r.Action == "terminate" {
			t.Errorf("rule %q has action terminate; coding-agent preset must not terminate", r.Name)
		}
	}
}

// Overriding a coding-agent rule by name still works (Task 1 semantics).
func TestCodingAgentPresetIsOverridable(t *testing.T) {
	cfg := &Config{}
	cfg.Policy.Preset = "coding-agent"
	cfg.Policy.Rules = []PolicyRule{
		{Name: "shell_execution", Type: "content_match", Target: "response",
			Patterns: []string{"bash\\s+-c\\s+"}, Severity: "critical", Action: "block",
			Description: "this deployment wants shell blocked"},
	}
	cfg.ApplyPolicyPreset()

	r := findRule(cfg.Policy.Rules, "shell_execution")
	if r == nil || r.Action != "block" || r.Shadow {
		t.Fatalf("override failed: got %+v, want enforcing block rule", r)
	}
}

// Feedback #3 (bonus): a bare rate/entropy heuristic must not be worded as
// a confirmed exfiltration.
func TestAnomalyDescriptionsNotAlarmist(t *testing.T) {
	for _, preset := range [][]PolicyRule{getStandardPreset(), getCodingAgentPreset()} {
		if r := findRule(preset, "compound_anomaly"); r != nil {
			if containsFold(r.Description, "exfiltration") {
				t.Errorf("compound_anomaly description still says 'exfiltration': %q", r.Description)
			}
		}
	}
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}
```

Add `"strings"` to the test file's imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run 'TestCodingAgent|TestAnomalyDescriptions' -v`
Expected: compile FAIL (`getCodingAgentPreset` undefined).

- [ ] **Step 3: Implement the preset**

3a. In `ApplyPolicyPreset()`'s switch, add:

```go
	case "coding-agent":
		presetRules = getCodingAgentPreset()
```

3b. Update the `Preset` field comment (line ~122) to `// minimal, standard, strict, mcp, or coding-agent`.

3c. Reword `compound_anomaly` in `getStandardPreset()` (line ~1068): change the description to `"ANOMALY: Sustained high-rate + high-entropy burst (elevated rate/entropy signal)"`.

3d. Add `getCodingAgentPreset()` after `getMinimalPreset()`:

```go
// getCodingAgentPreset returns a policy tuned for trusted coding agents
// (Claude Code, Hermes, Cursor): deterministic structural rules ENFORCE,
// content/statistical heuristics run in SHADOW — flagged and captured but
// never blocking and never feeding the risk ladder. Rationale: coding
// agents legitimately emit bash -c / sudo / rm -rf / curl|sh in their own
// output, and their rapid tool loops look like high-rate high-entropy
// bursts to anomaly detectors (integration feedback #3).
func getCodingAgentPreset() []PolicyRule {
	return []PolicyRule{
		// ---- Enforced: structural, can't false-fire on agent output ----
		{Name: "block_dangerous_tools", Type: "tool_blocked", Target: "response", Patterns: []string{
			"exec_*", "shell_*", "rm_*", "sudo_*", "eval_*",
		}, Severity: "critical", Action: "block", Description: "LLM07: Block dangerous tool calls"},
		{Name: "dangerous_tool_arguments", Type: "tool_argument_pattern", Target: "response", Patterns: []string{
			"rm\\s+-rf\\s+/",
			"chmod\\s+777\\s+/",
			"curl\\s+[^|]*\\|\\s*(ba)?sh",
		}, Severity: "critical", Action: "block", Description: "LLM08: Dangerous patterns in tool arguments"},
		{Name: "tool_credential_access", Type: "content_match", Target: "request", Patterns: []string{
			"\"function\"\\s*:\\s*\"(get|read|fetch)_(secret|credential|password|key)\"",
			"\"name\"\\s*:\\s*\"(vault_read|secret_manager|get_api_key)\"",
		}, Severity: "critical", Action: "block", Description: "LLM07: Tool requests credential access"},

		// ---- Enforced: generous runaway limits (coding sessions are long) ----
		{Name: "rate_limit_high", Type: "requests_per_minute", Threshold: 120, Severity: "critical", Action: "block", Description: "FIREWALL: Request rate exceeds 120/min"},
		{Name: "high_request_count", Type: "request_count", Threshold: 2000, Severity: "warning", Action: "flag", Description: "FIREWALL: Session exceeded 2000 requests"},

		// ---- Shadow: heuristics that false-fire on legitimate agent output ----
		{Name: "shell_execution", Type: "content_match", Target: "both", Shadow: true, Patterns: []string{
			"(run|execute)\\s+(a\\s+)?(bash|shell|terminal)\\s+(command|script)",
			"bash\\s+-c\\s+",
			"/bin/(ba)?sh\\s+",
		}, Severity: "warning", Action: "flag", Description: "LLM08: Shell execution pattern (shadow)"},
		{Name: "privilege_escalation", Type: "content_match", Target: "both", Shadow: true, Patterns: []string{
			"sudo\\s+(rm|chmod|chown|kill|bash|sh|python|perl|ruby|apt|yum|dnf|pip|npm|make|gcc|curl|wget)\\b",
			"(run|execute)\\s+(this\\s+)?(command\\s+)?(as|with)\\s+root",
			"(get|gain|obtain)\\s+(root|admin|superuser)\\s+(access|privileges|permissions)",
		}, Severity: "warning", Action: "flag", Description: "LLM08: Privilege escalation pattern (shadow)"},
		{Name: "destructive_file_ops", Type: "content_match", Target: "both", Shadow: true, Patterns: []string{
			"rm\\s+(-rf?|--recursive)\\s+/",
			"rm\\s+-rf\\s+\\*",
			"(delete|remove|wipe)\\s+all\\s+(files|data|everything)",
		}, Severity: "warning", Action: "flag", Description: "LLM08: Destructive file operation (shadow)"},
		{Name: "network_exfiltration", Type: "content_match", Target: "both", Shadow: true, Patterns: []string{
			"curl\\s+[^|]*\\|\\s*(ba)?sh",
			"wget\\s+[^|]*\\|\\s*(ba)?sh",
			"reverse\\s+shell",
		}, Severity: "warning", Action: "flag", Description: "LLM08: Piped-download pattern (shadow)"},
		{Name: "prompt_injection_ignore", Type: "content_match", Target: "both", Shadow: true, Patterns: []string{
			"ignore\\s+(all\\s+)?(previous|prior|above|your)\\s+(instructions|prompts|rules)",
			"disregard\\s+(all\\s+)?(your\\s+)?(previous|prior|system)\\s+(instructions|prompts)",
			"forget\\s+(all\\s+)?(previous|prior|your)\\s+(instructions|training|rules)",
		}, Severity: "warning", Action: "flag", Description: "LLM01: Prompt injection pattern (shadow)"},

		// ---- Shadow: statistical anomalies (measured noise on agent tool loops) ----
		{Name: "rate_anomaly", Type: "rate_anomaly", Shadow: true, Severity: "warning", Action: "flag",
			ThresholdFloat: 0.01, MinSamples: 10, Description: "ANOMALY: Request rate statistically abnormal (shadow)"},
		{Name: "compound_anomaly", Type: "compound_anomaly", Shadow: true, Severity: "warning", Action: "flag",
			ThresholdFloat: 0.15, MinSamples: 5, Description: "ANOMALY: Sustained high-rate + high-entropy burst (elevated rate/entropy signal, shadow)"},
	}
}
```

Recommended companion circuit-breaker settings go in docs (Task 5), not the preset — the preset function can only return rules, and silently mutating `CircuitBreaker` config from a preset would surprise users.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: all PASS.

- [ ] **Step 5: Build and full suite**

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/policy_preset_test.go
git commit -m "feat(policy): coding-agent preset — enforce structural rules, shadow heuristics

New preset for trusted coding agents (feedback #3): dangerous tool
names/args and credential-access tools block; shell/sudo/rm/curl|sh
content patterns and rate/entropy anomalies run in shadow. Nothing in
the preset can terminate a session, and the compound_anomaly wording no
longer claims 'exfiltration' for a bare statistical signal."
```

---

### Task 5: Documentation + changelog

**Files:**
- Modify: `docs/policy-rules-reference.md` (new sections), `docs/configuration.md` (policy section), `CHANGELOG.md` (top)

**Interfaces:**
- Consumes: everything shipped in Tasks 1–4. No code.

- [ ] **Step 1: Document the new behavior**

In `docs/policy-rules-reference.md`, add a "Rule layering" section near the top (follow the file's existing heading style):

```markdown
## Rule layering: presets, overrides, and disabled rules

Preset and custom rules merge with **local-overrides-default** semantics
(like Splunk or Cribl configs): a custom rule with the same `name` as a
preset rule **replaces** it. To soften one `standard` rule without
rebuilding the policy:

​```yaml
policy:
  preset: standard
  rules:
    - name: shell_execution        # replaces the preset rule of this name
      type: content_match
      target: response
      patterns: ["bash\\s+-c\\s+"]
      severity: warning
      action: flag                 # preset had block
​```

To drop rules entirely, list them by name — this also works on generated
circuit-breaker rules:

​```yaml
policy:
  preset: standard
  disabled_rules: [destructive_file_ops, compound_anomaly]
​```

### Shadow rules

`shadow: true` makes a rule observe-only: it flags and captures, its
action is forced to `flag`, and it contributes **nothing** to the risk
ladder. Use it to keep noisy heuristics visible without letting them
escalate.

### Audit mode and the risk ladder

`mode: audit` is a true dry run: rule actions don't enforce **and** the
risk ladder is clamped to observe/warn. Scores still accumulate and are
visible in the dashboard, but audit mode can never throttle, block, or
terminate.

### The `coding-agent` preset

Tuned for trusted coding agents (Claude Code, Hermes, Cursor), whose
legitimate output contains `bash -c`, `sudo`, `rm -rf`, `curl | sh` and
whose tool loops look like high-rate bursts to anomaly detectors:

- **Enforced:** dangerous tool names (`exec_*`, `shell_*`, `rm_*`,
  `sudo_*`, `eval_*`), dangerous tool arguments, credential-access tool
  calls, generous rate limits (120 req/min).
- **Shadow:** shell/privilege/destructive/exfil content patterns, prompt
  injection heuristics, `rate_anomaly`, `compound_anomaly`.
- Nothing in the preset terminates a session.

Recommended companion settings:

​```yaml
policy:
  preset: coding-agent
  circuit_breaker:
    enabled: true
    max_tool_fanout: 100   # agents legitimately expose 30+ tools
​```
```

In `docs/configuration.md`, extend the policy section's option list with `disabled_rules`, `shadow`, and the `coding-agent` preset value (match the file's existing table/format — read it first).

- [ ] **Step 2: Update CHANGELOG.md**

Add under a new "Unreleased" heading at the top (match existing entry style):

```markdown
## [Unreleased]

### Fixed
- Custom policy rules now **replace** same-named preset rules
  (local-overrides-default) instead of coexisting with them. (#feedback-1)
- `mode: audit` is now a true dry run: the risk ladder is clamped to
  observe/warn and can no longer block or terminate. (#feedback-2)

### Added
- `policy.disabled_rules`: drop preset, custom, or generated rules by name.
- `shadow: true` on a rule: flag + capture without feeding the risk ladder.
- `coding-agent` policy preset: structural rules enforce, content and
  statistical heuristics run in shadow. (#feedback-3)

### Changed
- `compound_anomaly` description no longer claims an "exfiltration
  pattern" for a bare rate/entropy signal.
```

- [ ] **Step 3: Verify docs claims against the shipped code**

Run: `go test ./... && go build ./...`
Expected: PASS (no code changed in this task; this is the pre-commit gate).
Re-read each doc claim and confirm it matches a test from Tasks 1–4.

- [ ] **Step 4: Commit**

```bash
git add docs/policy-rules-reference.md docs/configuration.md CHANGELOG.md
git commit -m "docs(policy): document rule layering, shadow rules, audit-mode ladder, coding-agent preset"
```

---

## Final verification (whole branch)

- [ ] `go build ./... && go test ./...` — everything green.
- [ ] `go vet ./...` — clean.
- [ ] Manual smoke: `policy.preset: coding-agent`, `mode: enforce` config; confirm at startup the engine logs the rule count and no rule carries `terminate`.
- [ ] Use superpowers:verification-before-completion before claiming done; then superpowers:finishing-a-development-branch to choose merge/PR.

## Out of scope (later branches per the spec)

- Branch 2 `fix/session-identity-auth` (#4, #4b)
- Branch 3 `fix/backend-config` (#8, #9)
- Branch 4 `fix/redaction` (#10)

Each gets its own plan document when its turn comes.
