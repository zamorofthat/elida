// Package runner connects preprocessing, the scheduler, the thresholds and
// the session. It is the only component the proxy sees.
//
// Modes:
//   - disabled: nothing runs, nothing is recorded.
//   - shadow:   decisions are recorded on the session for calibration. No
//     violations, no risk.
//   - audit:    evidence-only violations with diagnostic points, recorded
//     through Config.Policy (nil Policy means shadow-only behavior).
//   - enforce:  a non-vetoed score at or above Thresholds.Main records an
//     ORDINARY semantic_injection violation, which adds risk through the
//     policy engine's existing flag -> throttle -> block -> kill ladder. The
//     runner adds no action, threshold or proxy path of its own.
//     injection_elevated stays evidence-only. New refuses enforce unless
//     Config.ThresholdSetMatches: enforcement needs calibration evidence for
//     the exact model and threshold set in use.
//
// policy.mode caps decision.mode: with policy.mode audit, enforce behaves
// as audit. Nothing here maintains a risk score of its own.
//
// Unknown is never safe. A window the provider did not answer produces no
// verdict and no shadow entry; the runner never synthesizes a low
// probability from absence, and partial coverage is recorded as partial.
//
// Request scope. One request gets one inline deadline (Config.InlineTimeout,
// or the scheduler's own) covering preprocessing and every message's
// assessment, and one inline window and token budget shared across its
// messages. New messages are taken newest first, at most
// MaxMessagesPerRequest per request.
//
// Coverage. coverage_complete on a recorded decision is per message: it says
// whether every eligible window of THAT message was scored. What a request
// left unexamined is counted by reason and exposed through CoverageGaps, for
// the decision status output:
//   - messages_not_assessed: eligible new messages past
//     MaxMessagesPerRequest, or reached after the request deadline. They are
//     retried on the next request that carries them.
//   - preprocessing gap reasons (input_truncated, ...): content the
//     scheduler never saw; that message's coverage_complete is false.
//   - original_only: the scheduler could not take derived representations,
//     so only the original was scored; coverage_complete is false.
//   - unsupported_block: content blocks of the message the proxy could not
//     render as text (images, documents); coverage_complete is false.
//
// already_assessed is not a gap and is counted separately
// (AlreadyAssessed). Chat clients resend the whole history, so a message
// this session already had scored (same index, same content hash) is
// skipped rather than scored and recorded again on every turn. A claim is
// kept only once some window of the message was answered, inline or by an
// async job; a message whose every window went unanswered (including async
// jobs that were canceled or failed) is released and assessed again later.
//
// Known limitations. A message that a concurrent request on the same
// session is still assessing is counted as already_assessed by the other
// request; if that assessment then answers nothing the claim is released,
// but the other request has skipped the message without a gap. The deadline
// is checked before each message, so preprocessing of the one message in
// progress can overrun it, at most once per request.
//
// Sessions. Bind registers a session so async results and the per-session
// assessed-message set can find it; the registry is bounded. The session-end
// path must call Unbind, so an ended session is neither retained nor written
// to by a late async result. One benign race remains: an OnAsync that looked
// the session up just before Unbind can still record onto it, so at most one
// in-flight result per job can land on a session that is being persisted.
//
// Nothing in this package logs, records or returns request content; the
// assessed-message set keeps a SHA-256 of each message, never the message.
package runner

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"elida/internal/config"
	"elida/internal/decision"
	"elida/internal/decision/preprocess"
	"elida/internal/decision/scheduler"
	"elida/internal/policy"
	"elida/internal/session"
)

// Rule names and event categories semantic decisions record under.
//
// RuleSemanticInjection is an evidence-only violation in audit mode and an
// ordinary, risk-contributing one in effective enforce mode.
// RuleInjectionElevated is always evidence-only: it exists so a
// repeated-category correlation rule can accumulate weak signals that
// individually mean nothing.
const (
	// RuleSemanticInjection names a score at or above Thresholds.Main.
	RuleSemanticInjection = "semantic_injection"
	// RuleInjectionElevated names a score in [Thresholds.Elevated, Main).
	RuleInjectionElevated = "injection_elevated"

	// CategorySemanticInjection is RuleSemanticInjection's event category.
	CategorySemanticInjection = "semantic_injection"
	// CategoryInjectionElevated is RuleInjectionElevated's event category.
	CategoryInjectionElevated = "injection_elevated"
)

// PolicyRecorder is the policy engine seen from here: one method, which
// routes a semantic decision into the existing violation machinery.
//
// Narrow on purpose. The runner must not be able to reach
// AddExternalRiskPoints or anything else that would let semantic detection
// maintain a score of its own. *policy.Engine satisfies it.
type PolicyRecorder interface {
	RecordSemanticViolation(sessionID string, v policy.Violation)
}

// WouldContributePoints is what a violation of this severity from this
// source role would add to the session risk score at the moment it fired,
// before decay.
//
// It is a diagnostic shown in the UI and telemetry for audit mode. It uses
// the policy engine's own weight tables with the engine's fallbacks (an
// unknown severity or role weighs 1.0), so the number an operator sees is
// what enforcement would actually add; a test pins it to the value the
// engine stores. It is never a second authoritative score and nothing reads
// it to enforce.
func WouldContributePoints(severity policy.Severity, sourceRole string) float64 {
	sev := policy.SeverityWeights[severity]
	if sev == 0 {
		sev = 1.0
	}
	role := policy.SourceRoleWeights[sourceRole]
	if role == 0 {
		role = 1.0
	}
	return sev * role
}

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
	// Elevated is the lower edge of the injection_elevated band: a score in
	// [Elevated, Main) that was not vetoed records an evidence-only
	// injection_elevated event in audit and enforce modes. Zero or less
	// disables the band (otherwise every score would qualify). Elevated at
	// or above Main leaves the band empty: no injection_elevated events
	// (setupDecision warns about it at startup).
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
// value; the only use of the policy package is the AssessPolicyMessages
// conversion, so nothing here depends on the policy engine's behavior.
type Message struct {
	// Role is "user", "assistant", "system" or "tool".
	Role string
	// Index is the position in the request's messages array (-1 for a
	// top-level system prompt).
	Index int
	// Content is the text to analyze. It is never logged or recorded.
	Content string
	// SkippedBlocks counts content blocks of the message that are not in
	// Content (images, documents, other non-text blocks). Nonzero means the
	// message is only partly analyzable: its decisions are recorded with
	// coverage_complete false and GapUnsupportedBlock is counted.
	SkippedBlocks int
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
	// assessed; otherwise only the original content is, and the result is
	// recorded as incomplete coverage.
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
	// InlineTimeout is the one inline deadline for a whole request. Zero
	// takes the scheduler's own (*scheduler.Inline exposes it); if neither
	// is known, only the caller's context bounds the request. It is the
	// same deadline the scheduler applies per call, scoped to the request;
	// it never extends the scheduler's.
	InlineTimeout time.Duration
	// Policy records violations. Nil means shadow-only behavior regardless
	// of Mode, which is how the runner is tested without a policy engine.
	// New rejects a typed nil (a nil *policy.Engine behind the interface),
	// which would otherwise be called on the first finding.
	Policy PolicyRecorder
	// ThresholdSetMatches reports whether the configured threshold set
	// matches the loaded model's own. Enforce mode is refused when it does
	// not: strict enforcement requires calibration evidence for the exact
	// model and threshold-set versions in use, and a threshold selected for
	// a different model is not evidence.
	ThresholdSetMatches bool
}

// maxBoundSessions caps the session registry. An async result has to find
// its session, and a map keyed by session ID that nothing prunes is an
// unbounded retained history.
const maxBoundSessions = 1024

// MaxAssessedPerSession caps the per-session set of messages already
// assessed. Past it the oldest entry is evicted, and that message is
// assessed again if it is still being resent.
const MaxAssessedPerSession = 256

// maxRecordedDecisions caps the set of decision IDs already recorded. It is
// what keeps an inline and an async completion of the same window, or a
// redelivery, from recording one decision twice. Like the scheduler's dedup
// history it is a fixed ring, not a per-session map.
const maxRecordedDecisions = 4096

// MaxMessagesPerRequest caps how many new eligible messages of one request
// are assessed. The newest are assessed first; the rest are counted as
// GapMessagesNotAssessed and retried on the next request carrying them.
const MaxMessagesPerRequest = 8

// Coverage reasons.
const (
	// GapMessagesNotAssessed counts eligible new messages skipped because of
	// MaxMessagesPerRequest or because the request deadline had passed.
	GapMessagesNotAssessed = "messages_not_assessed"
	// GapOriginalOnly counts messages assessed through a scheduler without
	// AssessCandidates: derived representations were not scored.
	GapOriginalOnly = "original_only"
	// GapUnsupportedBlock counts content blocks of assessed messages that
	// could not be rendered as text (images, documents, nested non-text
	// tool_result blocks), so were never analyzed.
	GapUnsupportedBlock = "unsupported_block"
	// ReasonAlreadyAssessed counts messages skipped because this session
	// already had them scored. It is not a gap; see AlreadyAssessed.
	ReasonAlreadyAssessed = "already_assessed"
)

// Runner is the orchestrator. It is safe for concurrent use.
type Runner struct {
	cfg  Config
	mode string
	// assessor is cfg.Scheduler's candidate-aware form, when it has one.
	assessor candidateAssessor
	// timeout is the request-scoped inline deadline; 0 means none known.
	timeout time.Duration

	// bound is the session registry: async results and the per-session
	// assessed-message set find their session through it. It is bounded and
	// evicted in insertion order; boundMu also guards every assessedSet.
	boundMu   sync.Mutex
	bound     map[string]*boundSession
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

	alreadyAssessed atomic.Int64
}

// boundSession is one registry entry.
type boundSession struct {
	sess     *session.Session
	assessed *assessedSet
	// pending tracks claimed messages whose assessment is not settled yet:
	// the request is still assessing them, or async jobs for them are still
	// outstanding. It holds at most MaxAssessedPerSession entries.
	pending map[msgKey]*pendingMsg
}

// pendingMsg is the settlement state of one claimed message.
//
// The claim stays only if some window of the message was answered, inline
// or async. Once the request has finished with it (open is false) and every
// async job queued for it has been delivered (jobs is 0), a message with
// no answer at all is released, so the next request carrying it assesses it
// again: unknown is never "already assessed". jobs may go negative while
// open, when a fast async job is delivered before the request has counted
// what it queued.
type pendingMsg struct {
	open     bool
	jobs     int
	answered bool
}

// msgKey identifies one message of a session's history by position and
// content hash, never by content.
type msgKey struct {
	index int
	sum   [sha256.Size]byte
}

// assessedSet is a bounded set of message keys, evicted oldest first.
type assessedSet struct {
	seen map[msgKey]int // key -> ring slot
	ring []msgKey
	used []bool
	next int
}

func newAssessedSet() *assessedSet {
	return &assessedSet{
		seen: make(map[msgKey]int),
		ring: make([]msgKey, MaxAssessedPerSession),
		used: make([]bool, MaxAssessedPerSession),
	}
}

// add reports whether k is new, and records it.
func (s *assessedSet) add(k msgKey) bool {
	if _, dup := s.seen[k]; dup {
		return false
	}
	slot := s.next
	if s.used[slot] {
		// Evict only if the old key still points at this slot; a key that
		// was released and re-added lives in a newer slot.
		if at, ok := s.seen[s.ring[slot]]; ok && at == slot {
			delete(s.seen, s.ring[slot])
		}
	}
	s.ring[slot], s.used[slot] = k, true
	s.next = (slot + 1) % len(s.ring)
	s.seen[k] = slot
	return true
}

// remove forgets k: its assessment produced nothing, so a later request may
// try again.
func (s *assessedSet) remove(k msgKey) {
	if slot, ok := s.seen[k]; ok {
		delete(s.seen, k)
		s.used[slot] = false
	}
}

// candidateAssessor is the richer scheduler entry point that takes every
// preprocessed representation of one message at once.
type candidateAssessor interface {
	AssessCandidates(ctx context.Context, req scheduler.Request, in decision.Input, cands []decision.Candidate, signals []decision.Signal) (decision.Assessment, error)
}

// timeoutReporter is a scheduler that reports its inline deadline.
type timeoutReporter interface {
	InlineTimeout() time.Duration
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
		// Checked on the configured mode, before the policy cap: a refused
		// enforce must surface as the configuration error it is, never as a
		// quiet downgrade the operator cannot see.
		if cfg.Mode == config.DecisionModeEnforce && !cfg.ThresholdSetMatches {
			return nil, fmt.Errorf("runner: enforce mode refused: the configured threshold set %q does not match the loaded model; strict enforcement requires calibration evidence for the exact model and threshold-set versions in use", cfg.Model.ThresholdSet)
		}
	}
	if cfg.Policy != nil {
		// A nil pointer behind the interface (e.g. a nil *policy.Engine) is
		// a non-nil interface and would be called on the first finding.
		if rv := reflect.ValueOf(cfg.Policy); rv.Kind() == reflect.Pointer && rv.IsNil() {
			return nil, fmt.Errorf("runner: Policy is a nil %T; pass a nil interface for shadow-only behavior", cfg.Policy)
		}
	}
	if cfg.InlineTimeout < 0 {
		return nil, fmt.Errorf("runner: InlineTimeout must not be negative, got %v", cfg.InlineTimeout)
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
		timeout:      cfg.InlineTimeout,
		bound:        make(map[string]*boundSession, maxBoundSessions),
		boundRing:    make([]string, maxBoundSessions),
		recorded:     make(map[string]int, maxRecordedDecisions),
		recordedRing: make([]string, maxRecordedDecisions),
		gaps:         make(map[string]int64),
	}
	if ca, ok := cfg.Scheduler.(candidateAssessor); ok {
		r.assessor = ca
	}
	if tr, ok := cfg.Scheduler.(timeoutReporter); ok && r.timeout == 0 {
		r.timeout = tr.InlineTimeout()
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

// isElevated reports whether the session's risk is already elevated.
//
// The ladder action is the authority: past observe means elevated, and a
// nonzero score alone is not (a single decaying info-level flag must not
// admit all plain content inline). When the action is empty, which is what
// a disabled risk ladder reports, the score is all there is, so any
// positive score counts.
func isElevated(score float64, action string) bool {
	if action == "" {
		return score > 0
	}
	return action != "observe"
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
				SourceRole:   in.SourceRole,
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

// CoverageGaps returns a snapshot of the gap counts by reason, including
// GapMessagesNotAssessed, GapOriginalOnly and the preprocessing gap reasons.
// The decision status output surfaces it. The map is copied so a caller
// cannot mutate runner state through it.
func (r *Runner) CoverageGaps() map[string]int64 {
	r.gapsMu.Lock()
	defer r.gapsMu.Unlock()
	out := make(map[string]int64, len(r.gaps))
	for k, v := range r.gaps {
		out[k] = v
	}
	return out
}

// AlreadyAssessed returns how many messages were skipped because their
// session already had them scored (ReasonAlreadyAssessed). This is not a
// coverage gap: the content was analyzed on an earlier request.
func (r *Runner) AlreadyAssessed() int64 { return r.alreadyAssessed.Load() }

// Bind registers a session so an async completion and the assessed-message
// set can find it. The registry is bounded: past maxBoundSessions the oldest
// binding is evicted, an async result for it is dropped, and its history is
// assessed again if it is resent.
func (r *Runner) Bind(sess *session.Session) {
	if sess == nil {
		return
	}
	r.boundMu.Lock()
	defer r.boundMu.Unlock()
	if b, ok := r.bound[sess.ID]; ok {
		// Refresh the pointer: a session re-created under the same ID must
		// not leave async results landing on the stale one.
		b.sess = sess
		return
	}
	if old := r.boundRing[r.boundNext]; old != "" {
		delete(r.bound, old)
	}
	r.boundRing[r.boundNext] = sess.ID
	r.boundNext = (r.boundNext + 1) % len(r.boundRing)
	r.bound[sess.ID] = &boundSession{
		sess:     sess,
		assessed: newAssessedSet(),
		pending:  make(map[msgKey]*pendingMsg),
	}
}

// Unbind drops a session from the registry, with its assessed-message set.
// The session-end callback must call it (Task 26): an ended session is then
// no longer retained by the runner, and a late async result for it is
// dropped instead of landing on an already-persisted session.
func (r *Runner) Unbind(sess *session.Session) {
	if sess == nil {
		return
	}
	r.boundMu.Lock()
	defer r.boundMu.Unlock()
	b, ok := r.bound[sess.ID]
	if !ok || b.sess != sess {
		// Not bound, or a newer session now owns this ID.
		return
	}
	delete(r.bound, sess.ID)
	for i, id := range r.boundRing {
		if id == sess.ID {
			r.boundRing[i] = ""
			break
		}
	}
}

func (r *Runner) lookupSession(id string) *session.Session {
	r.boundMu.Lock()
	defer r.boundMu.Unlock()
	if b, ok := r.bound[id]; ok {
		return b.sess
	}
	return nil
}

// claimMessage reports whether this session has not yet had the message
// assessed, and marks it. A session missing from the registry has no
// history to consult, so every message counts as new.
func (r *Runner) claimMessage(sessionID string, k msgKey) bool {
	r.boundMu.Lock()
	defer r.boundMu.Unlock()
	b, ok := r.bound[sessionID]
	if !ok {
		return true
	}
	return b.assessed.add(k)
}

// releaseMessage forgets a claim whose assessment produced nothing.
func (r *Runner) releaseMessage(sessionID string, k msgKey) {
	r.boundMu.Lock()
	defer r.boundMu.Unlock()
	if b, ok := r.bound[sessionID]; ok {
		b.assessed.remove(k)
	}
}

// openPending starts tracking a claimed message's settlement. It reports
// false when the session is not bound or its pending set is full; the caller
// then settles the message from the inline result alone.
func (r *Runner) openPending(sessionID string, k msgKey) bool {
	r.boundMu.Lock()
	defer r.boundMu.Unlock()
	b, ok := r.bound[sessionID]
	if !ok || len(b.pending) >= MaxAssessedPerSession {
		return false
	}
	b.pending[k] = &pendingMsg{open: true}
	return true
}

// closePending records what the request's own assessment of a message did:
// whether anything was answered inline and how many async jobs it queued.
func (r *Runner) closePending(sessionID string, k msgKey, answered bool, queued int) {
	r.boundMu.Lock()
	defer r.boundMu.Unlock()
	b, ok := r.bound[sessionID]
	if !ok {
		return
	}
	p, ok := b.pending[k]
	if !ok {
		return
	}
	p.open = false
	p.answered = p.answered || answered
	p.jobs += queued
	b.settle(k, p)
}

// asyncDelivered records one async job's terminal delivery for a message.
// A delivery for a message not being tracked (settled already, or never
// tracked) changes nothing.
func (r *Runner) asyncDelivered(sessionID string, k msgKey, answered bool) {
	r.boundMu.Lock()
	defer r.boundMu.Unlock()
	b, ok := r.bound[sessionID]
	if !ok {
		return
	}
	p, ok := b.pending[k]
	if !ok {
		return
	}
	p.jobs--
	p.answered = p.answered || answered
	b.settle(k, p)
}

// settle ends tracking once the request is done with the message and every
// queued job has been delivered, releasing the claim when nothing at all
// was answered. The caller holds boundMu.
func (b *boundSession) settle(k msgKey, p *pendingMsg) {
	if p.open || p.jobs > 0 {
		return
	}
	delete(b.pending, k)
	if !p.answered {
		b.assessed.remove(k)
	}
}

// answeredAny reports whether any decision in a was answered.
func answeredAny(a decision.Assessment) bool {
	for _, d := range a.Decisions {
		if d.Answered {
			return true
		}
	}
	return false
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

// AssessRequest preprocesses and assesses the new eligible messages of one
// request.
//
// It never returns an error: a failure to analyze is a coverage fact, not a
// request failure. One deadline covers the whole request, preprocessing
// included, and one inline budget is shared by its messages. Messages this
// session already had scored are skipped; of the rest, at most
// MaxMessagesPerRequest are assessed, newest first. It must be called BEFORE
// the request is forwarded, so an inline result can still protect it.
func (r *Runner) AssessRequest(ctx context.Context, sess *session.Session, requestID string, msgs []Message) {
	if r.mode == config.DecisionModeDisabled || sess == nil || len(msgs) == 0 {
		return
	}
	if r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}

	// Newest first: in a chat request the last messages are the new ones.
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

	elevated := isElevated(r.cfg.RiskLookup(sess.ID))
	spent := &scheduler.Spend{}

	var assessed, notAssessed int64
	for _, msg := range eligible {
		k := msgKey{index: msg.Index, sum: sha256.Sum256([]byte(msg.Content))}
		if assessed >= MaxMessagesPerRequest || ctx.Err() != nil {
			// Unclaimed, so a later request carrying it tries again; unless
			// it was already assessed, in which case nothing is missing.
			if r.alreadyClaimed(sess.ID, k) {
				r.alreadyAssessed.Add(1)
			} else {
				notAssessed++
			}
			continue
		}
		if !r.claimMessage(sess.ID, k) {
			r.alreadyAssessed.Add(1)
			continue
		}
		assessed++
		tracked := r.openPending(sess.ID, k)
		answered, queued := r.assessMessage(ctx, sess, requestID, msg, elevated, spent)
		switch {
		case tracked:
			// Settles now if nothing was queued, or once every queued job
			// has been delivered (OnAsync); released if nothing answered.
			r.closePending(sess.ID, k, answered, queued)
		case !answered && queued == 0:
			// Untracked (pending set full): settle from what is known now.
			// Unknown is never "already assessed".
			r.releaseMessage(sess.ID, k)
		}
	}
	if notAssessed > 0 {
		r.countGap(GapMessagesNotAssessed, notAssessed)
		slog.Debug("semantic assessment skipped messages",
			"session_id", sess.ID, "request_id", requestID,
			"skipped", notAssessed, "reason", GapMessagesNotAssessed)
	}
}

// AssessPolicyMessages adapts the policy engine's per-message view to this
// package's Message type, so the proxy can hand the same extraction to both
// the regex engine and the semantic subsystem. It then behaves exactly as
// AssessRequest.
//
// This is the only place runner depends on the policy package, and it is a
// one-way value conversion.
func (r *Runner) AssessPolicyMessages(ctx context.Context, sess *session.Session, requestID string, msgs []policy.MessageToScan) {
	if r.mode == config.DecisionModeDisabled || len(msgs) == 0 {
		return
	}
	converted := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		converted = append(converted, Message{Role: m.Role, Index: m.Index, Content: m.Content, SkippedBlocks: m.SkippedBlocks})
	}
	r.AssessRequest(ctx, sess, requestID, converted)
}

// Bound reports whether a session is currently in the registry. It exists so
// the session-end path can be verified to release the session (Unbind).
func (r *Runner) Bound(sessionID string) bool {
	return r.lookupSession(sessionID) != nil
}

// alreadyClaimed reports whether the session's assessed set holds k, without
// claiming it.
func (r *Runner) alreadyClaimed(sessionID string, k msgKey) bool {
	r.boundMu.Lock()
	defer r.boundMu.Unlock()
	b, ok := r.bound[sessionID]
	if !ok {
		return false
	}
	_, dup := b.assessed.seen[k]
	return dup
}

// assessMessage preprocesses, assesses and records one message. It reports
// whether any window was answered inline, and how many async jobs were
// queued whose deliveries will reach OnAsync for this session.
func (r *Runner) assessMessage(ctx context.Context, sess *session.Session, requestID string, msg Message, elevated bool, spent *scheduler.Spend) (answered bool, queued int) {
	// Analysis-only preprocessing, inside the request deadline: the loop
	// in AssessRequest checks the deadline before each message. Nothing
	// here is forwarded.
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
			Spent:      spent,
		}
		a, err = r.assessor.AssessCandidates(ctx, req, in, pre.Candidates(), r.cfg.Signals)
	} else {
		a, err = r.cfg.Scheduler.Assess(ctx, in, r.cfg.Signals)
		if err == nil {
			// Only the original was scored, without the request's
			// elevation or preprocessing signals: never a complete scan.
			a.Coverage.Complete = false
			r.countGap(GapOriginalOnly, 1)
			// Assess carries no session identity, so any async job it
			// queued can never be delivered back to this session.
			a.Coverage.QueuedAsync = 0
		}
	}
	if err != nil {
		// The error type only: an error message could quote content.
		slog.Debug("semantic assessment did not complete",
			"session_id", sess.ID, "request_id", requestID,
			"error_type", fmt.Sprintf("%T", err))
		return false, 0
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
	// Blocks the proxy could not render as text were never handed over at
	// all: the same kind of gap, at the message level.
	if msg.SkippedBlocks > 0 {
		r.countGap(GapUnsupportedBlock, int64(msg.SkippedBlocks))
		a.Coverage.Complete = false
	}

	r.handle(sess, requestID, in, a)
	return answeredAny(a), a.Coverage.QueuedAsync
}

// OnAsync handles an async completion. It is the scheduler's OnAsync
// callback and does not block.
//
// An async result arrives after the request was forwarded, so it is always
// recorded with ScopeFutureActivity and never claims to have protected the
// current request, whatever scope it arrived with.
//
// Every delivery, including a canceled or failed job's all-unanswered one,
// also counts toward settling its message's claim: when the last job for a
// message is delivered and nothing about that message was answered, the
// claim is released so the next request carrying it assesses it again.
func (r *Runner) OnAsync(req scheduler.Request, in decision.Input, a decision.Assessment) {
	if r.mode == config.DecisionModeDisabled {
		return
	}
	k := msgKey{index: in.MessageIndex, sum: sha256.Sum256([]byte(in.Content))}
	defer r.asyncDelivered(req.SessionID, k, answeredAny(a))
	sess := r.lookupSession(req.SessionID)
	if sess == nil {
		// The session ended (Unbind), was evicted from the bounded registry,
		// or was never bound. The result has nowhere to land; that is a
		// bounded-history consequence, not an error.
		slog.Debug("async semantic result has no bound session",
			"session_id", req.SessionID, "request_id", req.RequestID)
		return
	}
	a.Scope = decision.ScopeFutureActivity
	r.handle(sess, req.RequestID, in, a)
}

// handle records verdicts according to the effective mode.
//
//   - shadow:  the bounded session list only. No violations, no risk.
//   - audit:   the shadow list plus an evidence-only violation carrying
//     diagnostic would-contribute points. No risk.
//   - enforce: the shadow list plus an ORDINARY semantic_injection
//     violation, which contributes risk through the existing ladder; the
//     ladder, not this package, decides whether that throttles or blocks.
//
// A non-vetoed score in [Thresholds.Elevated, Thresholds.Main) records an
// evidence-only injection_elevated event in audit and enforce alike: it is
// evidence for correlation, never a finding on its own.
//
// Only answered windows produce verdicts, so an ordinary violation is only
// ever recorded for a window the provider actually scored over the
// threshold. Incomplete coverage is carried on the evidence and the shadow
// entry (coverage_complete false); the part that went unscored is never
// turned into a violation, and it is never treated as safe either.
//
// One claim per verdict. claimDecision is taken once per DecisionID and
// gates BOTH the shadow entry and the violation, so a retry, an async
// redelivery or an inline+async pair for the same window records neither
// twice. Each verdict is complete when it is built (probability, veto and
// coverage are all final), so what the policy engine receives for a
// DecisionID is its final value; the engine's own first-wins dedup is only a
// backstop.
func (r *Runner) handle(sess *session.Session, requestID string, in decision.Input, a decision.Assessment) {
	for _, v := range r.Verdicts(sess.ID, requestID, in, a) {
		if !r.claimDecision(v.DecisionID) {
			continue
		}
		// Shadow data is recorded for every answered decision, whatever the
		// mode and whatever the score: calibration needs the whole
		// distribution, including the vetoes and the quiet scores.
		r.recordShadow(sess, in, v)

		if r.mode == config.DecisionModeShadow || r.cfg.Policy == nil {
			continue
		}
		// A vetoed decision is not a finding in any mode. The aux head
		// decided this directive is aimed at a person; the rescue is
		// recorded above so it stays explainable.
		if v.Vetoed {
			continue
		}

		switch {
		case v.Probability >= r.cfg.Thresholds.Main:
			// Over the violation threshold. Evidence-only in audit mode; an
			// ordinary violation in effective enforce mode, so the existing
			// risk ladder does the rest.
			evidenceOnly := r.mode != config.DecisionModeEnforce
			r.recordViolation(sess.ID, in, v, RuleSemanticInjection, CategorySemanticInjection, evidenceOnly)
		case r.cfg.Thresholds.Elevated > 0 && v.Probability >= r.cfg.Thresholds.Elevated:
			// Sub-threshold but notable. Always evidence-only: on its own
			// this contributes nothing, and a repeated-category correlation
			// rule is what gives it meaning.
			r.recordViolation(sess.ID, in, v, RuleInjectionElevated, CategoryInjectionElevated, true)
		}
	}
}

// recordViolation builds the policy violation for one verdict and records it
// through the ordinary violation path. The caller holds the verdict's claim.
func (r *Runner) recordViolation(sessionID string, in decision.Input, v Verdict, ruleName, category string, evidenceOnly bool) {
	severity := policy.Severity(v.Severity)
	if severity == "" {
		severity = policy.SeverityInfo
	}

	pv := policy.Violation{
		RuleName:      ruleName,
		Description:   r.describe(v, ruleName),
		Severity:      severity,
		Action:        "flag",
		Timestamp:     r.cfg.Clock(),
		SourceRole:    in.SourceRole,
		MessageIndex:  in.MessageIndex,
		EventCategory: category,
		FrameworkRef:  "OWASP-LLM01",
		EvidenceOnly:  evidenceOnly,
		Semantic: &policy.SemanticEvidence{
			Signal:           string(v.Signal),
			Probability:      v.Probability,
			AuxProbability:   v.AuxProbability,
			Vetoed:           v.Vetoed,
			Model:            r.cfg.Model.Name,
			ModelVersion:     r.cfg.Model.Version,
			ModelChecksum:    r.cfg.Model.Checksum,
			ThresholdSet:     r.cfg.Model.ThresholdSet,
			DecisionID:       v.DecisionID,
			Transform:        v.Window.Transform,
			TransformDepth:   v.Window.TransformDepth,
			WindowStartByte:  v.Window.StartByte,
			WindowEndByte:    v.Window.EndByte,
			CoverageComplete: v.Coverage.Complete,
			ExecutionMode:    v.ExecutionMode,
			ProtectionScope:  string(v.Scope),
		},
	}
	if evidenceOnly {
		// The diagnostic the UI shows: what this would have added had it
		// contributed. The engine recomputes it from the same weights; it is
		// set here too so the value is present on what the runner sends.
		pv.WouldContributePoints = WouldContributePoints(severity, in.SourceRole)
	}

	// SourceContent and MatchedText are deliberately left empty. Raw content
	// is persisted or exported only under the existing capture and redaction
	// policy, and the window offsets above already say where to look.
	r.cfg.Policy.RecordSemanticViolation(sessionID, pv)

	// A finding is logged at info; elevated evidence, which can be frequent
	// and means nothing on its own, at debug. Never content.
	level := slog.LevelInfo
	if ruleName == RuleInjectionElevated {
		level = slog.LevelDebug
	}
	slog.Log(context.Background(), level, "semantic decision recorded",
		"session_id", sessionID,
		"rule", ruleName,
		"severity", severity,
		"evidence_only", evidenceOnly,
		"would_contribute_points", pv.WouldContributePoints,
		"probability", v.Probability,
		"aux_probability", v.AuxProbability,
		"transform", v.Window.Transform,
		"execution_mode", v.ExecutionMode,
		"protection_scope", v.Scope,
		"coverage_complete", v.Coverage.Complete,
		"decision_id", v.DecisionID,
		"threshold_set", r.cfg.Model.ThresholdSet,
		"mode", r.mode,
	)
}

// describe writes the human-readable violation description. It names the
// representation and the thresholds, never the content.
func (r *Runner) describe(v Verdict, ruleName string) string {
	where := "original content"
	if v.Window.Transform != "" {
		where = "the " + v.Window.Transform + " representation"
	}
	if ruleName == RuleInjectionElevated {
		return fmt.Sprintf("semantic injection signal %.3f in %s: above the elevated threshold %.2f but below the violation threshold %.2f (evidence only, contributes no risk)",
			v.Probability, where, r.cfg.Thresholds.Elevated, r.cfg.Thresholds.Main)
	}
	return fmt.Sprintf("semantic injection signal %.3f in %s: at or above the violation threshold %.2f (threshold set %s)",
		v.Probability, where, r.cfg.Thresholds.Main, r.cfg.Model.ThresholdSet)
}

// recordShadow appends one bounded shadow entry. The caller holds the
// verdict's claim (handle), so it runs once per DecisionID. It never touches
// the risk score and carries no content.
func (r *Runner) recordShadow(sess *session.Session, in decision.Input, v Verdict) {
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
