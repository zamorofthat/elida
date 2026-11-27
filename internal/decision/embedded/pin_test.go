package embedded

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// The manifest lists the SHA-256 of every model file and Load verifies the
// files against it, but nothing tied the manifest itself to what the operator
// meant to deploy: whoever could write the model directory could replace the
// model, the manifest and the digests together. decision.model_checksum pins
// the manifest's own digest.

func goodChecksum(t *testing.T) string {
	t.Helper()
	m, err := Load(filepath.Join("testdata", "good"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return m.Checksum()
}

func pinOptions(pin string, required bool, built *bool) Options {
	return Options{
		Enabled: true, Required: required, ModelPath: filepath.Join("testdata", "good"), ThresholdSet: "v1",
		ExpectedChecksum: pin,
		NewPipeline: func(context.Context, string, *Manifest) (Pipeline, error) {
			*built = true
			return scriptedPipeline{}, nil
		},
	}
}

func TestProvider_PinMatchLoads(t *testing.T) {
	quietSlog(t)
	good := goodChecksum(t)
	for name, pin := range map[string]string{"lower": good, "upper": strings.ToUpper(good), "padded": " " + good + " "} {
		var built bool
		p, err := New(context.Background(), pinOptions(pin, true, &built))
		if err != nil {
			t.Fatalf("%s: New with a matching pin: %v", name, err)
		}
		if !built || p.Manifest() == nil {
			t.Errorf("%s: matching pin must load the model (built=%v)", name, built)
		}
		_ = p.Close()
	}
}

func TestProvider_PinMismatchRefusedWhenRequired(t *testing.T) {
	quietSlog(t)
	var built bool
	_, err := New(context.Background(), pinOptions(strings.Repeat("0", 64), true, &built))
	if !errors.Is(err, ErrChecksumPinMismatch) {
		t.Fatalf("err = %v, want ErrChecksumPinMismatch", err)
	}
	if built {
		t.Error("a model that fails the pin must never reach the inference pipeline")
	}
}

func TestProvider_PinMismatchDegradesWhenNotRequired(t *testing.T) {
	quietSlog(t)
	var built bool
	p, err := New(context.Background(), pinOptions(strings.Repeat("0", 64), false, &built))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = p.Close() }()
	if built {
		t.Error("a model that fails the pin must never reach the inference pipeline")
	}
	if p.Manifest() != nil {
		t.Error("a model that fails the pin must not be exposed as loaded")
	}
	h := p.Health()
	if h.Capability != CapabilityDegraded {
		t.Errorf("capability = %q, want %q", h.Capability, CapabilityDegraded)
	}
	if !strings.Contains(p.reason, "checksum pin") {
		t.Errorf("reason = %q, want it to name the checksum pin", p.reason)
	}
}

func TestProvider_NoPinKeepsEveryModelLoadable(t *testing.T) {
	quietSlog(t)
	var built bool
	p, err := New(context.Background(), pinOptions("", true, &built))
	if err != nil {
		t.Fatalf("New without a pin: %v", err)
	}
	defer func() { _ = p.Close() }()
	if !built {
		t.Error("an unset pin must not change loading")
	}
}
