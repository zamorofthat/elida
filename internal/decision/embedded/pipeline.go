package embedded

import (
	"context"
	"fmt"
	"runtime"
)

// Pipeline is the inference seam.
//
// Logits returns RAW, uncalibrated logits — one per head, in the manifest's
// head order — for each input text. Calibration (temperature scaling and the
// sigmoid) is applied by this package, not by the backend, because the
// packaged pipeline's own sigmoid does not know about the model's
// temperature and would produce overconfident probabilities.
type Pipeline interface {
	Logits(ctx context.Context, texts []string) ([][]float64, error)
	CountTokens(text string) int
	Close() error
}

// PipelineFactory constructs a Pipeline for a verified model directory.
// Task 21 supplies the Hugot implementation; tests supply a fake.
type PipelineFactory func(ctx context.Context, dir string, m *Manifest) (Pipeline, error)

// fakePipeline is a Pipeline backed by a lookup table. It is exported
// through NewFakePipelineFactory so black-box tests in test/unit/ can build
// a working provider without a real model.
type fakePipeline struct {
	logits        map[string][]float64
	bytesPerToken int
	heads         int
}

// NewFakePipelineFactory returns a factory whose pipeline answers from a
// table keyed by exact input text, falling back to the "" entry. Each value
// is one raw logit per head, in head order.
//
// This is the seam that lets the manifest, startup-mode, degraded-mode and
// circuit-breaker tests run with no ONNX runtime and no 90 MiB download.
func NewFakePipelineFactory(logits map[string][]float64, bytesPerToken int) PipelineFactory {
	return func(_ context.Context, _ string, m *Manifest) (Pipeline, error) {
		return &fakePipeline{logits: logits, bytesPerToken: bytesPerToken, heads: len(m.HeadOrder)}, nil
	}
}

func (f *fakePipeline) Logits(ctx context.Context, texts []string) ([][]float64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([][]float64, 0, len(texts))
	for _, t := range texts {
		v, ok := f.logits[t]
		if !ok {
			v, ok = f.logits[""]
		}
		if !ok {
			return nil, fmt.Errorf("embedded: fake pipeline has no logits for %q and no default", t)
		}
		if len(v) != f.heads {
			return nil, fmt.Errorf("embedded: fake pipeline returned %d logits for a %d-head model", len(v), f.heads)
		}
		out = append(out, v)
	}
	return out, nil
}

func (f *fakePipeline) CountTokens(text string) int {
	if text == "" {
		return 0
	}
	per := f.bytesPerToken
	if per <= 0 {
		per = 4
	}
	return (len(text) + per - 1) / per
}

func (f *fakePipeline) Close() error { return nil }

// DefaultArch returns the architecture this binary was built for.
func DefaultArch() string { return runtime.GOARCH }
