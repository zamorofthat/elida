package decision

import (
	"context"
	"time"
)

// Signal is one of the fixed security questions a Provider may answer.
type Signal string

const (
	// SignalInjection is the main head: content attempts to manipulate the
	// assistant's instructions.
	SignalInjection Signal = "injection"
	// SignalHumanDirected is the auxiliary head: imperative or obligation
	// phrasing aimed at a human reader (an email, a runbook, a code
	// comment) rather than at the assistant. It is a veto on
	// SignalInjection, never a detector in its own right, and never creates
	// a violation alone.
	SignalHumanDirected Signal = "human_directed"
	// SignalCompliance is reserved for a response-side head that has not
	// been trained, evaluated or calibrated. No Phase 1 provider supports
	// it; requesting it yields an unanswered decision.
	SignalCompliance Signal = "compliance"
)

// Direction is which side of the proxied exchange content came from.
type Direction string

const (
	// DirectionRequest is content flowing from the client toward the model.
	DirectionRequest Direction = "request"
	// DirectionResponse is content flowing from the model back to the client.
	DirectionResponse Direction = "response"
)

// Input is one unit of content to score. It deliberately carries no session
// ID, request ID or any other internal identifier.
type Input struct {
	Content      string
	Direction    Direction
	SourceRole   string // "user", "assistant", "system", "tool"
	MessageIndex int
}

// Window locates scored content inside the original message. StartByte and
// EndByte are always offsets into the ORIGINAL content, even for a derived
// representation, so evidence always points at real bytes the operator can
// find. A decode cannot be byte-mapped in reverse, so every window of a
// derived representation reports its ancestor's full original range there.
//
// LocalStartByte and LocalEndByte are the window's offsets within the text
// of the representation it was scored from. They are what tells the windows
// of one derived representation apart, and an operator can reproduce a
// derived window by applying Transform to the original and slicing these
// offsets. For the original representation they equal StartByte and EndByte
// less the representation's own start (normally 0, so local == absolute).
//
// Transform is the transformation chain that produced the scored text ("" for
// the original) and TransformDepth is how many decodes deep it is. Together
// with the local offsets, a Window is unique within one message, which is
// what makes it a correct map key for ByWindow.
type Window struct {
	StartByte      int
	EndByte        int
	LocalStartByte int
	LocalEndByte   int
	Transform      string
	TransformDepth int
}

// Decision is one Provider answer for one Signal over one Window.
//
// Answered is false when the provider does not support the signal or could
// not produce a result within the operation's constraints. When Answered is
// false, Probability carries no information and must not be read.
type Decision struct {
	Signal      Signal
	Probability float64
	Answered    bool
	Model       string
	Version     string
	Window      Window
	Latency     time.Duration
}

// Coverage records how much of the eligible content was actually scored.
// Partial coverage must never be reported as a clean full scan.
type Coverage struct {
	EligibleWindows int
	ScoredInline    int
	QueuedAsync     int
	EligibleBytes   int
	ScoredBytes     int
	// Complete is the authoritative answer to "was this a clean full scan?"
	// and is set only by the Scheduler, which is the one component that
	// knows whether every eligible window actually produced a decision.
	// Callers read this field; IsComplete is a validation aid, not a second
	// source of truth.
	Complete bool
}

// IsComplete recomputes completeness from the counts, for validation. It is
// not the authoritative answer — Coverage.Complete is — and the Scheduler
// must set Complete equal to IsComplete(); a disagreement between the two is
// a Scheduler bug, not a state callers are expected to reconcile.
//
// It is false when nothing was eligible: no analysis is not a clean scan.
func (c Coverage) IsComplete() bool {
	if c.EligibleWindows <= 0 || c.EligibleBytes <= 0 {
		return false
	}
	return c.ScoredInline >= c.EligibleWindows && c.ScoredBytes >= c.EligibleBytes
}

// ProtectionScope records what a result could still protect by the time it
// arrived.
type ProtectionScope string

const (
	// ScopeCurrentRequest: completed before forwarding; may affect this request.
	ScopeCurrentRequest ProtectionScope = "current_request"
	// ScopeFutureActivity: completed after forwarding; may affect only later
	// session activity. An async result never claims to have blocked or
	// recalled content that was already forwarded.
	ScopeFutureActivity ProtectionScope = "future_activity"
	// ScopeRemainingStream: completed after a response stream began; may
	// terminate only bytes not yet delivered. Reserved for Phase 2 — no
	// Phase 1 signal is asked of responses, so nothing produces it yet.
	ScopeRemainingStream ProtectionScope = "remaining_stream"
)

// AdmissionReason explains why one window did or did not get the inline fast
// lane. Health output reports the distribution of these.
type AdmissionReason string

const (
	// AdmitUntrustedToolResult admits a window to the inline fast lane
	// because its content came from a tool result rather than a human or
	// assistant turn: tool output is untrusted by default and gets the
	// synchronous check before anything forwards.
	AdmitUntrustedToolResult AdmissionReason = "untrusted_tool_result"
	// AdmitEncodedOrObfuscated admits a window inline because preprocessing
	// found an encoded or obfuscated representation (for example base64 or
	// unicode tricks) that a cheap check alone cannot clear.
	AdmitEncodedOrObfuscated AdmissionReason = "encoded_or_obfuscated"
	// AdmitWeakInjectionSignal admits a window inline because a fast, cheap
	// pre-check already found a weak but non-zero injection signal, so the
	// window gets a full answer rather than waiting on an async pass.
	AdmitWeakInjectionSignal AdmissionReason = "weak_injection_signal"
	// AdmitElevatedSessionRisk admits a window inline because the session
	// already carries elevated risk, even though the window would not
	// otherwise qualify on its own content alone.
	AdmitElevatedSessionRisk AdmissionReason = "elevated_session_risk"
	// AdmitBroadStrictMode admits a window inline because operator
	// configuration runs in a broad or strict admission mode that inlines
	// more windows than the default policy would.
	AdmitBroadStrictMode AdmissionReason = "broad_strict_mode"

	// DenyNotEligible means the window never qualified for scoring at all
	// (for example, content outside the signals' scope). It is not queued
	// async either; it is simply never scored.
	DenyNotEligible AdmissionReason = "not_eligible"
	// DenyNoWorkerAvailable means the window qualified for async scoring
	// but no worker was free to take it. The window goes unscored and
	// Coverage reports it as short.
	DenyNoWorkerAvailable AdmissionReason = "no_worker_available"
	// DenyInlineBudgetSpent means this request's inline token or time
	// budget was already spent by earlier windows. The window falls to the
	// async queue if one accepts it, otherwise goes unscored.
	DenyInlineBudgetSpent AdmissionReason = "inline_budget_spent"
	// DenyDeadlineSpent means the scheduler's global deadline had already
	// elapsed before this window could be scored. The window is skipped
	// rather than scored late.
	DenyDeadlineSpent AdmissionReason = "deadline_spent"
	// DenyQueueFull means the async queue was at capacity and could not
	// accept the window. The window goes unscored and Coverage reports it
	// as short.
	DenyQueueFull AdmissionReason = "async_queue_full"
	// DenyCapabilityAsyncOnly means the provider cannot meet an inline
	// budget on this build (the embedded provider's async_only capability:
	// any build other than linux/amd64 with GOEXPERIMENT=simd). The inline
	// lane is never tried; the window goes straight to the async lane in
	// suspicion order, bounded by the async cap like any capacity denial.
	DenyCapabilityAsyncOnly AdmissionReason = "capability_async_only"
)

// Admission is the admission controller's record for one window.
type Admission struct {
	Window   Window
	Admitted bool
	Reason   AdmissionReason
}

// Candidate is one analyzable representation of a message: the original, or
// a bounded derived representation from package preprocess. StartByte and
// EndByte locate it in the original content.
//
// Candidate exists so the scheduler never imports the preprocessing
// package, and so a derived representation can never be mistaken for
// content to forward.
type Candidate struct {
	Content        string
	Transform      string
	TransformDepth int
	StartByte      int
	EndByte        int
}

// TokenCounter counts model tokens. The embedded provider supplies a
// tokenizer-backed implementation; tests use a byte heuristic. The scheduler
// needs token counts for windowing and for the inline token budget without
// depending on any particular inference backend.
type TokenCounter interface {
	CountTokens(text string) int
}

// Assessment is the composed result for one Input. It is produced by a
// Scheduler, never by a Provider.
type Assessment struct {
	Decisions    []Decision
	Coverage     Coverage
	Scope        ProtectionScope
	TotalLatency time.Duration
	Admissions   []Admission
	// Outcome is how an async job ended. It is set only on assessments a
	// Scheduler delivers asynchronously, and every queued job is delivered
	// exactly once with a terminal Outcome, so a sink can always tell a
	// finished job from one still pending. Inline assessments leave it "".
	Outcome AsyncOutcome
	// ErrorClass classifies why an async job produced no answer
	// ("deadline_exceeded", "canceled", "provider_panic", or an error type
	// name). It never carries an error message, which could quote content.
	ErrorClass string
}

// AsyncOutcome is the terminal state of one async job.
type AsyncOutcome string

const (
	// AsyncAnswered: at least one requested signal was answered.
	AsyncAnswered AsyncOutcome = "answered"
	// AsyncUnanswered: the provider ran and answered nothing.
	AsyncUnanswered AsyncOutcome = "unanswered"
	// AsyncFailed: the provider returned an error or panicked.
	AsyncFailed AsyncOutcome = "failed"
	// AsyncCanceled: the job's context ended (its async timeout, or
	// scheduler shutdown) before it produced an answer. Its decisions are
	// all unanswered.
	AsyncCanceled AsyncOutcome = "canceled"
)

// MaxProbability returns the highest probability among answered decisions
// for one signal, and whether anything answered at all. Returning answered
// separately is what stops an unsupported, failed or timed-out signal from
// being read as safe.
//
// MaxProbability is for content-level reporting: the single number that
// describes a whole Input. It collapses every window, so it must NOT be used
// to evaluate the human_directed veto. That veto applies only to the
// injection score from the same invocation, window and representation, and a
// cross-window maximum would let one benign window rescue a window that is
// in fact injecting. Evaluate the veto per window via ByWindow.
func (a Assessment) MaxProbability(s Signal) (float64, bool) {
	var max float64
	var answered bool
	for _, d := range a.Decisions {
		if d.Signal != s || !d.Answered {
			continue
		}
		if !answered || d.Probability > max {
			max = d.Probability
		}
		answered = true
	}
	return max, answered
}

// ByWindow returns the answered decisions for one signal, keyed by the Window
// each one covered. Window is a struct of ints and a string, so it is
// comparable by value and is used as the map key directly. Its local offsets
// make it unique per window, including across the windows of one derived
// representation, which all share the ancestor's absolute range.
//
// This is the accessor the human_directed veto must use. The veto applies
// only to the injection score from the same invocation, window and
// representation, so pairing the two signals per window is the only correct
// way to evaluate it; MaxProbability cannot express that pairing.
//
// A window missing from the result was not answered for this signal, and an
// absent window must never be read as a low probability — the same rule
// MaxProbability's answered return enforces.
//
// If two decisions for the same signal share a window, the later one in
// Decisions wins. That is a duplicate the Scheduler should not emit; last
// wins only keeps this accessor total.
func (a Assessment) ByWindow(sig Signal) map[Window]Decision {
	out := make(map[Window]Decision)
	for _, d := range a.Decisions {
		if d.Signal != sig || !d.Answered {
			continue
		}
		out[d.Window] = d
	}
	return out
}

// Scheduler splits content into windows, runs providers inside one global
// deadline, and composes the Assessment.
type Scheduler interface {
	Assess(ctx context.Context, content Input, signals []Signal) (Assessment, error)
}

// Provider scores one Input (one window) for the requested signals.
//
// Implementations must be safe for concurrent use, must honor ctx, and must
// return a Decision with Answered false rather than an error for a signal
// they do not support.
type Provider interface {
	Name() string
	Supports(Signal) bool
	Decide(ctx context.Context, input Input, signals []Signal) ([]Decision, error)
}
