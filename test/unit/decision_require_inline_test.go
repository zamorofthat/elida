package unit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elida/internal/config"
	"elida/internal/decision/embedded"
)

func TestRequireInline_DefaultsToFalse(t *testing.T) {
	// The gate must be opt-in. A deployment that upgrades into this change
	// and runs on arm64 keeps starting.
	c := config.DefaultConfig()
	if c.Decision.RequireInline {
		t.Fatal("decision.require_inline must default to false")
	}

	// And an existing config file that says nothing about it still loads
	// with the gate off.
	path := writeTempConfig(t, "listen: \":8080\"\nbackend: \"https://api.example.com\"\ndecision:\n  enabled: true\n  model_path: /etc/elida/models/injection\n")
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if loaded.Decision.RequireInline {
		t.Fatal("a config that does not mention require_inline must leave it false")
	}
	if !loaded.Decision.Enabled {
		t.Fatal("sanity: the fixture enables decision")
	}
	// It must not have become a validation error either.
	res := loaded.Validate()
	for _, e := range res.Errors {
		if strings.Contains(e.Field, "require_inline") {
			t.Fatalf("an unset require_inline must not error: %s: %s", e.Field, e.Message)
		}
	}
}

func TestRequireInline_EnvOverride(t *testing.T) {
	t.Setenv("ELIDA_DECISION_REQUIRE_INLINE", "true")
	path := writeTempConfig(t, "listen: \":8080\"\nbackend: \"https://api.example.com\"\n")
	c, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if !c.Decision.RequireInline {
		t.Fatal("ELIDA_DECISION_REQUIRE_INLINE=true must set the flag")
	}
}

func TestRequireInline_ValidationWarnsOnUnsupportedArch(t *testing.T) {
	// Asking for inline on a build with no accelerated kernels cannot be a
	// config error, because the config is architecture-independent and the
	// same file is deployed everywhere. It is a warning; the hard failure
	// happens at startup, where the architecture is known.
	c := config.DefaultConfig()
	c.Listen = ":8080"
	c.Backend = "https://api.example.com"
	c.Decision.Enabled = true
	c.Decision.RequireInline = true

	res := c.Validate()
	if !res.Valid {
		t.Fatalf("require_inline must not make a config invalid: %+v", res.Errors)
	}
	var found bool
	for _, w := range res.Warnings {
		if w.Field == "decision.require_inline" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a decision.require_inline warning explaining the startup gate, got %+v", res.Warnings)
	}
}

func TestRequireInline_StartupSucceedsWhenCapabilityIsInline(t *testing.T) {
	quietSlog(t)
	p, err := embedded.New(context.Background(), embedded.Options{
		Enabled:       true,
		Required:      true,
		RequireInline: true,
		ModelPath:     copyFixture(t),
		ThresholdSet:  "v1",
		Arch:          "amd64",
		SIMD:          embedded.Bool(true),
		NewPipeline:   fakeFactory(),
	})
	if err != nil {
		t.Fatalf("amd64 with SIMD computes capability inline, so the gate must pass: %v", err)
	}
	defer func() { _ = p.Close() }()
	if got := p.Health().Capability; got != embedded.CapabilityInline {
		t.Fatalf("Capability = %q, want inline", got)
	}
}

func TestRequireInline_StartupFailsOnAsyncOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		arch string
		simd bool
	}{
		{"arm64 with the experiment on", "arm64", true},
		{"arm64 with the experiment off", "arm64", false},
		{"amd64 without the experiment", "amd64", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := embedded.New(context.Background(), embedded.Options{
				Enabled:       true,
				Required:      true,
				RequireInline: true,
				ModelPath:     copyFixture(t),
				ThresholdSet:  "v1",
				Arch:          tc.arch,
				SIMD:          embedded.Bool(tc.simd),
				NewPipeline:   fakeFactory(),
			})
			if err == nil {
				_ = p.Close()
				t.Fatalf("arch=%s simd=%v computes async_only; require_inline must fail startup", tc.arch, tc.simd)
			}
			if !errors.Is(err, embedded.ErrInlineRequired) {
				t.Fatalf("error = %v, want ErrInlineRequired", err)
			}
			// The message has to tell an operator what to do about it.
			msg := err.Error()
			for _, want := range []string{"async_only", "require_inline", tc.arch} {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q does not mention %q", msg, want)
				}
			}
		})
	}
}

func TestRequireInline_StartupFailsOnDegraded(t *testing.T) {
	// Degraded means every decision is unknown. An operator who required
	// inline protection must not be left running with none, even with
	// required: false, which only permits a degraded MODEL load.
	dir := copyFixture(t)
	if err := os.Remove(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	p, err := embedded.New(context.Background(), embedded.Options{
		Enabled:       true,
		Required:      false, // would otherwise start degraded
		RequireInline: true,
		ModelPath:     dir,
		ThresholdSet:  "v1",
		Arch:          "amd64",
		SIMD:          embedded.Bool(true),
		NewPipeline:   fakeFactory(),
	})
	if err == nil {
		_ = p.Close()
		t.Fatal("require_inline must override required: false when the model did not load")
	}
	if !errors.Is(err, embedded.ErrInlineRequired) {
		t.Fatalf("error = %v, want ErrInlineRequired", err)
	}
	if !strings.Contains(err.Error(), "degraded") {
		t.Fatalf("the error must name the degraded capability: %v", err)
	}
}

func TestRequireInline_DegradedErrorNamesTheArchitectureRemedy(t *testing.T) {
	// On arm64, fixing the model assets only reaches async_only. The
	// degraded refusal must say so up front, with the architecture, whether
	// SIMD kernels are compiled in, and which builds can be inline.
	dir := copyFixture(t)
	if err := os.Remove(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	_, err := embedded.New(context.Background(), embedded.Options{
		Enabled:       true,
		RequireInline: true,
		ModelPath:     dir,
		ThresholdSet:  "v1",
		Arch:          "arm64",
		SIMD:          embedded.Bool(false),
		NewPipeline:   fakeFactory(),
	})
	if !errors.Is(err, embedded.ErrInlineRequired) {
		t.Fatalf("error = %v, want ErrInlineRequired", err)
	}
	for _, want := range []string{"arch=arm64", "simd=false", "Fix the model assets", "linux/amd64", "GOEXPERIMENT=simd", "decision.require_inline: false"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("degraded refusal must contain %q: %v", want, err)
		}
	}
}

func TestRequireInline_ValidationWarnsWhenInert(t *testing.T) {
	// require_inline does nothing while decision is off (no model is
	// loaded, startup succeeds). That must be said, not left silent or
	// described as a startup gate.
	cases := []struct {
		name    string
		enabled bool
		mode    string
	}{
		{"decision.enabled false", false, config.DecisionModeShadow},
		{"decision.mode disabled", true, config.DecisionModeDisabled},
	}
	for _, tc := range cases {
		c := config.DefaultConfig()
		c.Listen = ":8080"
		c.Backend = "https://api.example.com"
		c.Decision.Enabled = tc.enabled
		c.Decision.Mode = tc.mode
		c.Decision.RequireInline = true
		res := c.Validate()
		if !res.Valid {
			t.Fatalf("%s: require_inline must not make a config invalid: %+v", tc.name, res.Errors)
		}
		var inert, gate bool
		for _, w := range res.Warnings {
			if w.Field != "decision.require_inline" {
				continue
			}
			if strings.Contains(w.Message, "has no effect while decision is disabled") {
				inert = true
			}
			if strings.Contains(w.Message, "startup will fail") {
				gate = true
			}
		}
		if !inert || gate {
			t.Fatalf("%s: want the inert warning and not the startup-gate one, got %+v", tc.name, res.Warnings)
		}
	}
}

func TestDecisionValidation_WarnsWhenNoPolicyEngineCapsTheMode(t *testing.T) {
	for _, mode := range []string{config.DecisionModeAudit, config.DecisionModeEnforce, config.DecisionModeShadow} {
		c := config.DefaultConfig()
		c.Listen = ":8080"
		c.Backend = "https://api.example.com"
		c.Policy.Enabled = false
		c.Decision.Enabled = true
		c.Decision.Mode = mode
		res := c.Validate()
		var capped bool
		for _, w := range res.Warnings {
			if w.Field == "decision.mode" && strings.Contains(w.Message, "capped to shadow because the policy engine is disabled") {
				capped = true
			}
		}
		if want := mode != config.DecisionModeShadow; capped != want {
			t.Fatalf("mode %s: no-policy cap warning = %v, want %v; warnings %+v", mode, capped, want, res.Warnings)
		}
	}
}

func TestRequireInline_FalsePermitsAsyncOnly(t *testing.T) {
	quietSlog(t)
	p, err := embedded.New(context.Background(), embedded.Options{
		Enabled:       true,
		Required:      true,
		RequireInline: false,
		ModelPath:     copyFixture(t),
		ThresholdSet:  "v1",
		Arch:          "arm64",
		SIMD:          embedded.Bool(false),
		NewPipeline:   fakeFactory(),
	})
	if err != nil {
		t.Fatalf("with require_inline false, async_only is permitted: %v", err)
	}
	defer func() { _ = p.Close() }()
	h := p.Health()
	if h.Capability != embedded.CapabilityAsyncOnly {
		t.Fatalf("Capability = %q, want async_only", h.Capability)
	}
	if h.Reason == "" {
		t.Fatal("async_only must still explain itself")
	}
}

func TestRequireInline_DisabledFeatureIgnoresTheGate(t *testing.T) {
	// require_inline with the feature off is inert: nothing is loaded, so
	// there is no capability to require. A disabled feature must never be
	// able to fail startup.
	p, err := embedded.New(context.Background(), embedded.Options{
		Enabled:       false,
		RequireInline: true,
		ModelPath:     filepath.Join(t.TempDir(), "does-not-exist"),
		Arch:          "arm64",
		SIMD:          embedded.Bool(false),
	})
	if err != nil {
		t.Fatalf("a disabled feature must never fail startup: %v", err)
	}
	defer func() { _ = p.Close() }()
	if got := p.Health().Capability; got != embedded.CapabilityDisabled {
		t.Fatalf("Capability = %q, want disabled", got)
	}
}

func TestRequireInline_RealBuildMatchesSIMDEnabled(t *testing.T) {
	quietSlog(t)
	// The gate and the reported capability must derive from the same fact.
	// This pins that SIMDEnabled() is what the capability matrix consults,
	// so a build-tag change moves both together.
	p, err := embedded.New(context.Background(), embedded.Options{
		Enabled:      true,
		Required:     true,
		ModelPath:    copyFixture(t),
		ThresholdSet: "v1",
		// Arch and SIMD left unset: New fills them from this binary.
		NewPipeline: fakeFactory(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = p.Close() }()

	h := p.Health()
	if h.Arch != embedded.DefaultArch() {
		t.Errorf("Health.Arch = %q, want %q", h.Arch, embedded.DefaultArch())
	}
	if h.SIMD != embedded.SIMDEnabled() {
		t.Errorf("Health.SIMD = %v, want SIMDEnabled() = %v", h.SIMD, embedded.SIMDEnabled())
	}
	wantInline := embedded.DefaultArch() == "amd64" && embedded.SIMDEnabled()
	gotInline := h.Capability == embedded.CapabilityInline
	if gotInline != wantInline {
		t.Fatalf("capability inline = %v, but arch=%q simd=%v implies %v",
			gotInline, h.Arch, h.SIMD, wantInline)
	}
	t.Logf("this build: arch=%s simd=%v capability=%s", h.Arch, h.SIMD, h.Capability)
}

func TestRequireInline_LoadFailureWrapsTheCause(t *testing.T) {
	// With required: true as well, the gate still names itself, and the load
	// error stays matchable so an operator's tooling can tell why.
	dir := copyFixture(t)
	if err := os.Remove(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	_, err := embedded.New(context.Background(), embedded.Options{
		Enabled:       true,
		Required:      true,
		RequireInline: true,
		ModelPath:     dir,
		ThresholdSet:  "v1",
		Arch:          "amd64",
		SIMD:          embedded.Bool(true),
		NewPipeline:   fakeFactory(),
	})
	if !errors.Is(err, embedded.ErrInlineRequired) {
		t.Fatalf("error = %v, want ErrInlineRequired", err)
	}
	if !errors.Is(err, embedded.ErrManifestMissing) {
		t.Fatalf("error = %v, want the wrapped ErrManifestMissing", err)
	}
	for _, want := range []string{"degraded", "require_inline", "amd64", "simd=true"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestRequireInline_PipelineFailureIsRefused(t *testing.T) {
	// The second degraded path (the factory fails) must not bypass the gate.
	boom := errors.New("pipeline construction failed")
	_, err := embedded.New(context.Background(), embedded.Options{
		Enabled:       true,
		Required:      false,
		RequireInline: true,
		ModelPath:     copyFixture(t),
		ThresholdSet:  "v1",
		Arch:          "amd64",
		SIMD:          embedded.Bool(true),
		NewPipeline: func(context.Context, string, *embedded.Manifest) (embedded.Pipeline, error) {
			return nil, boom
		},
	})
	if !errors.Is(err, embedded.ErrInlineRequired) || !errors.Is(err, boom) {
		t.Fatalf("error = %v, want ErrInlineRequired wrapping the factory error", err)
	}
}
