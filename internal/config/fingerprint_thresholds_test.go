package config

import "testing"

func TestFingerprintConfigDefaultThresholds(t *testing.T) {
	cfg := DefaultConfig()
	want := FingerprintThresholds{Minor: 3.3, Notable: 4.1, Anomalous: 5.0, Severe: 6.0}
	if cfg.Fingerprint.Thresholds != want {
		t.Errorf("default thresholds = %+v, want %+v", cfg.Fingerprint.Thresholds, want)
	}
}

func TestFingerprintConfigThresholdsMustIncrease(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Fingerprint.Thresholds = FingerprintThresholds{Minor: 5.0, Notable: 4.1, Anomalous: 3.0, Severe: 2.0}
	result := cfg.Validate()
	if result.Valid {
		t.Fatal("expected validation failure for non-increasing thresholds")
	}
	if !hasFieldError(result, "fingerprint.thresholds") {
		t.Errorf("expected error on fingerprint.thresholds field, got %+v", result.Errors)
	}
}

func TestFingerprintConfigThresholdsMustBePositive(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Fingerprint.Thresholds = FingerprintThresholds{Minor: -1, Notable: 4.1, Anomalous: 5.0, Severe: 6.0}
	result := cfg.Validate()
	if result.Valid {
		t.Fatal("expected validation failure for non-positive threshold")
	}
	if !hasFieldError(result, "fingerprint.thresholds") {
		t.Errorf("expected error on fingerprint.thresholds field, got %+v", result.Errors)
	}
}

func TestFingerprintConfigZeroValueThresholdsAllowed(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Fingerprint.Thresholds = FingerprintThresholds{}
	result := cfg.Validate()
	if hasFieldError(result, "fingerprint.thresholds") {
		t.Errorf("zero-value thresholds (fall back to defaults) should not fail validation, got %+v", result.Errors)
	}
}

func hasFieldError(result *ValidationResult, field string) bool {
	for _, e := range result.Errors {
		if e.Field == field {
			return true
		}
	}
	return false
}
