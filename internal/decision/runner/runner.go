// Package runner connects preprocessing, the scheduler, the thresholds and
// the session. It is the only component the proxy sees.
//
// Modes:
//   - disabled: nothing runs, nothing is recorded.
//   - shadow:   decisions are recorded on the session for calibration. No
//     violations, no risk.
//   - audit:    evidence-only violations with diagnostic points (Task 28).
//   - enforce:  calibrated violations drive the existing ladder (Task 29).
//
// policy.mode caps decision.mode: with policy.mode audit, enforce behaves
// as audit. Nothing here maintains a risk score of its own.
//
// Unknown is never safe. A window the provider did not answer produces no
// verdict and no shadow entry; the runner never synthesizes a low
// probability from absence, and partial coverage is recorded as partial.
//
// Nothing in this package logs, records or returns request content.
package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"elida/internal/config"
	"elida/internal/decision"
	"elida/internal/decision/preprocess"
	"elida/internal/decision/scheduler"
	"elida/internal/session"
)

// Thresholds maps calibrated probabilities onto severities.
//
// These are not free-form numbers: they arrive from the loaded model's
// threshold-set artifact, and selecting production values is a reserved
// decision.
type Thresholds struct {
	// Main is the injection decision threshold from the threshold set.
	Main float64
	// Aux is the human_directed veto threshold. At or above it, an
	// injection score from the same window is rescued.
	Aux float64
	// Elevated emits evidence-only injection_elevated events (Task 28).
	Elevated float64
	// Warning maps a probability at or above it onto a warning severity.
	Warning float64
	// Critical maps a probability at or above it onto a critical severity.
	Critical float64
}

// ModelIdentity travels onto every recorded decision so a finding can always
// be traced to the exact model package that produced it.
type ModelIdentity struct {
	// Name is the model package name.
	Name string
	// Version is the model package version; it is part of every DecisionID.
	Version string
	// Checksum is the verified checksum of the loaded model artifact.
	Checksum string
	// ThresholdSet names the versioned threshold artifact in use.
	ThresholdSet string
}

// Message is one message to analyze. It mirrors policy.MessageToScan by
// value so this package does not depend on the policy engine in shadow mode.
type Message struct {
	// Role is "user", "assistant", "system" or "tool".
	Role string
	// Index is the position in the request's messages array (-1 for a
	// top-level system prompt).
	Index int
	// Content is the text to analyze. It is never logged or recorded.
	Content string
}

// Config configures the runner.
type Config struct {
	// Mode is the configured decision mode: disabled, shadow, audit or
	// enforce.
	Mode string
	// PolicyMode is policy.mode ("enforce" or "audit"); it caps Mode.
	PolicyMode string
	// Scheduler assesses content. If it also implements AssessCandidates
	// (as *scheduler.Inline does), every preprocessed representation is
	// assessed; otherwise only the original content is.
	Scheduler decision.Scheduler
	// Budget bounds analysis-only preprocessing.
	Budget preprocess.Budget
	// Signals are the signals asked of the scheduler.
	Signals []decision.Signal
	// Thresholds come from the loaded threshold set.
	Thresholds Thresholds
	// Model is recorded on every decision.
	Model ModelIdentity
	// Strict enables broad inline attempts; it reaches the scheduler's
	// admission controller as Request.Strict. It changes what gets scored,
	// never what a score means.
	Strict bool
	// RiskLookup reports the session's current risk score and ladder action.
	// It is how "this session is already elevated" reaches admission
	// without the runner owning any score of its own. Nil means never
	// elevated.
	RiskLookup func(sessionID string) (score float64, action string)
	// Clock stamps recorded decisions. Defaults to time.Now.
	Clock func() time.Time
}

// maxBoundSessions caps the async-completion registry. An async result has
// to find its session, and a map keyed by session ID that nothing prunes is
// an unbounded retained history.
const maxBoundSessions = 1024

// maxRecordedDecisions caps the set of decision IDs already recorded. It is
// what keeps a retried request, or an inline and an async completion of the
// same window, from recording one decision twice. Like the scheduler's
// dedup history it is a fixed ring, not a per-session map.
const maxRecordedDecisions = 4096

// MaxMessagesPerRequest caps how many eligible messages of one request are
// assessed. The scheduler's deadline and budgets apply per message, so
// without this cap a request carrying a long history would multiply both.
// The newest messages are assessed first; the rest are counted as the
// GapMessagesNotAssessed coverage gap.
const MaxMessagesPerRequest = 8

// GapMessagesNotAssessed is the CoverageGaps reason for eligible messages
// skipped because of MaxMessagesPerRequest or an ended request context.
const GapMessagesNotAssessed = "messages_not_assessed"

// Runner is the orchestrator. It is safe for concurrent use.
type Runner struct {
	cfg  Config
	mode string
	// assessor is cfg.Scheduler's candidate-aware form, when it has one.
	assessor candidateAssessor

	// bound lets an async completion find its session. It is bounded and
	// evicted in insertion order.
	boundMu   sync.Mutex
	bound     map[string]*session.Session
	boundRing []string
	boundNext int

	// recorded is the bounded set of decision IDs already recorded.
	recordedMu   sync.Mutex
	recorded     map[string]int
	recordedRing []string
	recordedNext int

	// gaps counts coverage gaps by reason, for health output.
	gapsMu sync.Mutex
	gaps   map[string]int64
}

// candidateAssessor is the richer scheduler entry point that takes every
// preprocessed representation of one message at once.
type candidateAssessor interface {
	AssessCandidates(ctx context.Context, req scheduler.Request, in decision.Input, cands []decision.Candidate, signals []decision.Signal) (decision.Assessment, error)
}

// New validates the configuration and constructs a runner.
func New(cfg Config) (*Runner, error) {
	switch cfg.Mode {
	case "":
		return nil, errors.New("runner: Mode is required")
	case config.DecisionModeDisabled, config.DecisionModeShadow, config.DecisionModeAudit, config.DecisionModeEnforce:
	default:
		return nil, fmt.Errorf("runner: unknown mode %q", cfg.Mode)
	}
	if cfg.Mode != config.DecisionModeDisabled {
		if cfg.Scheduler == nil {
			return nil, errors.New("runner: Scheduler is required unless Mode is disabled")
		}
		if len(cfg.Signals) == 0 {
			return nil, errors.New("runner: at least one Signal is required")
		}
		if cfg.Thresholds.Main <= 0 || cfg.Thresholds.Main >= 1 {
			return nil, fmt.Errorf("runner: Thresholds.Main %v is outside (0,1)", cfg.Thresholds.Main)
		}
		if cfg.Thresholds.Aux <= 0 || cfg.Thresholds.Aux >= 1 {
			return nil, fmt.Errorf("runner: Thresholds.Aux %v is outside (0,1)", cfg.Thresholds.Aux)
		}
		if cfg.Model.ThresholdSet == "" {
			return nil, errors.New("runner: Model.ThresholdSet is required")
		}
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.RiskLookup == nil {
		cfg.RiskLookup = func(string) (float64, string) { return 0, "" }
	}
	cfg.Signals = append([]decision.Signal(nil), cfg.Signals...)

	r := &Runner{
		cfg:          cfg,
		mode:         capMode(cfg.Mode, cfg.PolicyMode),
		bound:        make(map[string]*session.Session, maxBoundSessions),
		boundRing:    make([]string, maxBoundSessions),
		recorded:     make(map[string]int, maxRecordedDecisions),
		recordedRing: make([]string, maxRecordedDecisions),
		gaps:         make(map[string]int64),
	}
	if ca, ok := cfg.Scheduler.(candidateAssessor); ok {
		r.assessor = ca
	}
	return r, nil
}

// capMode applies the policy.mode cap: policy.mode audit caps decision.mode
// enforce down to audit. A lower decision mode is never raised.
//
//	mode      policy.mode  effective
//	enforce   enforce      enforce
//	enforce   audit        audit
//	audit     any          audit
//	shadow    any          shadow
//	disabled  any          disabled
func capMode(mode, policyMode string) string {
	if mode == config.DecisionModeEnforce && policyMode == "audit" {
		return config.DecisionModeAudit
	}
	return mode
}

// EffectiveMode returns the mode actually in force after the policy cap.
func (r *Runner) EffectiveMode() string { return r.mode }

// eligibleRole reports whether content from this role is analyzed.
//
// Phase 1 scope is request-side user content and untrusted tool results.
// System and assistant content is trusted and excluded.
func eligibleRole(role string) bool {
	return role == "user" || role == "tool"
}

// severityFor maps a calibrated probability onto a violation severity.
// Below Elevated there is nothing to say at all.
func severityFor(p float64, th Thresholds) string {
	switch {
	case p >= th.Critical:
		return "critical"
	case p >= th.Warning:
		return "warning"
	case p >= th.Elevated:
		return "info"
	default:
		return ""
	}
}

// applyVeto reports whether the human_directed head rescues an injection
// score from the same window.
//
// The upstream rule is `injection >= main AND human_directed < aux`, so a
// human_directed at or above the aux threshold vetoes regardless of the
// injection score (a veto on a sub-threshold score is moot but recorded).
// An unanswered aux head cannot rescue anything: unknown is never a reason
// to lower a score.
func applyVeto(_, aux float64, auxAnswered bool, th Thresholds) bool {
	if !auxAnswered {
		return false
	}
	return aux >= th.Aux
}

// executionMode turns a protection scope into the execution mode recorded on
// evidence.
func executionMode(scope decision.ProtectionScope) string {
	if scope == decision.ScopeCurrentRequest {
		return "inline"
	}
	return "async"
}

// Verdict is one decision after thresholds and the veto have been applied.
type Verdict struct {
	// Signal is always decision.SignalInjection.
	Signal decision.Signal
	// Probability is the injection probability for this window.
	Probability float64
	// AuxProbability is the human_directed probability from the same
	// window; meaningful only when AuxAnswered.
	AuxProbability float64
	// AuxAnswered reports whether the same window's human_directed head
	// answered.
	AuxAnswered bool
	// Vetoed reports whether the same window's human_directed head rescued
	// this score.
	Vetoed bool
	// Severity is severityFor(Probability); "" below Thresholds.Elevated.
	Severity string
	// Answered is always true: unanswered windows produce no verdict.
	Answered bool
	// Window locates the scored content in the original message.
	Window decision.Window
	// DecisionID is the stable decision identity.
	DecisionID string
	// ExecutionMode is "inline" or "async".
	ExecutionMode string
	// Scope is what this result could still protect.
	Scope decision.ProtectionScope
	// Coverage is the assessment's coverage.
	Coverage decision.Coverage
	// Latency is the provider latency for this window.
	Latency time.Duration
}

// Verdicts turns an Assessment into verdicts: one per answered injection
// window, with the human_directed head of the SAME window applied as a veto.
//
// The pairing goes through Assessment.ByWindow, never MaxProbability: an
// unrelated window's human_directed score cannot rescue this one. A Window
// is unique per scored window, including across the windows of one derived
// representation (its local offsets differ), so ByWindow pairs exactly.
func (r *Runner) Verdicts(sessionID, requestID string, in decision.Input, a decision.Assessment) []Verdict {
	inj := a.ByWindow(decision.SignalInjection)
	if len(inj) == 0 {
		return nil
	}
	aux := a.ByWindow(decision.SignalHumanDirected)

	// Walk Decisions for a deterministic order; ByWindow supplies the
	// answers, one per window.
	seen := make(map[decision.Window]bool, len(inj))
	out := make([]Verdict, 0, len(inj))
	for _, cand := range a.Decisions {
		w := cand.Window
		d, ok := inj[w]
		if cand.Signal != decision.SignalInjection || !ok || seen[w] {
			continue
		}
		seen[w] = true
		var auxP float64
		var auxOK bool
		if ad, ok := aux[w]; ok {
			auxP, auxOK = ad.Probability, true
		}

		out = append(out, Verdict{
			Signal:         d.Signal,
			Probability:    d.Probability,
			AuxProbability: auxP,
			AuxAnswered:    auxOK,
			Vetoed:         applyVeto(d.Probability, auxP, auxOK, r.cfg.Thresholds),
			Severity:       severityFor(d.Probability, r.cfg.Thresholds),
			Answered:       true,
			Window:         w,
			DecisionID: decision.DecisionID(decision.Identity{
				SessionID:    sessionID,
				RequestID:    requestID,
				MessageIndex: in.MessageIndex,
				Signal:       d.Signal,
				ModelVersion: r.cfg.Model.Version,
			}.WithWindow(w)),
			ExecutionMode: executionMode(a.Scope),
			Scope:         a.Scope,
			Coverage:      a.Coverage,
			Latency:       d.Latency,
		})
	}
	return out
}

// countGap records coverage gaps by reason, for health output. A gap is a
// coverage fact, never a finding, and adds no risk.
func (r *Runner) countGap(reason string, n int64) {
	r.gapsMu.Lock()
	defer r.gapsMu.Unlock()
	r.gaps[reason] += n
}

// CoverageGaps returns a snapshot of the gap counts by reason. The map is
// copied so a caller cannot mutate runner state through it.
func (r *Runner) CoverageGaps() map[string]int64 {
	r.gapsMu.Lock()
	defer r.gapsMu.Unlock()
	out := make(map[string]int64, len(r.gaps))
	for k, v := range r.gaps {
		out[k] = v
	}
	return out
}

// Bind registers a session so an async completion can find it again. The
// registry is bounded: past maxBoundSessions the oldest binding is evicted,
// and an async result for an evicted session is dropped.
func (r *Runner) Bind(sess *session.Session) {
	if sess == nil {
		return
	}
	r.boundMu.Lock()
	defer r.boundMu.Unlock()
	if _, ok := r.bound[sess.ID]; ok {
		// Refresh the pointer: a session re-created under the same ID must
		// not leave async results landing on the stale one.
		r.bound[sess.ID] = sess
		return
	}
	if old := r.boundRing[r.boundNext]; old != "" {
		delete(r.bound, old)
	}
	r.boundRing[r.boundNext] = sess.ID
	r.boundNext = (r.boundNext + 1) % len(r.boundRing)
	r.bound[sess.ID] = sess
}

func (r *Runner) lookupSession(id string) *session.Session {
	r.boundMu.Lock()
	defer r.boundMu.Unlock()
	return r.bound[id]
}

// claimDecision reports whether id has not been recorded before, and marks
// it recorded. Check and mark happen under one lock, so an inline and an
// async completion racing on the same decision record it once.
func (r *Runner) claimDecision(id string) bool {
	r.recordedMu.Lock()
	defer r.recordedMu.Unlock()
	if _, dup := r.recorded[id]; dup {
		return false
	}
	slot := r.recordedNext
	if old := r.recordedRing[slot]; old != "" {
		delete(r.recorded, old)
	}
	r.recordedRing[slot] = id
	r.recordedNext = (slot + 1) % len(r.recordedRing)
	r.recorded[id] = slot
	return true
}

// AssessRequest preprocesses and assesses the eligible messages of one
// request.
//
// It never returns an error: a failure to analyze is a coverage fact, not a
// request failure. Each message is assessed under the scheduler's own
// deadline; at most MaxMessagesPerRequest messages are assessed, newest
// first, and assessment stops once ctx has ended. It must be called BEFORE
// the request is forwarded, so an inline result can still protect it.
func (r *Runner) AssessRequest(ctx context.Context, sess *session.Session, requestID string, msgs []Message) {
	if r.mode == config.DecisionModeDisabled || sess == nil || len(msgs) == 0 {
		return
	}

	// Newest first: in a chat request the last messages are the new ones;
	// the history before them was assessed on earlier requests.
	var eligible []Message
	for i := len(msgs) - 1; i >= 0; i-- {
		if eligibleRole(msgs[i].Role) && msgs[i].Content != "" {
			eligible = append(eligible, msgs[i])
		}
	}
	if len(eligible) == 0 {
		return
	}
	r.Bind(sess)

	_, action := r.cfg.RiskLookup(sess.ID)
	// Elevated means the ladder has moved past observe; a nonzero score
	// alone is not elevation.
	elevated := action != "" && action != "observe"

	for n, msg := range eligible {
		if n >= MaxMessagesPerRequest || ctx.Err() != nil {
			skipped := int64(len(eligible) - n)
			r.countGap(GapMessagesNotAssessed, skipped)
			slog.Debug("semantic assessment skipped messages",
				"session_id", sess.ID, "request_id", requestID,
				"skipped", skipped, "reason", GapMessagesNotAssessed)
			return
		}
		r.assessMessage(ctx, sess, requestID, msg, elevated)
	}
}

// assessMessage preprocesses, assesses and records one message.
func (r *Runner) assessMessage(ctx context.Context, sess *session.Session, requestID string, msg Message, elevated bool) {
	// Analysis-only preprocessing. Nothing here is forwarded.
	pre := preprocess.Run(msg.Content, r.cfg.Budget)

	in := decision.Input{
		Content:      msg.Content,
		Direction:    decision.DirectionRequest,
		SourceRole:   msg.Role,
		MessageIndex: msg.Index,
	}

	var a decision.Assessment
	var err error
	if r.assessor != nil {
		req := scheduler.Request{
			SessionID:  sess.ID,
			RequestID:  requestID,
			Elevated:   elevated,
			PreSignals: pre.Signals,
			Strict:     r.cfg.Strict,
		}
		a, err = r.assessor.AssessCandidates(ctx, req, in, pre.Candidates(), r.cfg.Signals)
	} else {
		a, err = r.cfg.Scheduler.Assess(ctx, in, r.cfg.Signals)
	}
	if err != nil {
		// The error type only: an error message could quote content.
		slog.Debug("semantic assessment did not complete",
			"session_id", sess.ID, "request_id", requestID,
			"error_type", fmt.Sprintf("%T", err))
		return
	}

	for _, g := range pre.Gaps {
		r.countGap(string(g.Reason), 1)
		slog.Debug("semantic preprocessing coverage gap",
			"session_id", sess.ID,
			"request_id", requestID,
			"reason", g.Reason,
			"transform", g.Transform,
		)
	}
	// A preprocessing gap (truncated input, a representation not produced)
	// is content the scheduler never saw, so the scan is not complete even
	// when every window it was handed answered.
	if len(pre.Gaps) > 0 {
		a.Coverage.Complete = false
	}

	r.handle(sess, requestID, in, a)
}

// OnAsync handles an async completion. It is the scheduler's OnAsync
// callback and does not block.
//
// An async result arrives after the request was forwarded, so it is always
// recorded with ScopeFutureActivity and never claims to have protected the
// current request, whatever scope it arrived with.
func (r *Runner) OnAsync(req scheduler.Request, in decision.Input, a decision.Assessment) {
	if r.mode == config.DecisionModeDisabled {
		return
	}
	sess := r.lookupSession(req.SessionID)
	if sess == nil {
		// The session was evicted from the bounded registry or never bound.
		// The result has nowhere to land; that is a bounded-history
		// consequence, not an error.
		slog.Debug("async semantic result has no bound session",
			"session_id", req.SessionID, "request_id", req.RequestID)
		return
	}
	a.Scope = decision.ScopeFutureActivity
	r.handle(sess, req.RequestID, in, a)
}

// handle records verdicts according to the effective mode.
//
// shadow is the only mode implemented here. audit and enforce are added in
// Tasks 28 and 29; until then they record the shadow surface too, which is
// strictly less than they will do and never more.
func (r *Runner) handle(sess *session.Session, requestID string, in decision.Input, a decision.Assessment) {
	for _, v := range r.Verdicts(sess.ID, requestID, in, a) {
		r.recordShadow(sess, in, v)
	}
}

// recordShadow appends one bounded shadow entry, once per DecisionID. It
// never touches the risk score and carries no content.
func (r *Runner) recordShadow(sess *session.Session, in decision.Input, v Verdict) {
	if !r.claimDecision(v.DecisionID) {
		return
	}
	sess.RecordSemanticShadow(session.SemanticShadow{
		Timestamp:        r.cfg.Clock(),
		DecisionID:       v.DecisionID,
		Signal:           string(v.Signal),
		Probability:      v.Probability,
		AuxProbability:   v.AuxProbability,
		Vetoed:           v.Vetoed,
		SourceRole:       in.SourceRole,
		MessageIndex:     in.MessageIndex,
		Transform:        v.Window.Transform,
		TransformDepth:   v.Window.TransformDepth,
		WindowStartByte:  v.Window.StartByte,
		WindowEndByte:    v.Window.EndByte,
		Model:            r.cfg.Model.Name,
		ModelVersion:     r.cfg.Model.Version,
		ModelChecksum:    r.cfg.Model.Checksum,
		ThresholdSet:     r.cfg.Model.ThresholdSet,
		ExecutionMode:    v.ExecutionMode,
		ProtectionScope:  string(v.Scope),
		CoverageComplete: v.Coverage.Complete,
		LatencyMs:        v.Latency.Milliseconds(),
	})
}
