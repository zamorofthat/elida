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
	// MaxAsyncWindows caps the windows one request may queue for async
	// continuation. Zero is valid and disables async continuation.
	MaxAsyncWindows int
	// AsyncQueueSize is the async job queue capacity. Must be at least 1. A
	// full queue drops the window (DenyQueueFull) rather than blocking.
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

	// OnAsync is called on an async worker when an async job completes. Its
	// Assessment always has Scope decision.ScopeFutureActivity. It must not
	// block: the worker running it consumes no further queued jobs until it
	// returns, and Shutdown's drain waits for it. A panic in it is recovered.
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
	// AsyncQueued counts windows accepted onto the async queue. Every
	// queued job ends as exactly one of AsyncCompleted or AsyncCanceled.
	AsyncQueued int64
	// AsyncCompleted counts async jobs that ran and were delivered to
	// OnAsync, answered or not (a provider error or panic is a delivered
	// unknown, not a cancellation).
	AsyncCompleted int64
	// AsyncDropped counts windows the async queue refused: the queue was
	// full, or the scheduler was shut down. Each is a coverage gap.
	AsyncDropped int64
	// AsyncQueueDepth is the current async queue length.
	AsyncQueueDepth int
	// DuplicatesSuppressed counts windows not queued because the same job
	// (decision.JobID) was already claimed inline or async.
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
	// AsyncCanceled counts queued jobs whose context ended (AsyncTimeout or
	// Shutdown) before they produced an answer. Like the embedded provider's
	// Canceled count, these are budget outcomes, not provider failures, and
	// they are not delivered to OnAsync.
	AsyncCanceled int64
	// AsyncPanics counts provider panics recovered on an async job. Each is
	// delivered to OnAsync as an unknown and counted in AsyncCompleted.
	AsyncPanics int64
}

// DedupHistory is how many recent job identities are remembered for
// deduplication. It is a fixed ring, not a per-session map: unbounded
// retained history is exactly the failure mode the bounds exist to prevent.
// At 4096 entries it covers far more than any single session's windows while
// costing a few hundred kilobytes.
const DedupHistory = 4096

// asyncJob is one window waiting for an async worker.
type asyncJob struct {
	req    Request
	in     decision.Input
	win    WindowedText
	sigs   []decision.Signal
	denied decision.AdmissionReason // why the window missed the inline lane
}

// Inline is the scheduler. It is safe for concurrent use.
type Inline struct {
	cfg Config

	// workers is the physical worker pool. An available slot is a token in
	// this channel; a non-blocking receive is how inline admission asks
	// "is a worker free right now?" without ever waiting.
	workers chan struct{}

	// queue is the bounded async continuation queue. A full queue drops the
	// job (counted and reported) rather than blocking the hot path.
	queue chan asyncJob
	// lifeMu guards closed and the close of queue: enqueue sends under the
	// read lock, Shutdown closes under the write lock, so a send can never
	// race the close into a panic.
	lifeMu sync.RWMutex
	closed bool
	// baseCtx is the lifetime of async work. Shutdown cancels it when the
	// drain finishes or its deadline passes, so a job cannot outlive it.
	baseCtx      context.Context
	cancelBase   context.CancelFunc
	asyncWG      sync.WaitGroup
	shutdownOnce sync.Once
	// drained is closed once every async worker has exited after Shutdown.
	drained chan struct{}

	// seen and seenRing are the bounded deduplication set: seen maps a job
	// ID to its ring slot, and the ring evicts the oldest ID on wrap.
	seenMu   sync.Mutex
	seen     map[string]int
	seenRing []string
	seenNext int

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
	asyncCanceled   atomic.Int64
	asyncPanics     atomic.Int64

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

	baseCtx, cancelBase := context.WithCancel(context.Background())
	s := &Inline{
		cfg:        cfg,
		workers:    make(chan struct{}, cfg.MaxConcurrency),
		queue:      make(chan asyncJob, cfg.AsyncQueueSize),
		baseCtx:    baseCtx,
		cancelBase: cancelBase,
		drained:    make(chan struct{}),
		seen:       make(map[string]int, DedupHistory),
		seenRing:   make([]string, DedupHistory),
		reasons:    make(map[decision.AdmissionReason]int64),
	}
	for i := 0; i < cfg.MaxConcurrency; i++ {
		s.workers <- struct{}{}
	}
	// One async worker per physical worker. They contend for the same
	// worker tokens as inline work, which is what keeps total inference
	// concurrency at MaxConcurrency regardless of where work came from.
	for i := 0; i < cfg.MaxConcurrency; i++ {
		s.asyncWG.Add(1)
		go s.asyncWorker()
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

	var inlineWindows, inlineTokens, asyncWindows int
	for _, w := range ordered {
		// 1. Is this message eligible at all? not_eligible means we are not
		// analyzing this content, so it is never queued either: async
		// capacity is for work we wanted to do and could not, not for work
		// we declined.
		if !eligible {
			s.deny(&a, w, eligibleReason)
			continue
		}

		var denied decision.AdmissionReason
		switch {
		// 2. Is there inline budget left?
		case inlineWindows >= s.cfg.MaxInlineWindows || inlineTokens+w.Tokens > s.cfg.MaxInlineTokens:
			denied = decision.DenyInlineBudgetSpent
		// 3. Is any of the deadline left?
		case ctx.Err() != nil:
			denied = decision.DenyDeadlineSpent
		// 4. Is a physical worker free RIGHT NOW? Never wait.
		default:
			select {
			case <-s.workers:
			default:
				denied = decision.DenyNoWorkerAvailable
			}
		}
		if denied != "" {
			// 5. Bounded async continuation for capacity denials.
			if s.continueAsync(&a, req, in, w, signals, denied, asyncWindows) {
				asyncWindows++
			}
			continue
		}

		inlineWindows++
		inlineTokens += w.Tokens
		s.inlineAttempted.Add(1)
		s.note(&a, w, true, eligibleReason)
		// Claim the job so a retry of this request does not queue this
		// window async after it was already scored inline. The inline lane
		// itself never consults the claim: protecting the current request
		// outranks deduplication, and decision IDs collapse the repeat.
		if id := jobIDFor(req, in, w); id != "" {
			s.claim(id)
		}

		// runOnWorker takes ownership of the worker token acquired above.
		ds, ok := s.runOnWorker(ctx, in, w, signals, &s.inlinePanics)
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
//
// panics is the counter a recovered panic increments, so inline and async
// panics are reported separately.
func (s *Inline) runOnWorker(ctx context.Context, in decision.Input, w WindowedText, signals []decision.Signal, panics *atomic.Int64) ([]decision.Decision, bool) {
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
				panics.Add(1)
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

// deny records a denied admission that is not continued asynchronously.
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
		AsyncQueueDepth:      len(s.queue),
		DuplicatesSuppressed: s.duplicates.Load(),
		MaxInFlight:          s.maxInFlight.Load(),
		AdmissionReasons:     reasons,
		InlinePanics:         s.inlinePanics.Load(),
		InFlight:             s.inFlight.Load(),
		AsyncCanceled:        s.asyncCanceled.Load(),
		AsyncPanics:          s.asyncPanics.Load(),
	}
}

// continueAsync offers a window that was denied the inline lane for a
// capacity reason (budget, deadline or worker) to the bounded async queue,
// and records its admission. It reports whether the window was queued.
//
// It never blocks. A window past MaxAsyncWindows, or a duplicate of a job
// already claimed, keeps its capacity denial reason; a window the queue
// refuses is recorded as DenyQueueFull. Either way it is a coverage gap.
func (s *Inline) continueAsync(a *decision.Assessment, req Request, in decision.Input, w WindowedText, sigs []decision.Signal, denied decision.AdmissionReason, queuedSoFar int) bool {
	if queuedSoFar >= s.cfg.MaxAsyncWindows {
		s.deny(a, w, denied)
		return false
	}
	id := jobIDFor(req, in, w)
	if id != "" && !s.claim(id) {
		s.duplicates.Add(1)
		s.deny(a, w, denied)
		return false
	}
	job := asyncJob{req: req, in: in, win: w, sigs: sigs, denied: denied}
	if !s.enqueue(job) {
		// Release the claim: the job never ran, so a retry must be free to
		// queue it rather than be suppressed as a duplicate of nothing.
		if id != "" {
			s.unclaim(id)
		}
		s.deny(a, w, decision.DenyQueueFull)
		return false
	}
	a.Coverage.QueuedAsync++
	s.deny(a, w, denied)
	return true
}

// jobIDFor derives the stable dedup key for one window of one request. It
// returns "" for a request with no session or request identity (Assess):
// without identity, equal content from unrelated callers would collide, and
// suppressing those as duplicates would silently skip analysis.
func jobIDFor(req Request, in decision.Input, w WindowedText) string {
	if req.SessionID == "" && req.RequestID == "" {
		return ""
	}
	return decision.JobID(decision.JobIdentity{
		SessionID:      req.SessionID,
		RequestID:      req.RequestID,
		MessageIndex:   in.MessageIndex,
		StartByte:      w.Window.StartByte,
		EndByte:        w.Window.EndByte,
		TransformChain: w.Window.Transform,
	})
}

// claim records a job identity and reports whether it is new.
//
// The ring makes the history bounded: the oldest identity is evicted when
// the ring wraps. An evicted identity could in principle be re-scored, which
// is acceptable — DedupHistory is far larger than one session's window
// count, and the alternative is an unbounded map keyed by session.
func (s *Inline) claim(id string) bool {
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	if _, dup := s.seen[id]; dup {
		return false
	}
	slot := s.seenNext
	if old := s.seenRing[slot]; old != "" {
		// Only evict the map entry if it still points at this slot; an ID
		// that was unclaimed and re-claimed lives in a newer slot.
		if at, ok := s.seen[old]; ok && at == slot {
			delete(s.seen, old)
		}
	}
	s.seenRing[slot] = id
	s.seenNext = (slot + 1) % len(s.seenRing)
	s.seen[id] = slot
	return true
}

// unclaim forgets a job identity whose job never ran.
func (s *Inline) unclaim(id string) {
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	if slot, ok := s.seen[id]; ok {
		delete(s.seen, id)
		s.seenRing[slot] = ""
	}
}

// enqueue offers a job to the async queue and reports whether it was
// accepted.
//
// It never blocks: a full queue is a dropped window, counted and logged
// without content, not back-pressure on the proxied request. After Shutdown
// it refuses everything. The read lock is held across the non-blocking send
// so Shutdown cannot close the queue underneath it.
func (s *Inline) enqueue(job asyncJob) bool {
	s.lifeMu.RLock()
	defer s.lifeMu.RUnlock()
	if s.closed {
		s.asyncDropped.Add(1)
		return false
	}
	// Count before the send so a fast worker can never make AsyncCompleted
	// exceed AsyncQueued in a snapshot.
	s.asyncQueued.Add(1)
	select {
	case s.queue <- job:
		return true
	default:
		s.asyncQueued.Add(-1)
		s.asyncDropped.Add(1)
		slog.Warn("semantic async queue is full; window dropped",
			"session_id", job.req.SessionID,
			"request_id", job.req.RequestID,
			"queue_size", cap(s.queue),
			"transform", job.win.Window.Transform,
			"consequence", "this window is a coverage gap and contributes no risk",
		)
		return false
	}
}

// asyncWorker runs queued jobs until Shutdown closes the queue.
func (s *Inline) asyncWorker() {
	defer s.asyncWG.Done()
	for job := range s.queue {
		s.runAsync(job)
	}
}

// runAsync runs one queued job.
//
// The job gets its own context bounded by AsyncTimeout and by Shutdown,
// waits for a physical worker token (unlike inline, async work may wait),
// and reports through OnAsync with ScopeFutureActivity: by the time it
// finishes the request has been forwarded, so the result can raise session
// risk and affect later activity but can never claim to have protected the
// current request.
//
// A job whose context ends first — while waiting for a worker or during
// inference — is counted in AsyncCanceled and not delivered: that is a
// budget outcome, not a provider failure, and an all-unknown result carries
// nothing a later decision could use.
func (s *Inline) runAsync(job asyncJob) {
	if s.baseCtx.Err() != nil {
		s.asyncCanceled.Add(1)
		return
	}
	start := s.cfg.Clock()
	ctx, cancel := context.WithTimeout(s.baseCtx, s.cfg.AsyncTimeout)
	defer cancel()

	select {
	case <-s.workers:
	case <-ctx.Done():
		s.asyncCanceled.Add(1)
		return
	}
	// select picks randomly when both cases are ready; do not start
	// inference on a context that has already ended.
	if ctx.Err() != nil {
		s.workers <- struct{}{}
		s.asyncCanceled.Add(1)
		return
	}

	// runOnWorker takes ownership of the worker token acquired above.
	ds, ok := s.runOnWorker(ctx, job.in, job.win, job.sigs, &s.asyncPanics)
	if !ok && ctx.Err() != nil {
		s.asyncCanceled.Add(1)
		return
	}

	a := decision.Assessment{
		Decisions: ds,
		Scope:     decision.ScopeFutureActivity,
		Coverage: decision.Coverage{
			EligibleWindows: 1,
			EligibleBytes:   len(job.win.Text),
		},
		// The admission record repeats why the window missed the inline
		// lane; an async result never claims inline admission.
		Admissions: []decision.Admission{{
			Window:   job.win.Window,
			Admitted: false,
			Reason:   job.denied,
		}},
	}
	if ok {
		a.Coverage.ScoredBytes = len(job.win.Text)
	}
	// Coverage.ScoredInline stays 0, so IsComplete is false: an async result
	// never reports a clean scan of the request it arrived too late for.
	// Whether this window answered is read from the decisions and
	// ScoredBytes.
	a.Coverage.Complete = a.Coverage.IsComplete()
	a.TotalLatency = s.cfg.Clock().Sub(start)

	// Count before delivery so a callback that signals completion observes
	// an up-to-date AsyncCompleted.
	s.asyncCompleted.Add(1)
	s.deliver(job, a)
}

// deliver hands an async result to OnAsync, recovering a callback panic so
// one bad sink cannot kill a worker or the process. The panic value is never
// logged: it could quote the content the callback was handed.
func (s *Inline) deliver(job asyncJob, a decision.Assessment) {
	if s.cfg.OnAsync == nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			slog.Error("semantic async callback panicked; result was not recorded",
				"panic_type", fmt.Sprintf("%T", p),
				"panic_hash", panicHash(p),
				"transform", job.win.Window.Transform,
			)
		}
	}()
	s.cfg.OnAsync(job.req, job.in, a)
}

// Shutdown stops accepting new async work, drains the queue within the
// caller's deadline, and then cancels any job still waiting or running.
//
// It returns nil when every async worker has exited, and ctx.Err() when the
// deadline passed first, so the caller can log that some semantic analysis
// was abandoned rather than silently losing it. After the deadline the
// remaining queued jobs are counted in AsyncCanceled and the workers exit
// promptly; a provider call that ignores its context may still finish in
// the background, exactly as on the inline path.
//
// Inline assessment keeps working after Shutdown; only async continuation
// stops. Calling Shutdown more than once is safe: a later call waits, under
// its own context, for any workers an earlier call left behind.
func (s *Inline) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() {
		s.lifeMu.Lock()
		s.closed = true
		close(s.queue)
		s.lifeMu.Unlock()

		go func() {
			s.asyncWG.Wait()
			close(s.drained)
		}()
	})

	// Prefer a finished drain over an expired context when both are ready.
	select {
	case <-s.drained:
		s.cancelBase()
		return nil
	default:
	}
	select {
	case <-s.drained:
		s.cancelBase()
		return nil
	case <-ctx.Done():
		s.cancelBase()
		slog.Warn("semantic scheduler shutdown deadline passed with async work outstanding",
			"queue_depth", len(s.queue),
			"error_class", errorClass(ctx.Err()),
		)
		return ctx.Err()
	}
}

// Compile-time proof that Inline satisfies the spec's Scheduler contract.
var _ decision.Scheduler = (*Inline)(nil)
