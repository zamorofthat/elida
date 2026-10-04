package unit

import (
	"strings"
	"testing"
	"time"

	"elida/internal/config"
)

func ptr[T any](v T) *T { return &v }

func TestDecisionSettings_DefaultsMirrorConfig(t *testing.T) {
	c := config.DefaultConfig()
	store, err := config.NewSettingsStoreFromConfig(c, t.TempDir())
	if err != nil {
		t.Fatalf("NewSettingsStoreFromConfig: %v", err)
	}
	d := store.GetDefaults().Decision
	if d.Mode == nil || *d.Mode != config.DecisionModeShadow {
		t.Fatalf("default mode not mirrored from Config: %+v", d.Mode)
	}
	if d.ElevatedThreshold == nil || *d.ElevatedThreshold != 0.3 {
		t.Fatalf("default elevated_threshold not mirrored: %+v", d.ElevatedThreshold)
	}
	if d.MaxConcurrency == nil || *d.MaxConcurrency != 2 {
		t.Fatalf("default max_concurrency not mirrored: %+v", d.MaxConcurrency)
	}
	if d.Preprocessing == nil || d.Preprocessing.MaxDecodeDepth == nil || *d.Preprocessing.MaxDecodeDepth != 2 {
		t.Fatalf("default preprocessing not mirrored: %+v", d.Preprocessing)
	}
}

func TestDecisionSettings_MergeOverridesOnlySetFields(t *testing.T) {
	dir := t.TempDir()
	c := config.DefaultConfig()
	store, err := config.NewSettingsStoreFromConfig(c, dir)
	if err != nil {
		t.Fatalf("NewSettingsStoreFromConfig: %v", err)
	}

	local := store.GetLocal()
	local.Decision = config.DecisionSettings{
		Mode:           ptr(config.DecisionModeAudit),
		MaxConcurrency: ptr(4),
		Preprocessing: &config.DecisionPreprocessingSettings{
			MaxDecodeDepth: ptr(3),
		},
	}
	if err := store.SaveLocal(local); err != nil {
		t.Fatalf("SaveLocal: %v", err)
	}

	m := store.GetMerged().Decision
	if m.Mode == nil || *m.Mode != config.DecisionModeAudit {
		t.Fatalf("mode not overridden: %+v", m.Mode)
	}
	if m.MaxConcurrency == nil || *m.MaxConcurrency != 4 {
		t.Fatalf("max_concurrency not overridden: %+v", m.MaxConcurrency)
	}
	if m.Preprocessing == nil || m.Preprocessing.MaxDecodeDepth == nil || *m.Preprocessing.MaxDecodeDepth != 3 {
		t.Fatalf("max_decode_depth not overridden: %+v", m.Preprocessing)
	}
	// Unset fields keep their defaults rather than becoming zero.
	if m.ElevatedThreshold == nil || *m.ElevatedThreshold != 0.3 {
		t.Fatalf("unset elevated_threshold should keep its default, got %+v", m.ElevatedThreshold)
	}
	if m.Preprocessing.MaxRepresentations == nil || *m.Preprocessing.MaxRepresentations != 8 {
		t.Fatalf("unset max_representations should keep its default, got %+v", m.Preprocessing.MaxRepresentations)
	}
	if m.AsyncQueueSize == nil || *m.AsyncQueueSize != 100 {
		t.Fatalf("unset async_queue_size should keep its default, got %+v", m.AsyncQueueSize)
	}
}

func TestDecisionSettings_ApplyToEnforcesCeilings(t *testing.T) {
	base := config.DefaultConfig().Decision

	s := config.DecisionSettings{
		MaxConcurrency: ptr(config.MaxConcurrencyCeiling + 1),
		AsyncQueueSize: ptr(config.AsyncQueueSizeCeiling + 1),
		Preprocessing: &config.DecisionPreprocessingSettings{
			MaxDecodeDepth: ptr(config.MaxDecodeDepthCeiling + 1),
		},
	}
	got, rejected := s.ApplyTo(base, "enforce")

	if got.MaxConcurrency != base.MaxConcurrency {
		t.Errorf("over-ceiling max_concurrency must be refused, got %d", got.MaxConcurrency)
	}
	if got.AsyncQueueSize != base.AsyncQueueSize {
		t.Errorf("over-ceiling async_queue_size must be refused, got %d", got.AsyncQueueSize)
	}
	if got.Preprocessing.MaxDecodeDepth != base.Preprocessing.MaxDecodeDepth {
		t.Errorf("over-ceiling max_decode_depth must be refused, got %d", got.Preprocessing.MaxDecodeDepth)
	}
	if len(rejected) != 3 {
		t.Fatalf("expected 3 rejection reasons, got %d: %v", len(rejected), rejected)
	}
	for _, r := range rejected {
		if !strings.Contains(r, "ceiling") {
			t.Errorf("rejection reason should explain the ceiling: %q", r)
		}
	}
}

func TestDecisionSettings_ApplyToTightensFreely(t *testing.T) {
	base := config.DefaultConfig().Decision
	s := config.DecisionSettings{
		MaxConcurrency:  ptr(1),
		InlineTimeoutMs: ptr(25),
		Preprocessing:   &config.DecisionPreprocessingSettings{MaxDecodeDepth: ptr(1)},
	}
	got, rejected := s.ApplyTo(base, "enforce")
	if len(rejected) != 0 {
		t.Fatalf("tightening a bound must be allowed, got rejections %v", rejected)
	}
	if got.MaxConcurrency != 1 {
		t.Errorf("max_concurrency = %d, want 1", got.MaxConcurrency)
	}
	if got.InlineTimeout != 25*time.Millisecond {
		t.Errorf("inline_timeout = %v, want 25ms", got.InlineTimeout)
	}
	if got.Preprocessing.MaxDecodeDepth != 1 {
		t.Errorf("max_decode_depth = %d, want 1", got.Preprocessing.MaxDecodeDepth)
	}
	if got.InlineQueueWait != 0 {
		t.Errorf("inline_queue_wait must stay 0, got %v", got.InlineQueueWait)
	}
}

func TestDecisionSettings_ApplyToRefusesEnforceUnderPolicyAudit(t *testing.T) {
	base := config.DefaultConfig().Decision
	s := config.DecisionSettings{Mode: ptr(config.DecisionModeEnforce)}

	got, rejected := s.ApplyTo(base, "audit")
	if got.Mode != config.DecisionModeAudit {
		t.Fatalf("enforce under policy.mode audit must cap to audit, got %q", got.Mode)
	}
	if len(rejected) != 1 || !strings.Contains(rejected[0], "policy.mode") {
		t.Fatalf("expected one policy.mode rejection, got %v", rejected)
	}

	got, rejected = s.ApplyTo(base, "enforce")
	if got.Mode != config.DecisionModeEnforce {
		t.Fatalf("enforce under policy.mode enforce should stand, got %q", got.Mode)
	}
	if len(rejected) != 0 {
		t.Fatalf("expected no rejections, got %v", rejected)
	}
}

func TestDecisionSettings_ApplyToRejectsBadMode(t *testing.T) {
	base := config.DefaultConfig().Decision
	s := config.DecisionSettings{Mode: ptr("paranoid")}
	got, rejected := s.ApplyTo(base, "enforce")
	if got.Mode != base.Mode {
		t.Fatalf("unknown mode must be refused, got %q", got.Mode)
	}
	if len(rejected) != 1 || !strings.Contains(rejected[0], "paranoid") {
		t.Fatalf("expected a rejection naming the bad mode, got %v", rejected)
	}
}

func TestDecisionSettings_ApplyToRejectsAnalysisBelowInput(t *testing.T) {
	base := config.DefaultConfig().Decision // MaxInputBytes=262144, MaxAnalysisBytes=524288

	// (a) Raising only max_input_bytes above the current max_analysis_bytes
	// must be refused, leaving both fields at their pre-edit values.
	s := config.DecisionSettings{
		Preprocessing: &config.DecisionPreprocessingSettings{
			MaxInputBytes: ptr(1048576), // > 524288 (default max_analysis_bytes); still under its own ceiling
		},
	}
	got, rejected := s.ApplyTo(base, "enforce")
	if len(rejected) != 1 {
		t.Fatalf("expected 1 rejection, got %d: %v", len(rejected), rejected)
	}
	if !strings.Contains(rejected[0], "max_analysis_bytes") || !strings.Contains(rejected[0], "max_input_bytes") {
		t.Errorf("rejection should name both keys: %q", rejected[0])
	}
	if got.Preprocessing.MaxInputBytes != base.Preprocessing.MaxInputBytes {
		t.Errorf("max_input_bytes must revert to %d, got %d", base.Preprocessing.MaxInputBytes, got.Preprocessing.MaxInputBytes)
	}
	if got.Preprocessing.MaxAnalysisBytes != base.Preprocessing.MaxAnalysisBytes {
		t.Errorf("max_analysis_bytes must stay %d, got %d", base.Preprocessing.MaxAnalysisBytes, got.Preprocessing.MaxAnalysisBytes)
	}

	// (b) Raising both consistently in one edit is accepted.
	s2 := config.DecisionSettings{
		Preprocessing: &config.DecisionPreprocessingSettings{
			MaxInputBytes:    ptr(600000),
			MaxAnalysisBytes: ptr(700000),
		},
	}
	got2, rejected2 := s2.ApplyTo(base, "enforce")
	if len(rejected2) != 0 {
		t.Fatalf("expected no rejections, got %v", rejected2)
	}
	if got2.Preprocessing.MaxInputBytes != 600000 {
		t.Errorf("max_input_bytes = %d, want 600000", got2.Preprocessing.MaxInputBytes)
	}
	if got2.Preprocessing.MaxAnalysisBytes != 700000 {
		t.Errorf("max_analysis_bytes = %d, want 700000", got2.Preprocessing.MaxAnalysisBytes)
	}

	// The accepted result must pass the same validation decision.Validate
	// enforces on a full Config: ApplyTo and validateDecision must agree on
	// what is acceptable.
	c := config.DefaultConfig()
	c.Listen = ":8080"
	c.Backend = "https://api.example.com"
	c.Decision = got2
	c.Decision.Enabled = true
	res := c.Validate()
	if !res.Valid {
		t.Fatalf("ApplyTo's accepted result failed Validate(): %+v", res.Errors)
	}
}

func TestDecisionSettings_AbsentSectionLeavesConfigUntouched(t *testing.T) {
	dir := t.TempDir()
	c := config.DefaultConfig()
	store, err := config.NewSettingsStoreFromConfig(c, dir)
	if err != nil {
		t.Fatalf("NewSettingsStoreFromConfig: %v", err)
	}

	// A settings document that touches policy and capture, but never
	// mentions decision at all (no `decision:` key, Decision left at its
	// Go zero value).
	local := store.GetLocal()
	local.Policy.Enabled = ptr(true)
	local.Capture.Mode = ptr("all")
	if err := store.SaveLocal(local); err != nil {
		t.Fatalf("SaveLocal: %v", err)
	}

	base := c.Decision
	merged := store.GetMerged().Decision

	got, rejected := merged.ApplyTo(base, "enforce")
	if len(rejected) != 0 {
		t.Fatalf("expected no rejections when decision section is absent, got %v", rejected)
	}

	if got.Mode != base.Mode {
		t.Errorf("Mode changed: got %q, want %q", got.Mode, base.Mode)
	}
	if got.ThresholdSet != base.ThresholdSet {
		t.Errorf("ThresholdSet changed: got %q, want %q", got.ThresholdSet, base.ThresholdSet)
	}
	if got.ElevatedThreshold != base.ElevatedThreshold {
		t.Errorf("ElevatedThreshold changed: got %v, want %v", got.ElevatedThreshold, base.ElevatedThreshold)
	}
	if got.InlineTimeout != base.InlineTimeout {
		t.Errorf("InlineTimeout changed: got %v, want %v", got.InlineTimeout, base.InlineTimeout)
	}
	if got.MaxConcurrency != base.MaxConcurrency {
		t.Errorf("MaxConcurrency changed: got %d, want %d", got.MaxConcurrency, base.MaxConcurrency)
	}
	if got.MaxInlineTokens != base.MaxInlineTokens {
		t.Errorf("MaxInlineTokens changed: got %d, want %d", got.MaxInlineTokens, base.MaxInlineTokens)
	}
	if got.MaxInlineWindows != base.MaxInlineWindows {
		t.Errorf("MaxInlineWindows changed: got %d, want %d", got.MaxInlineWindows, base.MaxInlineWindows)
	}
	if got.MaxAsyncWindows != base.MaxAsyncWindows {
		t.Errorf("MaxAsyncWindows changed: got %d, want %d", got.MaxAsyncWindows, base.MaxAsyncWindows)
	}
	if got.AsyncQueueSize != base.AsyncQueueSize {
		t.Errorf("AsyncQueueSize changed: got %d, want %d", got.AsyncQueueSize, base.AsyncQueueSize)
	}
	if got.InlineQueueWait != 0 {
		t.Errorf("InlineQueueWait must stay 0, got %v", got.InlineQueueWait)
	}
	if got.Preprocessing != base.Preprocessing {
		t.Errorf("Preprocessing changed: got %+v, want %+v", got.Preprocessing, base.Preprocessing)
	}

	// Whole-struct equality as a belt-and-suspenders check: every field of
	// DecisionConfig is comparable, so this catches anything the explicit
	// checks above missed.
	if got != base {
		t.Fatalf("absent decision section must leave config fully untouched:\n got  %+v\n base %+v", got, base)
	}
}
