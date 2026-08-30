package config

import (
	"strings"
	"testing"
)

// T12: telemetry.capture_content="all" only emits content that the proxy
// buffered, and the buffer is populated for every session only when
// storage.capture_mode="all". Setting capture_content=all while capture_mode is
// flagged_only (or storage disabled) silently emits empty content — Validate
// must surface a non-fatal warning naming both keys.
func TestValidate_CaptureContentAllWithoutCaptureModeAll_Warns(t *testing.T) {
	c := defaults()
	c.Telemetry.CaptureContent = "all"
	c.Storage.Enabled = true
	c.Storage.CaptureMode = "flagged_only"

	res := c.Validate()

	if !hasWarning(res, "capture_content") {
		t.Fatalf("expected a coherence warning for capture_content=all + capture_mode=flagged_only; warnings=%v", res.Warnings)
	}
}

func TestValidate_CaptureContentAllWithStorageDisabled_Warns(t *testing.T) {
	c := defaults()
	c.Telemetry.CaptureContent = "all"
	c.Storage.Enabled = false
	c.Storage.CaptureMode = "all"

	res := c.Validate()

	if !hasWarning(res, "capture_content") {
		t.Fatalf("expected a coherence warning when storage is disabled; warnings=%v", res.Warnings)
	}
}

// The coherent combination must NOT warn.
func TestValidate_CaptureContentAllWithCaptureModeAll_NoWarn(t *testing.T) {
	c := defaults()
	c.Telemetry.CaptureContent = "all"
	c.Storage.Enabled = true
	c.Storage.CaptureMode = "all"

	res := c.Validate()

	if hasWarning(res, "capture_content") {
		t.Errorf("did not expect a coherence warning when coherent; warnings=%v", res.Warnings)
	}
}

// capture_content=none never needs the buffer, so it must not warn.
func TestValidate_CaptureContentNone_NoWarn(t *testing.T) {
	c := defaults()
	c.Telemetry.CaptureContent = "none"
	c.Storage.Enabled = true
	c.Storage.CaptureMode = "flagged_only"

	res := c.Validate()

	if hasWarning(res, "capture_content") {
		t.Errorf("did not expect a coherence warning for capture_content=none; warnings=%v", res.Warnings)
	}
}

func hasWarning(res *ValidationResult, fieldSubstr string) bool {
	for _, w := range res.Warnings {
		if strings.Contains(w.Field, fieldSubstr) {
			return true
		}
	}
	return false
}
