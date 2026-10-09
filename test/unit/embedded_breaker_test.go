package unit

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"elida/internal/decision"
	"elida/internal/decision/embedded"
)

// errPipelineFactory builds a pipeline that fails or panics on demand.
func errPipelineFactory(fail *bool, panicking *bool, mu *sync.Mutex) embedded.PipelineFactory {
	return func(ctx context.Context, dir string, m *embedded.Manifest) (embedded.Pipeline, error) {
		return &flakyPipeline{fail: fail, panicking: panicking, mu: mu, heads: len(m.HeadOrder)}, nil
	}
}

type flakyPipeline struct {
	fail      *bool
	panicking *bool
	mu        *sync.Mutex
	heads     int
}

func (f *flakyPipeline) Logits(ctx context.Context, texts []string) ([][]float64, error) {
	f.mu.Lock()
	shouldFail, shouldPanic := *f.fail, *f.panicking
	f.mu.Unlock()
	if shouldPanic {
		panic("flaky pipeline exploded")
	}
	if shouldFail {
		return nil, errors.New("flaky pipeline failed")
	}
	out := make([][]float64, len(texts))
	for i := range texts {
		row := make([]float64, f.heads)
		for h := range row {
			row[h] = 1.0
		}
		out[i] = row
	}
	return out, nil
}

func (f *flakyPipeline) CountTokens(text string) int { return (len(text) + 3) / 4 }
func (f *flakyPipeline) Close() error                { return nil }

func breakerProvider(t *testing.T, fail, panicking *bool, mu *sync.Mutex, now *time.Time) *embedded.Provider {
	t.Helper()
	p, err := embedded.New(context.Background(), embedded.Options{
		Enabled:          true,
		Required:         true,
		ModelPath:        copyFixture(t),
		ThresholdSet:     "v1",
		Arch:             "amd64",
		SIMD:             embedded.Bool(true),
		NewPipeline:      errPipelineFactory(fail, panicking, mu),
		BreakerThreshold: 3,
		BreakerCooldown:  10 * time.Second,
		Clock:            func() time.Time { return *now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestEmbeddedBreaker_TripsAfterThresholdErrors(t *testing.T) {
	var mu sync.Mutex
	fail, panicking := true, false
	now := time.Now()
	p := breakerProvider(t, &fail, &panicking, &mu, &now)

	in := decision.Input{Content: "x", Direction: decision.DirectionRequest, SourceRole: "user"}
	sig := []decision.Signal{decision.SignalInjection}

	for i := 0; i < 3; i++ {
		if _, err := p.Decide(context.Background(), in, sig); err == nil {
			t.Fatalf("call %d should have errored", i)
		}
		if p.Health().BreakerOpen && i < 2 {
			t.Fatalf("breaker tripped early, after %d failures", i+1)
		}
	}
	h := p.Health()
	if !h.BreakerOpen {
		t.Fatalf("breaker must be open after 3 consecutive failures, health = %+v", h)
	}
	if h.Errors != 3 {
		t.Fatalf("Health.Errors = %d, want 3", h.Errors)
	}

	// While open, Decide must not touch the pipeline and must answer nothing.
	ds, err := p.Decide(context.Background(), in, sig)
	if err != nil {
		t.Fatalf("an open breaker must not surface an error: %v", err)
	}
	if len(ds) != 1 || ds[0].Answered {
		t.Fatalf("an open breaker must answer nothing, got %+v", ds)
	}
	if p.Health().Errors != 3 {
		t.Fatal("a short-circuited call must not count as another error")
	}
}

func TestEmbeddedBreaker_ResetsAfterCooldown(t *testing.T) {
	var mu sync.Mutex
	fail, panicking := true, false
	now := time.Now()
	p := breakerProvider(t, &fail, &panicking, &mu, &now)

	in := decision.Input{Content: "x"}
	sig := []decision.Signal{decision.SignalInjection}
	for i := 0; i < 3; i++ {
		_, _ = p.Decide(context.Background(), in, sig)
	}
	if !p.Health().BreakerOpen {
		t.Fatal("breaker should be open")
	}

	// Still inside the cooldown.
	now = now.Add(9 * time.Second)
	if !p.Health().BreakerOpen {
		t.Fatal("breaker must stay open for the whole cooldown")
	}

	// Past the cooldown, with a healthy pipeline again.
	now = now.Add(2 * time.Second)
	mu.Lock()
	fail = false
	mu.Unlock()
	if p.Health().BreakerOpen {
		t.Fatal("breaker must close after the cooldown elapses")
	}
	ds, err := p.Decide(context.Background(), in, sig)
	if err != nil {
		t.Fatalf("Decide after cooldown: %v", err)
	}
	if len(ds) != 1 || !ds[0].Answered {
		t.Fatalf("a recovered provider must answer, got %+v", ds)
	}
	if p.Health().BreakerOpen {
		t.Fatal("a successful call must leave the breaker closed")
	}
}

func TestEmbeddedBreaker_PanicIsCaughtAndCounted(t *testing.T) {
	var mu sync.Mutex
	fail, panicking := false, true
	now := time.Now()
	p := breakerProvider(t, &fail, &panicking, &mu, &now)

	in := decision.Input{Content: "x"}
	sig := []decision.Signal{decision.SignalInjection}

	// A provider panic must become an error, not a process crash.
	_, err := p.Decide(context.Background(), in, sig)
	if err == nil {
		t.Fatal("a panicking pipeline must surface an error")
	}
	if h := p.Health(); h.Panics != 1 {
		t.Fatalf("Health.Panics = %d, want 1", h.Panics)
	}

	for i := 0; i < 2; i++ {
		_, _ = p.Decide(context.Background(), in, sig)
	}
	h := p.Health()
	if !h.BreakerOpen {
		t.Fatal("repeated panics must trip the breaker")
	}
	if h.Panics != 3 {
		t.Fatalf("Health.Panics = %d, want 3", h.Panics)
	}
}

func TestEmbeddedBreaker_SuccessResetsTheFailureRun(t *testing.T) {
	var mu sync.Mutex
	fail, panicking := true, false
	now := time.Now()
	p := breakerProvider(t, &fail, &panicking, &mu, &now)

	in := decision.Input{Content: "x"}
	sig := []decision.Signal{decision.SignalInjection}

	// Two failures, then a success, then two more failures: the breaker
	// counts consecutive failures, so it must still be closed.
	_, _ = p.Decide(context.Background(), in, sig)
	_, _ = p.Decide(context.Background(), in, sig)
	mu.Lock()
	fail = false
	mu.Unlock()
	if _, err := p.Decide(context.Background(), in, sig); err != nil {
		t.Fatalf("the healthy call should succeed: %v", err)
	}
	mu.Lock()
	fail = true
	mu.Unlock()
	_, _ = p.Decide(context.Background(), in, sig)
	_, _ = p.Decide(context.Background(), in, sig)

	if p.Health().BreakerOpen {
		t.Fatal("a success in the middle must reset the consecutive-failure run")
	}
}

func TestEmbeddedBreaker_ConcurrentFailuresAreRaceFree(t *testing.T) {
	var mu sync.Mutex
	fail, panicking := true, false
	now := time.Now()
	p := breakerProvider(t, &fail, &panicking, &mu, &now)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.Decide(context.Background(), decision.Input{Content: "x"},
				[]decision.Signal{decision.SignalInjection})
			_ = p.Health()
		}()
	}
	wg.Wait()
	if !p.Health().BreakerOpen {
		t.Fatal("32 concurrent failures must trip the breaker")
	}
}

// countingPipeline counts Logits calls and fails on demand.
type countingPipeline struct {
	calls *atomic.Int64
	fail  *atomic.Bool
	heads int
}

func (c *countingPipeline) Logits(ctx context.Context, texts []string) ([][]float64, error) {
	c.calls.Add(1)
	if c.fail.Load() {
		return nil, errors.New("counting pipeline failed")
	}
	row := make([]float64, c.heads)
	return [][]float64{row}, nil
}
func (c *countingPipeline) CountTokens(text string) int { return (len(text) + 3) / 4 }
func (c *countingPipeline) Close() error                { return nil }

func TestEmbeddedBreaker_OpenSkipsPipelineAndProbeReopens(t *testing.T) {
	var calls atomic.Int64
	var fail atomic.Bool
	fail.Store(true)
	now := time.Now()
	p, err := embedded.New(context.Background(), embedded.Options{
		Enabled: true, Required: true, ModelPath: copyFixture(t), ThresholdSet: "v1",
		Arch: "amd64", SIMD: embedded.Bool(true),
		NewPipeline: func(ctx context.Context, dir string, m *embedded.Manifest) (embedded.Pipeline, error) {
			return &countingPipeline{calls: &calls, fail: &fail, heads: len(m.HeadOrder)}, nil
		},
		BreakerThreshold: 3, BreakerCooldown: 10 * time.Second,
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	in := decision.Input{Content: "x"}
	sig := []decision.Signal{decision.SignalInjection}
	for i := 0; i < 3; i++ {
		_, _ = p.Decide(context.Background(), in, sig)
	}
	if calls.Load() != 3 {
		t.Fatalf("pipeline calls = %d, want 3", calls.Load())
	}

	// Open: ten more decisions, none reach the pipeline.
	for i := 0; i < 10; i++ {
		ds, derr := p.Decide(context.Background(), in, sig)
		if derr != nil || len(ds) != 1 || ds[0].Answered {
			t.Fatalf("open breaker must answer nothing without error, got %+v, %v", ds, derr)
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("open breaker reached the pipeline: calls = %d", calls.Load())
	}

	// Half-open: exactly one probe, and its failure re-opens the breaker.
	now = now.Add(11 * time.Second)
	if _, perr := p.Decide(context.Background(), in, sig); perr == nil {
		t.Fatal("the probe should have reached the failing pipeline")
	}
	if calls.Load() != 4 {
		t.Fatalf("probe calls = %d, want 4", calls.Load())
	}
	if !p.Health().BreakerOpen {
		t.Fatal("a failed probe must re-open the breaker")
	}
	_, _ = p.Decide(context.Background(), in, sig)
	if calls.Load() != 4 {
		t.Fatal("re-opened breaker must not call the pipeline")
	}

	// Next cooldown, healthy again: the probe succeeds and closes it.
	now = now.Add(11 * time.Second)
	fail.Store(false)
	ds, err := p.Decide(context.Background(), in, sig)
	if err != nil || len(ds) != 1 || !ds[0].Answered {
		t.Fatalf("successful probe must answer, got %+v, %v", ds, err)
	}
	if p.Health().BreakerOpen {
		t.Fatal("a successful probe must close the breaker")
	}
}

// gatedPipeline blocks in Logits until ctx is done (blockOnCtx) or until
// release is closed (gate), and counts calls.
type gatedPipeline struct {
	calls      *atomic.Int64
	fail       *atomic.Bool
	blockOnCtx bool
	gate       chan struct{}
	heads      int
}

func (g *gatedPipeline) Logits(ctx context.Context, texts []string) ([][]float64, error) {
	g.calls.Add(1)
	if g.blockOnCtx {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if g.gate != nil {
		<-g.gate
	}
	if g.fail.Load() {
		return nil, errors.New("gated pipeline failed")
	}
	return [][]float64{make([]float64, g.heads)}, nil
}
func (g *gatedPipeline) CountTokens(text string) int { return (len(text) + 3) / 4 }
func (g *gatedPipeline) Close() error                { return nil }

func gatedProvider(t *testing.T, mk func(heads int) *gatedPipeline, now *time.Time) *embedded.Provider {
	t.Helper()
	p, err := embedded.New(context.Background(), embedded.Options{
		Enabled: true, Required: true, ModelPath: copyFixture(t), ThresholdSet: "v1",
		Arch: "amd64", SIMD: embedded.Bool(true),
		NewPipeline: func(ctx context.Context, dir string, m *embedded.Manifest) (embedded.Pipeline, error) {
			return mk(len(m.HeadOrder)), nil
		},
		BreakerThreshold: 3, BreakerCooldown: 10 * time.Second,
		Clock: func() time.Time { return *now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestEmbeddedBreaker_ContextErrorsDoNotTrip(t *testing.T) {
	var calls atomic.Int64
	var fail atomic.Bool
	now := time.Now()
	p := gatedProvider(t, func(h int) *gatedPipeline {
		return &gatedPipeline{calls: &calls, fail: &fail, blockOnCtx: true, heads: h}
	}, &now)

	in := decision.Input{Content: "x"}
	sig := []decision.Signal{decision.SignalInjection}
	const n = 3 + 2
	for i := 0; i < n; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := p.Decide(ctx, in, sig); err == nil {
			t.Fatalf("call %d should surface the context error", i)
		}
	}
	h := p.Health()
	if h.BreakerOpen {
		t.Fatal("caller context errors must not trip the breaker")
	}
	if h.Errors != 0 {
		t.Fatalf("Errors = %d, want 0 for caller context errors", h.Errors)
	}
	if h.Canceled != n {
		t.Fatalf("Canceled = %d, want %d", h.Canceled, n)
	}
}

func TestEmbeddedBreaker_PipelineErrorWithLiveContextStillCounts(t *testing.T) {
	var calls atomic.Int64
	var fail atomic.Bool
	fail.Store(true)
	now := time.Now()
	p := gatedProvider(t, func(h int) *gatedPipeline {
		return &gatedPipeline{calls: &calls, fail: &fail, heads: h}
	}, &now)

	for i := 0; i < 3; i++ {
		_, _ = p.Decide(context.Background(), decision.Input{Content: "x"},
			[]decision.Signal{decision.SignalInjection})
	}
	h := p.Health()
	if !h.BreakerOpen || h.Errors != 3 || h.Canceled != 0 {
		t.Fatalf("pipeline errors with a live context must count, health = %+v", h)
	}
}

func TestEmbeddedBreaker_CooldownAdmitsExactlyOneProbe(t *testing.T) {
	var calls atomic.Int64
	var fail atomic.Bool
	fail.Store(true)
	gate := make(chan struct{})
	close(gate) // open while tripping
	now := time.Now()
	var pipe *gatedPipeline
	p := gatedProvider(t, func(h int) *gatedPipeline {
		pipe = &gatedPipeline{calls: &calls, fail: &fail, gate: gate, heads: h}
		return pipe
	}, &now)

	in := decision.Input{Content: "x"}
	sig := []decision.Signal{decision.SignalInjection}
	for i := 0; i < 3; i++ {
		_, _ = p.Decide(context.Background(), in, sig)
	}
	if !p.Health().BreakerOpen {
		t.Fatal("breaker should be open")
	}

	// Hold the probe in flight on a fresh gate.
	probeGate := make(chan struct{})
	pipe.gate = probeGate
	fail.Store(false)
	before := calls.Load()
	now = now.Add(11 * time.Second)

	const callers = 32
	var wg sync.WaitGroup
	answered := make(chan bool, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ds, err := p.Decide(context.Background(), in, sig)
			answered <- err == nil && len(ds) == 1 && ds[0].Answered
		}()
	}

	// Wait until the 31 rejected callers have returned; only the probe remains.
	deadline := time.Now().Add(5 * time.Second)
	for len(answered) < callers-1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := calls.Load() - before; got != 1 {
		t.Fatalf("pipeline calls during half-open = %d, want exactly 1", got)
	}
	if len(answered) != callers-1 {
		t.Fatalf("%d callers returned while the probe was in flight, want %d", len(answered), callers-1)
	}
	for i := 0; i < callers-1; i++ {
		if <-answered {
			t.Fatal("a caller that lost the probe race must get an unanswered decision")
		}
	}

	close(probeGate)
	wg.Wait()
	if !<-answered {
		t.Fatal("the probe should have been answered")
	}
	if p.Health().BreakerOpen {
		t.Fatal("a successful probe must close the breaker")
	}
}
