package unit

import (
	"testing"

	"elida/internal/config"
)

func TestDefaultConfigHasInstructionIntegrity(t *testing.T) {
	cfg := config.DefaultConfig()
	ii := cfg.Policy.InstructionIntegrity
	if !ii.Enabled {
		t.Error("instruction integrity should be enabled by default")
	}
	if len(ii.TrackedTypes) == 0 {
		t.Error("expected default tracked types")
	}
	if len(ii.Rules) == 0 {
		t.Error("expected default instruction rules")
	}
	if ii.AsyncQueueSize != 100 {
		t.Errorf("async_queue_size = %d, want 100", ii.AsyncQueueSize)
	}
	if ii.ShapeConfidenceThreshold != 0.7 {
		t.Errorf("shape_confidence_threshold = %f, want 0.7", ii.ShapeConfidenceThreshold)
	}
}

func TestSettingsMergeInstructionIntegrity(t *testing.T) {
	cfg := config.DefaultConfig()
	store, err := config.NewSettingsStoreFromConfig(cfg, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	merged := store.GetMerged()
	if merged.Policy.InstructionIntegrity == nil {
		t.Fatal("expected instruction integrity in merged settings")
	}
	if !*merged.Policy.InstructionIntegrity.Enabled {
		t.Error("expected enabled by default")
	}
	if !*merged.Policy.InstructionIntegrity.ShapeDetection {
		t.Error("expected shape detection enabled by default")
	}
	if *merged.Policy.InstructionIntegrity.ShapeConfidenceThreshold != 0.7 {
		t.Errorf("threshold = %f, want 0.7", *merged.Policy.InstructionIntegrity.ShapeConfidenceThreshold)
	}
	if len(merged.Policy.InstructionIntegrity.TrackedTypes) != 5 {
		t.Errorf("tracked_types count = %d, want 5", len(merged.Policy.InstructionIntegrity.TrackedTypes))
	}
}

func TestSettingsOverrideInstructionIntegrity(t *testing.T) {
	cfg := config.DefaultConfig()
	store, err := config.NewSettingsStoreFromConfig(cfg, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// Override: disable instruction integrity
	disabled := false
	newThreshold := 0.5
	err = store.SaveLocal(config.Settings{
		Policy: config.PolicySettings{
			InstructionIntegrity: &config.InstructionIntegritySettings{
				Enabled:                  &disabled,
				ShapeConfidenceThreshold: &newThreshold,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	merged := store.GetMerged()
	if merged.Policy.InstructionIntegrity == nil {
		t.Fatal("expected instruction integrity in merged settings")
	}
	if *merged.Policy.InstructionIntegrity.Enabled {
		t.Error("expected disabled after override")
	}
	if *merged.Policy.InstructionIntegrity.ShapeConfidenceThreshold != 0.5 {
		t.Errorf("threshold = %f, want 0.5", *merged.Policy.InstructionIntegrity.ShapeConfidenceThreshold)
	}
	// Shape detection should still be true (not overridden)
	if !*merged.Policy.InstructionIntegrity.ShapeDetection {
		t.Error("shape detection should remain true (not overridden)")
	}
}

func TestSettingsDiffInstructionIntegrity(t *testing.T) {
	cfg := config.DefaultConfig()
	store, err := config.NewSettingsStoreFromConfig(cfg, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// No local overrides — diff should be empty
	diff := store.GetDiff()
	for k := range diff {
		if k == "policy.instruction_integrity.enabled" {
			t.Error("expected no diff for instruction_integrity.enabled before override")
		}
	}

	// Override enabled
	disabled := false
	err = store.SaveLocal(config.Settings{
		Policy: config.PolicySettings{
			InstructionIntegrity: &config.InstructionIntegritySettings{
				Enabled: &disabled,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	diff = store.GetDiff()
	d, ok := diff["policy.instruction_integrity.enabled"]
	if !ok {
		t.Fatal("expected diff for policy.instruction_integrity.enabled")
	}
	if d.DefaultValue != true {
		t.Errorf("default = %v, want true", d.DefaultValue)
	}
	if d.LocalValue != false {
		t.Errorf("local = %v, want false", d.LocalValue)
	}
}
