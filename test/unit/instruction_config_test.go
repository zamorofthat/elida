package unit

import (
	"testing"

	"elida/internal/config"
)

func TestDefaultConfigHasInstructionIntegrity(t *testing.T) {
	cfg := config.DefaultConfig()
	ii := cfg.Policy.InstructionIntegrity
	if ii.Enabled {
		t.Error("instruction integrity should be disabled by default")
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
