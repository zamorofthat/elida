// Package decisiontest provides test doubles for the decision contract.
//
// It is a normal package, not a _test.go file, because the black-box tests
// in test/unit/ and the scheduler's own tests both need it. Nothing in
// production code may import it.
package decisiontest

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"elida/internal/decision"
)

// FakeProvider is a configurable decision.Provider.
//
// Zero value is usable: it supports nothing and answers nothing, which is
// the "unknown is never safe" case. Use NewFake for the common case.
type FakeProvider struct {
	// ProviderName is returned by Name(). Defaults to "fake".
	ProviderName string
	// Supported lists the signals Supports() reports true for.
	Supported []decision.Signal
	// Scores is the probability returned per signal.
	Scores map[decision.Signal]float64
	// ScoreFunc, when set, overrides Scores and lets a test make the answer
	// depend on the content (for windowing and dedup tests). It cannot widen
	// Supported: a signal absent from Supported stays unanswered even if
	// ScoreFunc returns a score for it.
	ScoreFunc func(decision.Input) map[decision.Signal]float64
	// Latency is how long Decide blocks before answering. Decide returns
	// ctx.Err() if the context expires first.
	Latency time.Duration
	// Err, when set, is returned by every Decide call.
	Err error
	// PanicOn, when non-empty, makes Decide panic if the input content
	// contains it. Used to prove the circuit breaker catches provider panics.
	PanicOn string
	// UnanswerAfter, when > 0, makes every call after the Nth return
	// unanswered decisions. Used to prove degraded behavior stays "unknown".
	UnanswerAfter int

	calls       atomic.Int64
	inFlight    atomic.Int64
	maxInFlight atomic.Int64
}

// NewFake returns a provider that supports exactly the signals it has scores
// for and answers them with those probabilities.
func NewFake(scores map[decision.Signal]float64) *FakeProvider {
	signals := make([]decision.Signal, 0, len(scores))
	for s := range scores {
		signals = append(signals, s)
	}
	return &FakeProvider{ProviderName: "fake", Supported: signals, Scores: scores}
}

// Name returns ProviderName, or "fake" if it is unset.
func (f *FakeProvider) Name() string {
	if f.ProviderName == "" {
		return "fake"
	}
	return f.ProviderName
}

// Supports reports whether s appears in Supported.
func (f *FakeProvider) Supports(s decision.Signal) bool {
	for _, sup := range f.Supported {
		if sup == s {
			return true
		}
	}
	return false
}

// Decide implements decision.Provider.
func (f *FakeProvider) Decide(ctx context.Context, in decision.Input, signals []decision.Signal) ([]decision.Decision, error) {
	n := f.calls.Add(1)

	cur := f.inFlight.Add(1)
	for {
		max := f.maxInFlight.Load()
		if cur <= max || f.maxInFlight.CompareAndSwap(max, cur) {
			break
		}
	}
	defer f.inFlight.Add(-1)

	if f.PanicOn != "" && strings.Contains(in.Content, f.PanicOn) {
		panic("decisiontest: injected panic on " + f.PanicOn)
	}

	start := time.Now()
	if f.Latency > 0 {
		timer := time.NewTimer(f.Latency)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	} else if err := ctx.Err(); err != nil {
		return nil, err
	}

	if f.Err != nil {
		return nil, f.Err
	}

	scores := f.Scores
	if f.ScoreFunc != nil {
		scores = f.ScoreFunc(in)
	}
	unanswerAll := f.UnanswerAfter > 0 && n > int64(f.UnanswerAfter)

	latency := time.Since(start)
	out := make([]decision.Decision, 0, len(signals))
	for _, s := range signals {
		d := decision.Decision{
			Signal:  s,
			Model:   f.Name(),
			Version: "fake-v1",
			Latency: latency,
		}
		if p, ok := scores[s]; ok && f.Supports(s) && !unanswerAll {
			d.Probability = p
			d.Answered = true
		}
		out = append(out, d)
	}
	return out, nil
}

// Calls returns how many times Decide was entered.
func (f *FakeProvider) Calls() int64 { return f.calls.Load() }

// MaxInFlight returns the highest number of concurrent Decide calls observed.
// Scheduler tests use this to prove max_concurrency is honored.
func (f *FakeProvider) MaxInFlight() int64 { return f.maxInFlight.Load() }

// Reset clears the counters, leaving configuration alone.
func (f *FakeProvider) Reset() {
	f.calls.Store(0)
	f.inFlight.Store(0)
	f.maxInFlight.Store(0)
}

// ByteTokenCounter is a decision.TokenCounter that approximates tokens by
// byte count. It stands in for the real tokenizer wherever a test must not
// load a model. BytesPerToken defaults to 4, which is roughly English text
// through a WordPiece vocabulary.
type ByteTokenCounter struct{ BytesPerToken int }

// CountTokens approximates the token count of text as a byte-count division,
// rounded up, using BytesPerToken (or 4 if unset or non-positive).
func (c ByteTokenCounter) CountTokens(text string) int {
	if text == "" {
		return 0
	}
	per := c.BytesPerToken
	if per <= 0 {
		per = 4
	}
	return (len(text) + per - 1) / per
}

// Compile-time proof that the fake satisfies the contracts it stands in for.
var (
	_ decision.Provider     = (*FakeProvider)(nil)
	_ decision.TokenCounter = ByteTokenCounter{}
)
