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
	DirectionRequest  Direction = "request"
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
// find. Transform is the transformation chain that produced the scored text
// ("" for the original) and TransformDepth is how many decodes deep it is.
type Window struct {
	StartByte      int
	EndByte        int
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
	Complete        bool
}

// IsComplete reports whether every eligible window and byte was scored. It
// is false when nothing was eligible: no analysis is not a clean scan.
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
	AdmitUntrustedToolResult AdmissionReason = "untrusted_tool_result"
	AdmitEncodedOrObfuscated AdmissionReason = "encoded_or_obfuscated"
	AdmitWeakInjectionSignal AdmissionReason = "weak_injection_signal"
	AdmitElevatedSessionRisk AdmissionReason = "elevated_session_risk"
	AdmitBroadStrictMode     AdmissionReason = "broad_strict_mode"

	DenyNotEligible       AdmissionReason = "not_eligible"
	DenyNoWorkerAvailable AdmissionReason = "no_worker_available"
	DenyInlineBudgetSpent AdmissionReason = "inline_budget_spent"
	DenyDeadlineSpent     AdmissionReason = "deadline_spent"
	DenyQueueFull         AdmissionReason = "async_queue_full"
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
}

// MaxProbability returns the highest probability among answered decisions
// for one signal, and whether anything answered at all.
//
// This is the only supported way to read a signal from an Assessment:
// returning answered separately is what stops an unsupported, failed or
// timed-out signal from being read as safe.
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
