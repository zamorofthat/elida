package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"elida/internal/config"
	"elida/internal/control"
	"elida/internal/decision"
	"elida/internal/decision/embedded"
	"elida/internal/decision/preprocess"
	"elida/internal/decision/runner"
	"elida/internal/decision/scheduler"
	"elida/internal/policy"
	"elida/internal/session"
)

// decisionSignals are the signals asked of the provider: the injection head
// and the human_directed veto head of the same window.
var decisionSignals = []decision.Signal{decision.SignalInjection, decision.SignalHumanDirected}

// initDecision brings up semantic injection detection, or exits when
// decision.required makes a startup failure fatal.
func (a *app) initDecision() {
	if err := a.setupDecision(context.Background()); err != nil {
		slog.Error("semantic detection failed to start and decision.required is true", "error", err)
		os.Exit(1)
	}
}

// setupDecision constructs the provider, scheduler and runner.
//
// Disabled by default. Startup modes:
//   - decision.enabled false, or decision.mode disabled: nothing is loaded.
//   - decision.required true: a missing or invalid model returns an error,
//     which initDecision turns into a failed startup.
//   - decision.required false: a bad model starts the provider degraded, a
//     WARN is logged, and the effective mode is off; the provider stays set
//     so /control/decision reports the capability and reason.
//
// Nothing here can create a violation: the runner starts in the configured
// mode (default shadow), capped by policy.mode, and the effective mode is
// logged once.
//
// decision.require_inline is not handled here: the key and its startup gate
// are added by Task 30.
func (a *app) setupDecision(ctx context.Context) error {
	d := a.cfg.Decision
	if !d.Enabled {
		return nil
	}
	if d.Mode == config.DecisionModeDisabled {
		slog.Info("semantic injection detection is enabled but decision.mode is disabled; no model is loaded")
		return nil
	}
	if d.Provider != "" && d.Provider != "embedded" {
		err := fmt.Errorf("decision.provider %q is not available in this build", d.Provider)
		if d.Required {
			return err
		}
		slog.Warn("semantic detection is OFF", "error", err,
			"consequence", "no injection detection is active")
		return nil
	}

	provider, err := embedded.New(ctx, embedded.Options{
		Enabled:      true,
		Required:     d.Required,
		ModelPath:    d.ModelPath,
		ThresholdSet: d.ThresholdSet,
		NewPipeline:  a.decisionPipeline,
	})
	if err != nil {
		// Only reachable with required: true, an explicit operator request
		// to fail rather than run unprotected.
		return err
	}
	a.decisionProvider = provider

	m := provider.Manifest()
	if m == nil {
		// Degraded: embedded.New already logged the load error prominently.
		slog.Warn("semantic detection is OFF: the provider is degraded and decision.required is false",
			"capability", provider.Health().Capability,
			"effective_mode", config.DecisionModeDisabled,
		)
		return nil
	}

	// The runner is the scheduler's async sink, but the scheduler must exist
	// before the runner. The callback reads the runner through an atomic
	// pointer that is stored before setupDecision returns, i.e. before any
	// request can queue async work, so no delivery ever finds it nil.
	var pending atomic.Pointer[runner.Runner]
	schCfg := decisionSchedulerConfig(d, provider, func(req scheduler.Request, in decision.Input, as decision.Assessment) {
		if r := pending.Load(); r != nil {
			r.OnAsync(req, in, as)
		}
	})
	// Kept so tests can inspect exactly the configuration the scheduler was
	// built with (the exact counter is the provider; Estimator stays nil).
	// New is handed the stored value itself, so what is inspected is what
	// was built.
	a.decisionSchedulerCfg = schCfg
	sch, err := scheduler.New(a.decisionSchedulerCfg)
	if err != nil {
		return a.abandonDecision(ctx, fmt.Errorf("semantic scheduler configuration is invalid: %w", err))
	}
	a.decisionScheduler = sch

	policyMode := "enforce"
	if a.cfg.Policy.Enabled && a.cfg.Policy.Mode != "" {
		policyMode = a.cfg.Policy.Mode
	}
	pe := a.policyEngine
	r, err := runner.New(runner.Config{
		Mode:       d.Mode,
		PolicyMode: policyMode,
		Scheduler:  sch,
		Budget: preprocess.Budget{
			MaxInputBytes:      d.Preprocessing.MaxInputBytes,
			MaxAnalysisBytes:   d.Preprocessing.MaxAnalysisBytes,
			MaxRepresentations: d.Preprocessing.MaxRepresentations,
			MaxDecodeDepth:     d.Preprocessing.MaxDecodeDepth,
			MaxExpansionRatio:  d.Preprocessing.MaxExpansionRatio,
		},
		Signals:    decisionSignals,
		Thresholds: decisionThresholds(m, d.ElevatedThreshold),
		Model: runner.ModelIdentity{
			Name:         m.Name,
			Version:      m.Version,
			Checksum:     m.Checksum(),
			ThresholdSet: d.ThresholdSet,
		},
		Strict:     d.InlineAdmission.BroadStrictMode,
		RiskLookup: riskLookup(pe),
	})
	if err != nil {
		return a.abandonDecision(ctx, fmt.Errorf("semantic runner configuration is invalid: %w", err))
	}
	pending.Store(r)
	a.decisionRunner = r

	h := provider.Health()
	slog.Info("semantic injection detection initialized",
		"mode", d.Mode,
		"effective_mode", r.EffectiveMode(),
		"policy_mode", policyMode,
		"capability", h.Capability,
		"model", h.Model,
		"version", h.Version,
		"threshold_set", h.ThresholdSet,
		"threshold_set_matches", h.ThresholdSetMatches,
	)
	if !h.ThresholdSetMatches {
		slog.Warn("decision.threshold_set does not match the loaded model's threshold set",
			"configured", d.ThresholdSet, "model", m.Calibration.ThresholdSet)
	}
	return nil
}

// abandonDecision undoes a partial setup. With decision.required true the
// error is returned and startup fails; otherwise it is logged and semantic
// detection stays off.
func (a *app) abandonDecision(ctx context.Context, err error) error {
	if a.decisionScheduler != nil {
		_ = a.decisionScheduler.Shutdown(ctx)
	}
	if a.decisionProvider != nil {
		_ = a.decisionProvider.Close()
	}
	a.decisionScheduler, a.decisionProvider, a.decisionRunner = nil, nil, nil
	if a.cfg.Decision.Required {
		return err
	}
	slog.Error("semantic detection disabled", "error", err)
	return nil
}

// decisionSchedulerConfig is the production scheduler configuration.
//
// The provider is the EXACT TokenCounter (its tokenizer). Estimator is left
// nil so the scheduler uses its cheap default for windowing: passing the
// provider as Estimator would run the tokenizer over whole messages on the
// request path.
func decisionSchedulerConfig(d config.DecisionConfig, provider *embedded.Provider, onAsync func(scheduler.Request, decision.Input, decision.Assessment)) scheduler.Config {
	return scheduler.Config{
		Provider:         provider,
		TokenCounter:     provider,
		Estimator:        nil,
		Signals:          decisionSignals,
		MaxConcurrency:   d.MaxConcurrency,
		InlineTimeout:    d.InlineTimeout,
		MaxInlineTokens:  d.MaxInlineTokens,
		MaxInlineWindows: d.MaxInlineWindows,
		MaxAsyncWindows:  d.MaxAsyncWindows,
		AsyncQueueSize:   d.AsyncQueueSize,
		Admission: scheduler.AdmissionPolicy{
			UntrustedToolResults: d.InlineAdmission.UntrustedToolResults,
			EncodedOrObfuscated:  d.InlineAdmission.EncodedOrObfuscated,
			WeakInjectionSignal:  d.InlineAdmission.WeakInjectionSignal,
			ElevatedSessionRisk:  d.InlineAdmission.ElevatedSessionRisk,
			BroadStrictMode:      d.InlineAdmission.BroadStrictMode,
		},
		OnAsync: onAsync,
	}
}

// decisionThresholds maps the manifest's calibrated thresholds onto
// severities. The calibrated main threshold is the warning line, and
// critical sits midway between it and certainty: starting points for
// calibration review, not tuned values.
func decisionThresholds(m *embedded.Manifest, elevated float64) runner.Thresholds {
	main := m.Calibration.MainThreshold
	return runner.Thresholds{
		Main:     main,
		Aux:      m.Calibration.AuxThreshold,
		Elevated: elevated,
		Warning:  main,
		Critical: main + (1-main)/2,
	}
}

// riskLookup reports the policy engine's view of a session's risk, which is
// how "this session is already elevated" reaches inline admission.
func riskLookup(pe *policy.Engine) func(string) (float64, string) {
	return func(sessionID string) (float64, string) {
		if pe == nil {
			return 0, ""
		}
		score, action, _ := pe.GetSessionRiskScore(sessionID)
		return score, action
	}
}

// decisionDrainLimit caps how long shutdown waits for queued semantic
// analysis. There is no operator key for it: async results are shadow
// evidence, and losing the tail of them at shutdown is a coverage gap, not
// a correctness problem.
const decisionDrainLimit = 5 * time.Second

// decisionDrainReserveFraction is the share of the remaining shutdown budget
// kept back for the steps after the drain (session drain and persistence,
// OCSF close, OTEL flush, storage close). A hung provider can therefore
// consume at most the rest, never the whole budget.
const decisionDrainReserveFraction = 0.5

// decisionDrainContext derives the drain's own deadline from the shutdown
// context: min(decisionDrainLimit, remaining budget minus the reserve).
// With no parent deadline, decisionDrainLimit alone applies. A budget
// already spent yields an expired context, which makes the scheduler cancel
// outstanding jobs at once.
func decisionDrainContext(parent context.Context) (context.Context, context.CancelFunc) {
	limit := decisionDrainLimit
	if dl, ok := parent.Deadline(); ok {
		remaining := time.Until(dl)
		share := time.Duration(float64(remaining) * (1 - decisionDrainReserveFraction))
		if share < limit {
			limit = share
		}
	}
	if limit < 0 {
		limit = 0
	}
	return context.WithTimeout(parent, limit)
}

// shutdownDecision drains queued semantic analysis under its own
// sub-deadline (decisionDrainContext) and then closes the provider. The
// caller has already stopped the proxy, so no new semantic work can start.
//
// A provider call that ignores its context may still be running on a
// worker when Close runs; the embedded provider answers unknown after
// Close, and the process is exiting, so that call's result is dropped.
func (a *app) shutdownDecision(ctx context.Context) {
	if a.decisionScheduler != nil {
		drainCtx, cancel := decisionDrainContext(ctx)
		err := a.decisionScheduler.Shutdown(drainCtx)
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("semantic scheduler drain did not finish", "error", err)
		}
	}
	if a.decisionProvider != nil {
		if err := a.decisionProvider.Close(); err != nil {
			slog.Error("semantic provider close error", "error", err)
		}
	}
}

// semanticAssessorFunc adapts a function to proxy.SemanticAssessor. The
// runner's adapter method is named AssessPolicyMessages so its
// policy-specific conversion is obvious at the call site; the proxy's
// interface method is AssessRequest.
type semanticAssessorFunc func(ctx context.Context, sess *session.Session, requestID string, msgs []policy.MessageToScan)

// AssessRequest implements proxy.SemanticAssessor.
func (f semanticAssessorFunc) AssessRequest(ctx context.Context, sess *session.Session, requestID string, msgs []policy.MessageToScan) {
	f(ctx, sess, requestID, msgs)
}

// DecisionStatus implements control.DecisionProvider. Ratios are derived
// from scheduler counters so an operator sees capacity pressure, not a fixed
// throughput claim.
func (a *app) DecisionStatus() control.DecisionStatus {
	d := a.cfg.Decision
	st := control.DecisionStatus{
		Enabled:       d.Enabled,
		Mode:          d.Mode,
		EffectiveMode: config.DecisionModeDisabled,
		Capability:    string(embedded.CapabilityDisabled),
		// Pinned: Phase 1 inline admission never waits for a worker, and
		// validation rejects any other value.
		InlineQueueWaitMs: 0,
		// Always present: empty when nothing runs, and carrying
		// messages_not_assessed as soon as a runner exists.
		CoverageGaps: map[string]int64{},
	}
	if a.decisionProvider == nil {
		return st
	}
	h := a.decisionProvider.Health()
	st.Capability = string(h.Capability)
	st.Reason = h.Reason
	st.Arch = h.Arch
	st.SIMD = h.SIMD
	st.Model = h.Model
	st.ModelVersion = h.Version
	st.ModelChecksum = h.Checksum
	st.ThresholdSet = h.ThresholdSet
	st.ThresholdSetMatches = h.ThresholdSetMatches
	st.BreakerOpen = h.BreakerOpen
	st.InputRejected = h.InputRejected
	for _, s := range h.Signals {
		st.Signals = append(st.Signals, string(s))
	}

	if a.decisionRunner != nil {
		st.EffectiveMode = a.decisionRunner.EffectiveMode()
		st.CoverageGaps[runner.GapMessagesNotAssessed] = 0
		for k, v := range a.decisionRunner.CoverageGaps() {
			st.CoverageGaps[k] = v
		}
		st.AlreadyAssessed = a.decisionRunner.AlreadyAssessed()
	}
	if a.decisionScheduler != nil {
		m := a.decisionScheduler.Metrics()
		st.AsyncQueueDepth = m.AsyncQueueDepth
		st.AsyncDropped = m.AsyncDropped
		st.MaxInFlight = m.MaxInFlight
		st.InlineSlots = m.InlineSlots
		st.AsyncWorkers = m.AsyncWorkers
		st.InlinePanics = m.InlinePanics
		st.AsyncCanceled = m.AsyncCanceled
		if m.InlineAttempted > 0 {
			st.InlineCompletionRatio = float64(m.InlineCompleted) / float64(m.InlineAttempted)
		}
		if total := m.InlineAttempted + m.InlineDenied; total > 0 {
			st.InlineAdmissionRatio = float64(m.InlineAttempted) / float64(total)
			st.AsyncFallbackRatio = float64(m.AsyncQueued) / float64(total)
		}
		st.AdmissionReasons = make(map[string]int64, len(m.AdmissionReasons))
		for k, v := range m.AdmissionReasons {
			st.AdmissionReasons[string(k)] = v
		}
	}
	return st
}
