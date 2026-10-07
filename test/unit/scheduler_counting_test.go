package unit

import (
	"context"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"elida/internal/decision"
	"elida/internal/decision/decisiontest"
	"elida/internal/decision/scheduler"
)

// countingCounter wraps an exact counter and counts its calls.
type countingCounter struct {
	inner decision.TokenCounter
	calls atomic.Int64
}

func (c *countingCounter) CountTokens(text string) int {
	c.calls.Add(1)
	return c.inner.CountTokens(text)
}

// slowEstimator is an estimator that takes time per call, to make the
// windowing phase outlast the deadline.
type slowEstimator struct{ delay time.Duration }

func (s slowEstimator) CountTokens(text string) int {
	time.Sleep(s.delay)
	return scheduler.EstimateTokens(text)
}

// raceEnabled reports whether this test binary runs the race detector,
// which slows the timing-bound tests below by an order of magnitude.
func raceEnabled() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range bi.Settings {
		if s.Key == "-race" && s.Value == "true" {
			return true
		}
	}
	return false
}

func countingConfig(p decision.Provider, exact decision.TokenCounter) scheduler.Config {
	cfg := inlineConfig(p)
	cfg.TokenCounter = exact
	cfg.Estimator = nil // DefaultEstimator: the production windowing path
	cfg.MaxWindowTokens = scheduler.DefaultWindowTokens
	cfg.MaxInlineTokens = 4 * scheduler.DefaultWindowTokens
	cfg.MaxInlineWindows = 2
	cfg.MaxAsyncWindows = 4
	cfg.AsyncQueueSize = 64
	cfg.InlineTimeout = 5 * time.Second
	return cfg
}

func TestScheduler_ExactCountingIsBoundedOnLargeContent(t *testing.T) {
	discardSlog(t)
	prose := strings.Repeat("the deployment pipeline runs in three stages and then promotes the build ", 3600)[:256*1024]
	sentence := strings.Repeat("word ", 819) + ". "
	fourKB := strings.Repeat(sentence, 64)[:256*1024]

	budget := 100 * time.Millisecond
	if raceEnabled() {
		budget = time.Second
	}
	for name, content := range map[string]string{"prose-no-period": prose, "4kb-sentences": fourKB} {
		t.Run(name, func(t *testing.T) {
			f := decisiontest.NewFake(map[decision.Signal]float64{decision.SignalInjection: 0.1})
			exact := &countingCounter{inner: decisiontest.ByteTokenCounter{BytesPerToken: 4}}
			cfg := countingConfig(f, exact)
			s, err := scheduler.New(cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer func() { _ = s.Shutdown(context.Background()) }()

			req, in := userRequest()
			in.SourceRole = "tool"
			start := time.Now()
			a, err := s.AssessCandidates(context.Background(), req, in, candidatesFor(content), nil)
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("AssessCandidates: %v", err)
			}
			t.Logf("%s: %d windows, %d exact counts, %v", name, a.Coverage.EligibleWindows, exact.calls.Load(), elapsed)
			if elapsed > budget {
				t.Errorf("assessing 256 KB took %v, want under %v", elapsed, budget)
			}
			// The byte/4 exact fake never exceeds the estimate, so no
			// window splits: one exact count per window actually used.
			if limit := int64(cfg.MaxInlineWindows + cfg.MaxAsyncWindows); exact.calls.Load() > limit {
				t.Errorf("exact counter called %d times, want at most %d (inline + async windows)", exact.calls.Load(), limit)
			}
			if a.Coverage.EligibleWindows < 400 {
				t.Errorf("EligibleWindows = %d; 256 KB must window into hundreds of windows", a.Coverage.EligibleWindows)
			}
		})
	}
}

func TestScheduler_OverLimitWindowIsSplitByExactCount(t *testing.T) {
	discardSlog(t)
	f := decisiontest.NewFake(map[decision.Signal]float64{decision.SignalInjection: 0.1})
	// An exact counter that sees one token per byte: the estimator's
	// windows are about four times too big by this count.
	exact := &countingCounter{inner: decisiontest.ByteTokenCounter{BytesPerToken: 1}}
	cfg := countingConfig(f, exact)
	cfg.MaxInlineWindows = 8
	cfg.MaxInlineTokens = 8 * scheduler.DefaultWindowTokens
	cfg.MaxAsyncWindows = 0
	s, err := scheduler.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = s.Shutdown(context.Background()) }()

	content := strings.Repeat("abcd", 120) // 480 bytes, one estimated window
	req, in := userRequest()
	in.SourceRole = "tool"
	a, err := s.AssessCandidates(context.Background(), req, in, candidatesFor(content), nil)
	if err != nil {
		t.Fatalf("AssessCandidates: %v", err)
	}
	if a.Coverage.EligibleWindows < 4 {
		t.Fatalf("EligibleWindows = %d; a 480-token window must split into at least 4 pieces", a.Coverage.EligibleWindows)
	}
	if !a.Coverage.Complete {
		t.Fatalf("every piece fits the inline budget, coverage must be complete: %+v", a.Coverage)
	}
	// Each scored piece is within the limit by the exact count, and the
	// pieces tile the content.
	var tiled strings.Builder
	for _, ad := range a.Admissions {
		piece := content[ad.Window.StartByte:ad.Window.EndByte]
		if n := exact.inner.CountTokens(piece); n > scheduler.DefaultWindowTokens {
			t.Errorf("piece of %d exact tokens exceeds %d", n, scheduler.DefaultWindowTokens)
		}
	}
	ordered := make([]decision.Admission, len(a.Admissions))
	copy(ordered, a.Admissions)
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && ordered[j].Window.StartByte < ordered[j-1].Window.StartByte; j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}
	for _, ad := range ordered {
		tiled.WriteString(content[ad.Window.StartByte:ad.Window.EndByte])
	}
	if tiled.String() != content {
		t.Fatal("split pieces do not tile the window")
	}
}

func TestScheduler_SplitPiecesConsumeBudgetAndAreNeverTruncated(t *testing.T) {
	discardSlog(t)
	f := decisiontest.NewFake(map[decision.Signal]float64{decision.SignalInjection: 0.1})
	exact := decisiontest.ByteTokenCounter{BytesPerToken: 1}
	cfg := countingConfig(f, exact)
	cfg.MaxInlineWindows = 1
	cfg.MaxAsyncWindows = 0
	s, err := scheduler.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = s.Shutdown(context.Background()) }()

	content := strings.Repeat("abcd", 120)
	req, in := userRequest()
	in.SourceRole = "tool"
	a, err := s.AssessCandidates(context.Background(), req, in, candidatesFor(content), nil)
	if err != nil {
		t.Fatalf("AssessCandidates: %v", err)
	}
	// One inline window of budget: one piece scored, the rest are explicit
	// gaps, and coverage says so.
	if a.Coverage.ScoredInline != 1 || a.Coverage.Complete {
		t.Fatalf("Coverage = %+v; want 1 piece scored and the rest reported as gaps", a.Coverage)
	}
	if got := reasonCount(a, decision.DenyInlineBudgetSpent); got != a.Coverage.EligibleWindows-1 {
		t.Errorf("inline_budget_spent = %d, want %d (every unscored piece)", got, a.Coverage.EligibleWindows-1)
	}
}

func TestScheduler_DeadlineDuringWindowingIsACoverageGap(t *testing.T) {
	discardSlog(t)
	f := decisiontest.NewFake(map[decision.Signal]float64{decision.SignalInjection: 0.1})
	cfg := countingConfig(f, decisiontest.ByteTokenCounter{BytesPerToken: 4})
	cfg.Estimator = slowEstimator{delay: time.Millisecond}
	cfg.InlineTimeout = 30 * time.Millisecond
	s, err := scheduler.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = s.Shutdown(context.Background()) }()

	content := strings.Repeat("One short sentence here. ", 2000) // 2000 sentences
	req, in := userRequest()
	in.SourceRole = "tool"
	start := time.Now()
	a, err := s.AssessCandidates(context.Background(), req, in, candidatesFor(content), nil)
	if err != nil {
		t.Fatalf("AssessCandidates: %v", err)
	}
	if a.Coverage.Complete {
		t.Fatal("windowing ran out of deadline; coverage must be incomplete")
	}
	if a.Coverage.EligibleBytes != len(content) {
		t.Errorf("EligibleBytes = %d, want %d: the unwindowed remainder is still eligible content", a.Coverage.EligibleBytes, len(content))
	}
	if reasonCount(a, decision.DenyDeadlineSpent) == 0 {
		t.Error("the unwindowed remainder must be recorded as deadline_spent")
	}
	// Bounded by the deadline plus at most splitCheckEvery slow sentences.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("windowing ignored the deadline: %v", elapsed)
	}
}

func TestSplitWindowsContext_StopsAtCanceledContext(t *testing.T) {
	content := strings.Repeat("Sentence number something. ", 200)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ws, rest := scheduler.SplitWindowsContext(ctx, decision.Candidate{Content: content, EndByte: len(content)}, scheduler.DefaultEstimator, 32)
	if rest < 0 || rest >= len(content) {
		t.Fatalf("rest = %d; a canceled context must leave a remainder", rest)
	}
	var b strings.Builder
	for _, w := range ws {
		b.WriteString(w.Text)
	}
	if b.String() != content[:rest] {
		t.Fatal("windows built before the stop must tile content[:rest]")
	}
	ws, rest = scheduler.SplitWindowsContext(context.Background(), decision.Candidate{Content: content, EndByte: len(content)}, scheduler.DefaultEstimator, 32)
	if rest != -1 || len(ws) == 0 {
		t.Fatalf("a live context must window everything: rest=%d windows=%d", rest, len(ws))
	}
}

func TestEstimateTokens_Shape(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"hello", 2 + 2},                     // bytes/4 = 2 beats 1 word
		{"hello world", 3 + 2},               // bytes/4 = 3
		{`{"a":1}`, 7 + 2},                   // { " a " : 1 } -> 6 symbols + 1 word, capped at len
		{"!!!!!!!!", 8 + 2},                  // one token per symbol
		{"日本語", 3 + 2},                       // one per non-ASCII rune
		{strings.Repeat("\x80", 10), 10 + 2}, // invalid bytes count one each
	}
	for _, c := range cases {
		if got := scheduler.EstimateTokens(c.in); got != c.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	// Monotonic over rune-aligned prefixes (the only prefixes hardSplit
	// takes) on mixed content.
	mixed := `{"k":"v"} plain words, 日本 and !!` + strings.Repeat(" more text", 20)
	prev := 0
	for i := 0; i <= len(mixed); i++ {
		if i < len(mixed) && !utf8.RuneStart(mixed[i]) {
			continue
		}
		n := scheduler.EstimateTokens(mixed[:i])
		if n < prev {
			t.Fatalf("EstimateTokens not monotonic at %d: %d < %d", i, n, prev)
		}
		prev = n
	}
}
