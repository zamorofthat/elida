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
