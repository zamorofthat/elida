package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"elida/internal/decision"
)

// AdmissionPolicy selects which messages may attempt the inline fast lane.
// These flags decide eligibility only; admission additionally requires an
// immediately free worker and remaining inline budget.
type AdmissionPolicy struct {
	// UntrustedToolResults admits tool-result content.
	UntrustedToolResults bool
	// EncodedOrObfuscated admits content whose preprocessing reported any
	// signal (Request.PreSignals non-empty).
	EncodedOrObfuscated bool
	// WeakInjectionSignal admits content where any window carries a cheap
	// injection cue (SuspicionScore > 0).
	WeakInjectionSignal bool
	// ElevatedSessionRisk admits content from a session whose risk is
	// already elevated (Request.Elevated).
	ElevatedSessionRisk bool
	// BroadStrictMode admits any user or tool message when the request is
	// marked strict (Request.Strict).
	BroadStrictMode bool
}

// Request carries the orchestration-level facts the scheduler needs and that
// a Provider must never see. Session and request identifiers live here, not
// in decision.Input, so an HTTP provider cannot receive them.
type Request struct {
	// SessionID identifies the proxied session.
	SessionID string
	// RequestID identifies the proxied request within the session.
	RequestID string
	// Elevated is true when the session's risk score is already elevated.
	Elevated bool
	// PreSignals are the preprocessing signals for this message, e.g.
	// "zero_width_removed" or "encoded_payload".
	PreSignals []string
	// Strict is true when the operator has enabled broad inline attempts.
	Strict bool
}

// Config configures the scheduler.
type Config struct {
	// Provider scores one window per call. Required.
	Provider decision.Provider
	// TokenCounter counts tokens for windowing and the inline token budget.
	// Required.
	TokenCounter decision.TokenCounter
	// Signals are asked when a caller passes none. Required, non-empty.
	Signals []decision.Signal

	// MaxConcurrency is the physical worker pool size. Inline and async work
	// draw from the same pool, which is what keeps a timed-out inference
	// from creating an unbounded compute backlog: the worker it still
	// occupies is a worker inline admission will not find free.
	MaxConcurrency int

	// InlineTimeout is the one global deadline for an inline assessment. It
	// covers admission, windowing, tokenization and inference together.
	InlineTimeout time.Duration

	// MaxInlineTokens caps the tokens one request may send inline. Windows
	// past it are denied with DenyInlineBudgetSpent.
	MaxInlineTokens int
	// MaxInlineWindows caps the windows one request may score inline.
	// Windows past it are denied with DenyInlineBudgetSpent.
	//
	// Windows within one request run sequentially under the one global
	// deadline, so with N inline windows each gets roughly InlineTimeout/N
	// of wall time in the worst case. A request holds one worker at a time
	// and returns it between windows; another request can take that slot in
	// the gap, which surfaces as DenyNoWorkerAvailable for a later window of
	// this request.
	MaxInlineWindows int
	// MaxAsyncWindows caps the windows one request may queue async (Task 20).
	MaxAsyncWindows int
	// AsyncQueueSize is the async job queue capacity (Task 20). Must be at
	// least 1.
	AsyncQueueSize int

	// AsyncTimeout bounds one async job's lifetime. It is not an operator
	// key: async work must be bounded, but its exact bound is not something
	// the configuration reference exposes. Defaults to 30x InlineTimeout,
	// clamped to [1s, 30s].
	AsyncTimeout time.Duration

	// MaxWindowTokens defaults to DefaultWindowTokens.
	MaxWindowTokens int

	// Admission selects which messages are eligible for the inline lane.
	Admission AdmissionPolicy

	// OnAsync is called when an async job completes. It must not block.
	OnAsync func(req Request, in decision.Input, a decision.Assessment)

	// Clock measures TotalLatency. Defaults to time.Now. The deadline itself
	// always runs on the real clock via context.WithTimeout.
	Clock func() time.Time
}

// Metrics is a snapshot of scheduler counters. Health output reports these,
// and they are what tells an operator whether to add replicas or lower
// concurrency — not a fixed requests-per-second claim. Metrics never affect
// a decision.
type Metrics struct {
	// InlineAttempted counts windows that were admitted and handed a worker.
	InlineAttempted int64
	// InlineCompleted counts admitted windows that produced at least one
	// answered decision before the deadline. Attempted minus Completed is
	// the count of timeouts, provider errors, provider panics and
	// all-unanswered results.
	InlineCompleted int64
	// InlineDenied counts windows that were not admitted, for any reason.
	InlineDenied int64
	// AsyncQueued counts async jobs accepted (Task 20).
	AsyncQueued int64
	// AsyncCompleted counts async jobs finished (Task 20).
	AsyncCompleted int64
	// AsyncDropped counts async jobs rejected or abandoned (Task 20).
	AsyncDropped int64
	// AsyncQueueDepth is the current async queue length (Task 20).
	AsyncQueueDepth int
	// DuplicatesSuppressed counts async jobs suppressed as duplicates
	// (Task 20).
	DuplicatesSuppressed int64
	// MaxInFlight is the highest number of concurrent provider calls seen.
	MaxInFlight int64
	// AdmissionReasons is a copy of the per-reason admission counts.
	AdmissionReasons map[decision.AdmissionReason]int64
	// InlinePanics counts provider panics recovered on a worker. Each one is
	// also an unanswered window, so it is included in InlineAttempted minus
	// InlineCompleted.
	InlinePanics int64
	// InFlight is a gauge: the provider calls running right now, including
	// calls a caller already abandoned at its deadline that still hold a
	// worker.
	InFlight int64
}

// Inline is the scheduler. It is safe for concurrent use.
type Inline struct {
	cfg Config

	// workers is the physical worker pool. An available slot is a token in
	// this channel; a non-blocking receive is how inline admission asks
	// "is a worker free right now?" without ever waiting.
	workers chan struct{}

	inlineAttempted atomic.Int64
	inlineCompleted atomic.Int64
	inlineDenied    atomic.Int64
	asyncQueued     atomic.Int64
	asyncCompleted  atomic.Int64
	asyncDropped    atomic.Int64
	duplicates      atomic.Int64
	inFlight        atomic.Int64
	maxInFlight     atomic.Int64
	inlinePanics    atomic.Int64

	reasonsMu sync.Mutex
	reasons   map[decision.AdmissionReason]int64
}

// New validates the configuration and constructs a scheduler.
func New(cfg Config) (*Inline, error) {
	if cfg.Provider == nil {
		return nil, errors.New("scheduler: Provider is required")
	}
	if cfg.TokenCounter == nil {
		return nil, errors.New("scheduler: TokenCounter is required")
	}
	if len(cfg.Signals) == 0 {
		return nil, errors.New("scheduler: at least one Signal is required")
	}
	if cfg.MaxConcurrency < 1 {
		return nil, fmt.Errorf("scheduler: MaxConcurrency must be at least 1, got %d", cfg.MaxConcurrency)
	}
	if cfg.InlineTimeout <= 0 {
		return nil, fmt.Errorf("scheduler: InlineTimeout must be positive, got %v", cfg.InlineTimeout)
	}
	if cfg.MaxInlineWindows < 1 {
		return nil, fmt.Errorf("scheduler: MaxInlineWindows must be at least 1, got %d", cfg.MaxInlineWindows)
	}
	if cfg.MaxInlineTokens < 1 {
		return nil, fmt.Errorf("scheduler: MaxInlineTokens must be at least 1, got %d", cfg.MaxInlineTokens)
	}
	if cfg.AsyncQueueSize < 1 {
		return nil, fmt.Errorf("scheduler: AsyncQueueSize must be at least 1, got %d", cfg.AsyncQueueSize)
	}
	if cfg.MaxAsyncWindows < 0 {
		return nil, fmt.Errorf("scheduler: MaxAsyncWindows must not be negative, got %d", cfg.MaxAsyncWindows)
	}
	if cfg.MaxWindowTokens <= 0 {
		cfg.MaxWindowTokens = DefaultWindowTokens
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.AsyncTimeout <= 0 {
		cfg.AsyncTimeout = 30 * cfg.InlineTimeout
		if cfg.AsyncTimeout < time.Second {
			cfg.AsyncTimeout = time.Second
		}
		if cfg.AsyncTimeout > 30*time.Second {
			cfg.AsyncTimeout = 30 * time.Second
		}
	}
	// Copy the signal list so a caller mutating its slice cannot change
	// what a running scheduler asks.
	cfg.Signals = append([]decision.Signal(nil), cfg.Signals...)

	s := &Inline{
		cfg:     cfg,
		workers: make(chan struct{}, cfg.MaxConcurrency),
		reasons: make(map[decision.AdmissionReason]int64),
	}
	for i := 0; i < cfg.MaxConcurrency; i++ {
		s.workers <- struct{}{}
	}
	return s, nil
}

// Assess implements decision.Scheduler. It treats the whole input as one
// original candidate and supplies a zero Request, so admission depends only
// on the content and the source role.
func (s *Inline) Assess(ctx context.Context, in decision.Input, signals []decision.Signal) (decision.Assessment, error) {
	cands := []decision.Candidate{{Content: in.Content, StartByte: 0, EndByte: len(in.Content)}}
	return s.AssessCandidates(ctx, Request{}, in, cands, signals)
}

// AssessCandidates scores the candidates for one message under one global
// deadline and composes the Assessment.
//
// Admission is zero-queue: a window is scored inline only if a worker is
// free at the instant it is considered. Windows run one at a time on the
// caller's behalf, each returning its worker before the next is admitted,
// so one request never holds more than one worker.
//
// Coverage is window-level: a window counts as scored (ScoredInline,
// ScoredBytes) when ANY requested signal on it was answered. Coverage.Complete
// therefore means "every eligible window produced at least one answer", not
// "every signal was answered on every window"; per-signal answeredness is
// read from the decisions themselves (MaxProbability, ByWindow).
//
// It never returns an error for a denied admission, a timeout, a provider
// error or a provider panic: all of those are "unknown", which is a
// coverage and answeredness fact, not a request failure.
func (s *Inline) AssessCandidates(ctx context.Context, req Request, in decision.Input, cands []decision.Candidate, signals []decision.Signal) (decision.Assessment, error) {
	if len(signals) == 0 {
		signals = s.cfg.Signals
	}

	start := s.cfg.Clock()
	// One global deadline. Everything below draws from this budget; a
	// shorter caller deadline wins because WithTimeout never extends.
	ctx, cancel := context.WithTimeout(ctx, s.cfg.InlineTimeout)
	defer cancel()

	var a decision.Assessment
	a.Scope = decision.ScopeCurrentRequest

	// Window every candidate, then order the whole set together so a
	// suspicious derived representation can outrank a bland original window.
	var all []WindowedText
	for _, c := range cands {
		all = append(all, SplitWindows(c, s.cfg.TokenCounter, s.cfg.MaxWindowTokens)...)
	}
	a.Coverage.EligibleWindows = len(all)
	for _, w := range all {
		a.Coverage.EligibleBytes += len(w.Text)
	}
	if len(all) == 0 {
		a.Coverage.Complete = a.Coverage.IsComplete()
		a.TotalLatency = s.cfg.Clock().Sub(start)
		return a, nil
	}
	ordered := OrderWindows(all)

	eligible, eligibleReason := s.eligible(req, in, ordered)

	var inlineWindows, inlineTokens int
	for _, w := range ordered {
		// 1. Is this message eligible for inline at all?
		if !eligible {
			s.deny(&a, w, eligibleReason)
			continue
		}
		// 2. Is there inline budget left?
		if inlineWindows >= s.cfg.MaxInlineWindows || inlineTokens+w.Tokens > s.cfg.MaxInlineTokens {
			s.deny(&a, w, decision.DenyInlineBudgetSpent)
			continue
		}
		// 3. Is any of the deadline left?
		if ctx.Err() != nil {
			s.deny(&a, w, decision.DenyDeadlineSpent)
			continue
		}
		// 4. Is a physical worker free RIGHT NOW? Never wait.
		select {
		case <-s.workers:
		default:
			s.deny(&a, w, decision.DenyNoWorkerAvailable)
			continue
		}

		inlineWindows++
		inlineTokens += w.Tokens
		s.inlineAttempted.Add(1)
		s.note(&a, w, true, eligibleReason)

		// runOnWorker takes ownership of the worker token acquired above.
		ds, ok := s.runOnWorker(ctx, in, w, signals)
		if ok {
			s.inlineCompleted.Add(1)
			a.Coverage.ScoredInline++
			a.Coverage.ScoredBytes += len(w.Text)
		}
		a.Decisions = append(a.Decisions, ds...)
	}

	a.Coverage.Complete = a.Coverage.IsComplete()
	a.TotalLatency = s.cfg.Clock().Sub(start)
	return a, nil
}

// eligible decides whether this message may attempt the inline fast lane,
// and returns the reason — an admit reason when eligible, DenyNotEligible
// otherwise.
//
// Trusted content is excluded: only user messages and tool results are
// considered, which is the Phase 1 scope. System and assistant content is
// not analyzed unless an operator configures it, which Phase 1 does not
// expose.
func (s *Inline) eligible(req Request, in decision.Input, ws []WindowedText) (bool, decision.AdmissionReason) {
	switch in.SourceRole {
	case "user", "tool", "":
	default:
		return false, decision.DenyNotEligible
	}

	p := s.cfg.Admission
	if p.UntrustedToolResults && in.SourceRole == "tool" {
		return true, decision.AdmitUntrustedToolResult
	}
	if p.EncodedOrObfuscated && len(req.PreSignals) > 0 {
		return true, decision.AdmitEncodedOrObfuscated
	}
	if p.WeakInjectionSignal {
		for _, w := range ws {
			if SuspicionScore(w.Text) > 0 {
				return true, decision.AdmitWeakInjectionSignal
			}
		}
	}
	if p.ElevatedSessionRisk && req.Elevated {
		return true, decision.AdmitElevatedSessionRisk
	}
	if p.BroadStrictMode && req.Strict {
		return true, decision.AdmitBroadStrictMode
	}
	return false, decision.DenyNotEligible
}

// workerResult is what a worker goroutine hands back to the caller.
type workerResult struct {
	ds  []decision.Decision
	err error
}

// errProviderPanic marks a recovered provider panic.
var errProviderPanic = errors.New("scheduler: provider panicked")

// runOnWorker runs one provider call on a worker goroutine that owns the
// worker token the caller acquired, and waits for it only until the global
// deadline. Every failure mode becomes unanswered decisions.
//
// The caller waits on a select over the result and ctx.Done(), so the
// deadline bounds the caller's wait even for a provider that ignores ctx.
// The token, by contrast, is held until Decide actually returns: the
// deadline does not prove the backend stopped burning CPU, and a token still
// held is a worker the next inline admission will not find free. That is
// what keeps abandoned inference from becoming an unbounded backlog.
//
// A provider panic is recovered on the worker goroutine, where an
// unrecovered panic would crash the process, and reported as an error.
func (s *Inline) runOnWorker(ctx context.Context, in decision.Input, w WindowedText, signals []decision.Signal) ([]decision.Decision, bool) {
	cur := s.inFlight.Add(1)
	for {
		m := s.maxInFlight.Load()
		if cur <= m || s.maxInFlight.CompareAndSwap(m, cur) {
			break
		}
	}

	windowed := in
	windowed.Content = w.Text
	done := make(chan workerResult, 1)
	go func() {
		var r workerResult
		defer func() {
			if p := recover(); p != nil {
				s.inlinePanics.Add(1)
				// Never log the panic value: a provider's panic message can
				// quote request content. The type and a short hash are enough
				// to group recurrences without disclosing anything.
				slog.Error("semantic inference panicked; decision is unknown",
					"panic_type", fmt.Sprintf("%T", p),
					"panic_hash", panicHash(p),
					"transform", w.Window.Transform,
				)
				r = workerResult{err: errProviderPanic}
			}
			// Release before publishing the result, so a caller that moves
			// on to its next window finds this worker free again rather
			// than racing the release into a spurious no_worker denial.
			s.inFlight.Add(-1)
			s.workers <- struct{}{}
			done <- r
		}()
		r.ds, r.err = s.cfg.Provider.Decide(ctx, windowed, signals)
	}()

	var r workerResult
	select {
	case r = <-done:
	case <-ctx.Done():
		// Prefer a result that arrived together with the deadline.
		select {
		case r = <-done:
		default:
			slog.Debug("semantic inference abandoned at the deadline",
				"error_class", errorClass(ctx.Err()),
				"transform", w.Window.Transform,
			)
			return unanswered(signals, w.Window), false
		}
	}
	if r.err != nil {
		slog.Debug("semantic inference did not answer",
			"error_class", errorClass(r.err),
			"transform", w.Window.Transform,
		)
		return unanswered(signals, w.Window), false
	}
	return normalize(r.ds, signals, w.Window)
}

// errorClass classifies a provider error for logging without its message,
// which a provider may have built from request content.
func errorClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, errProviderPanic):
		return "provider_panic"
	default:
		return fmt.Sprintf("%T", err)
	}
}

// panicHash returns the first 12 hex characters of the SHA-256 of a panic
// value's printed form: stable for grouping, useless for recovering content.
func panicHash(p any) string {
	sum := sha256.Sum256([]byte(fmt.Sprint(p)))
	return hex.EncodeToString(sum[:])[:12]
}

// normalize turns a provider's output into exactly one decision per
// requested signal, in request order, stamped with the window: a Provider
// scores one window and does not know where it came from. A signal the
// provider omitted, or answered with a probability outside [0, 1] or NaN,
// is unanswered rather than absent or trusted, and every unanswered
// decision carries Probability 0 so no stale value can be read off it.
//
// ok reports whether ANY requested signal was answered. That is the
// window-level definition of "scored" that Coverage uses: a window with at
// least one answer counts as scored even if other signals on it are
// unanswered, and a window with no answers does not count.
func normalize(out []decision.Decision, signals []decision.Signal, w decision.Window) ([]decision.Decision, bool) {
	res := make([]decision.Decision, 0, len(signals))
	var ok bool
	for _, sig := range signals {
		d := decision.Decision{Signal: sig}
		for _, o := range out {
			if o.Signal == sig {
				d = o
				break
			}
		}
		if d.Answered && (d.Probability < 0 || d.Probability > 1 || math.IsNaN(d.Probability)) {
			d.Answered = false
		}
		if !d.Answered {
			d.Probability = 0
		}
		d.Window = w
		ok = ok || d.Answered
		res = append(res, d)
	}
	return res, ok
}

// unanswered builds one unanswered decision per signal for a window.
func unanswered(signals []decision.Signal, w decision.Window) []decision.Decision {
	out := make([]decision.Decision, 0, len(signals))
	for _, sig := range signals {
		out = append(out, decision.Decision{Signal: sig, Window: w})
	}
	return out
}

// deny records a denied admission. In this task denied windows are recorded
// and counted; Task 20 also enqueues them for async continuation.
func (s *Inline) deny(a *decision.Assessment, w WindowedText, reason decision.AdmissionReason) {
	s.inlineDenied.Add(1)
	s.note(a, w, false, reason)
}

// note appends an admission record and counts its reason.
func (s *Inline) note(a *decision.Assessment, w WindowedText, admitted bool, reason decision.AdmissionReason) {
	a.Admissions = append(a.Admissions, decision.Admission{
		Window:   w.Window,
		Admitted: admitted,
		Reason:   reason,
	})
	s.reasonsMu.Lock()
	s.reasons[reason]++
	s.reasonsMu.Unlock()
}

// Metrics returns a snapshot. The reason map is copied, so a caller cannot
// mutate scheduler state through it.
func (s *Inline) Metrics() Metrics {
	s.reasonsMu.Lock()
	reasons := make(map[decision.AdmissionReason]int64, len(s.reasons))
	for k, v := range s.reasons {
		reasons[k] = v
	}
	s.reasonsMu.Unlock()

	return Metrics{
		InlineAttempted:      s.inlineAttempted.Load(),
		InlineCompleted:      s.inlineCompleted.Load(),
		InlineDenied:         s.inlineDenied.Load(),
		AsyncQueued:          s.asyncQueued.Load(),
		AsyncCompleted:       s.asyncCompleted.Load(),
		AsyncDropped:         s.asyncDropped.Load(),
		DuplicatesSuppressed: s.duplicates.Load(),
		MaxInFlight:          s.maxInFlight.Load(),
		AdmissionReasons:     reasons,
		InlinePanics:         s.inlinePanics.Load(),
		InFlight:             s.inFlight.Load(),
	}
}

// Compile-time proof that Inline satisfies the spec's Scheduler contract.
var _ decision.Scheduler = (*Inline)(nil)
