package embedded

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"elida/internal/decision"
)

// maxBatch is the number of texts sent to the pipeline per call. It is
// pinned to 1 because Hugot 0.8.1 panics on a batch of 10
// (backends/model_gomlx.go:256), so every Decide issues exactly one input.
const maxBatch = 1

// Capability is what a deployment can honestly claim about semantic
// detection.
type Capability string

const (
	// CapabilityInline: the model is loaded on an architecture whose
	// measured latency can fit an inline budget.
	CapabilityInline Capability = "inline"
	// CapabilityAsyncOnly: the model is loaded, but this architecture runs
	// the scalar path and cannot meet an inline budget. ELIDA does not
	// claim equivalent protection across architectures with materially
	// different measured performance.
	CapabilityAsyncOnly Capability = "async_only"
	// CapabilityDegraded: the feature was enabled but the model could not
	// be loaded or verified. Every decision is unknown.
	CapabilityDegraded Capability = "degraded"
	// CapabilityDisabled: the feature is off. No assets are loaded.
	CapabilityDisabled Capability = "disabled"
)

// Health is the read-only status the control API and telemetry report.
type Health struct {
	Capability          Capability        // what the deployment can claim
	Reason              string            // why, in operator terms
	Arch                string            // architecture the capability was judged on
	SIMD                bool              // whether accelerated kernels are present
	Model               string            // loaded model name
	Version             string            // loaded model version
	Checksum            string            // SHA-256 of the loaded manifest
	Signals             []decision.Signal // signals the model answers
	ThresholdSet        string            // configured threshold-set version
	ThresholdSetMatches bool              // configured set equals the model's
	BreakerOpen         bool              // circuit breaker currently open
	Errors              int64             // inference errors since start
	Panics              int64             // recovered inference panics
}

// Options configures the embedded provider.
type Options struct {
	Enabled      bool
	Required     bool
	ModelPath    string
	ThresholdSet string
	// Arch and SIMD default to this binary's values; tests set them to
	// exercise the capability matrix.
	Arch string
	SIMD bool
	// NewPipeline defaults to the Hugot factory (Task 21).
	NewPipeline PipelineFactory
	// BreakerThreshold is how many consecutive failures trip the circuit
	// breaker. BreakerCooldown is how long it stays open. See Task 17.
	BreakerThreshold int
	BreakerCooldown  time.Duration
	Clock            func() time.Time
}

// Provider is the in-process decision.Provider.
//
// It is safe for concurrent use. The pipeline is shared: one pipeline, a
// bounded number of callers (the scheduler owns that bound), rather than one
// pipeline per request.
type Provider struct {
	mu       sync.RWMutex
	pipeline Pipeline
	manifest *Manifest

	capability   Capability
	reason       string
	arch         string
	simd         bool
	thresholdSet string
	clock        func() time.Time

	breakerThreshold int
	breakerCooldown  time.Duration
	consecutiveFails atomic.Int64
	breakerUntil     atomic.Int64 // unix nanos; 0 = closed
	errors           atomic.Int64
	panics           atomic.Int64
}

// New constructs the provider.
//
// Startup contract:
//   - Enabled false: load nothing, return a disabled provider, nil error.
//   - Enabled true, Required true: any load or verification failure is a
//     startup failure and returns an error.
//   - Enabled true, Required false: a load failure logs a prominent
//     operational event and returns a degraded provider with a nil error.
func New(ctx context.Context, opts Options) (*Provider, error) {
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.Arch == "" {
		opts.Arch = DefaultArch()
	}
	if opts.BreakerThreshold <= 0 {
		opts.BreakerThreshold = 5
	}
	if opts.BreakerCooldown <= 0 {
		opts.BreakerCooldown = 30 * time.Second
	}

	p := &Provider{
		arch:             opts.Arch,
		simd:             opts.SIMD,
		thresholdSet:     opts.ThresholdSet,
		clock:            opts.Clock,
		breakerThreshold: opts.BreakerThreshold,
		breakerCooldown:  opts.BreakerCooldown,
	}

	if !opts.Enabled {
		p.capability = CapabilityDisabled
		p.reason = "decision.enabled is false"
		return p, nil
	}

	factory := opts.NewPipeline
	if factory == nil {
		return nil, fmt.Errorf("embedded: no pipeline factory configured")
	}

	m, err := Load(opts.ModelPath)
	if err != nil {
		if opts.Required {
			return nil, fmt.Errorf("embedded: decision.required is true and the model could not be loaded: %w", err)
		}
		p.capability = CapabilityDegraded
		p.reason = err.Error()
		slog.Error("semantic detection is DEGRADED: the model could not be loaded and decision.required is false",
			"model_path", opts.ModelPath,
			"error", err,
			"consequence", "every semantic decision is unknown; no injection detection is active",
		)
		return p, nil
	}

	pipe, err := factory(ctx, opts.ModelPath, m)
	if err != nil {
		if opts.Required {
			return nil, fmt.Errorf("embedded: decision.required is true and the pipeline could not be built: %w", err)
		}
		p.capability = CapabilityDegraded
		p.reason = err.Error()
		slog.Error("semantic detection is DEGRADED: the inference pipeline could not be built and decision.required is false",
			"model_path", opts.ModelPath,
			"error", err,
			"consequence", "every semantic decision is unknown; no injection detection is active",
		)
		return p, nil
	}

	p.manifest = m
	p.pipeline = pipe

	// Capability depends on measured performance, not on the model loading.
	// GoMLX's accelerated kernels are gated to `amd64 && goexperiment.simd`;
	// everything else runs the scalar path at roughly 20x the latency, which
	// no 50 ms inline budget can absorb.
	if opts.Arch == "amd64" && opts.SIMD {
		p.capability = CapabilityInline
		p.reason = "amd64 with GOEXPERIMENT=simd: accelerated kernels are compiled in"
	} else {
		p.capability = CapabilityAsyncOnly
		p.reason = fmt.Sprintf("%s simd=%v: GoMLX accelerated kernels are gated to amd64 && goexperiment.simd, so this build runs the scalar path and cannot meet an inline budget", opts.Arch, opts.SIMD)
	}

	slog.Info("semantic detection enabled",
		"capability", p.capability,
		"reason", p.reason,
		"model", m.Name,
		"version", m.Version,
		"checksum", m.Checksum(),
		"threshold_set", opts.ThresholdSet,
		"threshold_set_matches", opts.ThresholdSet == m.Calibration.ThresholdSet,
		"signals", m.Signals,
	)
	return p, nil
}

// Name implements decision.Provider.
func (p *Provider) Name() string { return "embedded" }

// Manifest returns the loaded manifest, or nil when disabled or degraded.
func (p *Provider) Manifest() *Manifest {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.manifest
}

// Supports reports whether the packaged model actually answers a signal.
//
// A disabled or degraded provider supports nothing: callers must check this
// during initialization and must not read an unanswered signal as safe.
func (p *Provider) Supports(s decision.Signal) bool {
	p.mu.RLock()
	m := p.manifest
	p.mu.RUnlock()
	if m == nil {
		return false
	}
	for _, sup := range m.Signals {
		if sup == s {
			return true
		}
	}
	return false
}

// unanswered builds one unanswered decision per requested signal.
func (p *Provider) unanswered(signals []decision.Signal, latency time.Duration) []decision.Decision {
	p.mu.RLock()
	m := p.manifest
	p.mu.RUnlock()
	var name, version string
	if m != nil {
		name, version = m.Name, m.Version
	}
	out := make([]decision.Decision, 0, len(signals))
	for _, s := range signals {
		out = append(out, decision.Decision{
			Signal:  s,
			Model:   name,
			Version: version,
			Latency: latency,
		})
	}
	return out
}

// Decide implements decision.Provider for one window.
func (p *Provider) Decide(ctx context.Context, in decision.Input, signals []decision.Signal) ([]decision.Decision, error) {
	p.mu.RLock()
	pipe, m := p.pipeline, p.manifest
	p.mu.RUnlock()

	if pipe == nil || m == nil {
		// Disabled or degraded: unknown, never safe, and never an error —
		// the orchestration layer must not treat a disabled feature as a
		// request failure.
		return p.unanswered(signals, 0), nil
	}
	if p.breakerOpen() {
		return p.unanswered(signals, 0), nil
	}

	start := p.clock()
	logits, err := p.runGuarded(ctx, pipe, in.Content)
	latency := p.clock().Sub(start)
	if err != nil {
		p.recordFailure()
		return p.unanswered(signals, latency), err
	}
	p.consecutiveFails.Store(0)

	// Map head order to probabilities with the model's own temperature.
	probs := make(map[decision.Signal]float64, len(m.HeadOrder))
	for i, head := range m.HeadOrder {
		if i >= len(logits) {
			break
		}
		probs[decision.Signal(head)] = calibrate(logits[i], m.Calibration.Temperature)
	}

	out := make([]decision.Decision, 0, len(signals))
	for _, s := range signals {
		d := decision.Decision{
			Signal:  s,
			Model:   m.Name,
			Version: m.Version,
			Latency: latency,
		}
		if prob, ok := probs[s]; ok && p.Supports(s) {
			d.Probability = prob
			d.Answered = true
		}
		out = append(out, d)
	}
	return out, nil
}

// runGuarded calls the pipeline, converting a provider panic into an error
// so one bad input cannot take the proxy down.
func (p *Provider) runGuarded(ctx context.Context, pipe Pipeline, text string) (out []float64, err error) {
	defer func() {
		if r := recover(); r != nil {
			p.panics.Add(1)
			err = fmt.Errorf("embedded: inference panicked: %v", r)
		}
	}()
	texts := []string{text}
	if len(texts) > maxBatch {
		return nil, fmt.Errorf("embedded: batch of %d exceeds maxBatch %d (hugot backends/model_gomlx.go:256 panics on large batches)", len(texts), maxBatch)
	}
	batch, err := pipe.Logits(ctx, texts)
	if err != nil {
		return nil, err
	}
	if len(batch) != 1 {
		return nil, fmt.Errorf("embedded: pipeline returned %d results for 1 input", len(batch))
	}
	return batch[0], nil
}

// calibrate turns a raw logit into a calibrated probability.
//
// The packaged pipeline's own sigmoid does not apply the model's
// temperature, so applying it here is what makes the probability
// comparable to the calibrated thresholds in the manifest. Skipping it
// would make every score overconfident: sigmoid(6.0) is 0.9975 where
// sigmoid(6.0/2.41) is 0.9263.
func calibrate(logit, temperature float64) float64 {
	if temperature <= 0 {
		temperature = 1
	}
	return 1 / (1 + math.Exp(-logit/temperature))
}

// CountTokens implements decision.TokenCounter.
//
// A disabled or degraded provider still estimates, so the scheduler can
// window content without a loaded tokenizer.
func (p *Provider) CountTokens(text string) int {
	p.mu.RLock()
	pipe := p.pipeline
	p.mu.RUnlock()
	if pipe != nil {
		return pipe.CountTokens(text)
	}
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}

// Health returns the current status.
func (p *Provider) Health() Health {
	p.mu.RLock()
	m := p.manifest
	cap, reason := p.capability, p.reason
	p.mu.RUnlock()

	h := Health{
		Capability:   cap,
		Reason:       reason,
		Arch:         p.arch,
		SIMD:         p.simd,
		ThresholdSet: p.thresholdSet,
		BreakerOpen:  p.breakerOpen(),
		Errors:       p.errors.Load(),
		Panics:       p.panics.Load(),
	}
	if m != nil {
		h.Model = m.Name
		h.Version = m.Version
		h.Checksum = m.Checksum()
		h.Signals = append([]decision.Signal(nil), m.Signals...)
		h.ThresholdSetMatches = p.thresholdSet == m.Calibration.ThresholdSet
	}
	return h
}

// Close releases the pipeline.
func (p *Provider) Close() error {
	p.mu.Lock()
	pipe := p.pipeline
	p.pipeline = nil
	p.mu.Unlock()
	if pipe == nil {
		return nil
	}
	return pipe.Close()
}

// breakerOpen reports whether the circuit breaker is currently open.
// Task 17 replaces this with the cooldown-aware implementation.
func (p *Provider) breakerOpen() bool {
	until := p.breakerUntil.Load()
	return until != 0 && p.clock().UnixNano() < until
}

// recordFailure counts a failure. Task 17 adds the trip logic.
func (p *Provider) recordFailure() {
	p.errors.Add(1)
	p.consecutiveFails.Add(1)
}

var (
	_ decision.Provider     = (*Provider)(nil)
	_ decision.TokenCounter = (*Provider)(nil)
)
