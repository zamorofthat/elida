package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/correlation"
	"elida/internal/decision"
	"elida/internal/decision/decisiontest"
	"elida/internal/decision/preprocess"
	"elida/internal/decision/runner"
	"elida/internal/decision/scheduler"
	"elida/internal/session"
)

// unbindingCorrelator wraps a shadow correlator and, on the first Observe
// that produces a match, unbinds the observed session before returning, so
// the runner's post-Observe re-check finds the instance gone and drops the
// match (the ended-mid-Observe case ShadowMatchesDropped counts).
type unbindingCorrelator struct {
	inner  *correlation.Engine
	runner *runner.Runner
	sess   *session.Session
	fired  bool
}

func (u *unbindingCorrelator) Observe(key string, ev []correlation.Event) []correlation.Match {
	matches := u.inner.Observe(key, ev)
	if len(matches) > 0 && !u.fired {
		u.fired = true
		u.runner.Unbind(u.sess) // Observe runs outside boundMu, so this cannot deadlock
	}
	return matches
}

func (u *unbindingCorrelator) Forget(key string) { u.inner.Forget(key) }

// shadowRunnerWith builds a shadow-mode runner over an elevated-band fake
// provider whose shadow correlator is wrapped by wrap.
func shadowRunnerWith(t *testing.T, wrap func(*correlation.Engine) runner.ShadowCorrelator) *runner.Runner {
	t.Helper()
	f := decisiontest.NewFake(map[decision.Signal]float64{decision.SignalInjection: 0.35, decision.SignalHumanDirected: 0.02})
	sch, err := scheduler.New(scheduler.Config{
		Provider:         f,
		TokenCounter:     decisiontest.ByteTokenCounter{BytesPerToken: 4},
		Signals:          []decision.Signal{decision.SignalInjection, decision.SignalHumanDirected},
		MaxConcurrency:   2,
		InlineTimeout:    2 * time.Second,
		MaxInlineTokens:  4096,
		MaxInlineWindows: 8,
		MaxAsyncWindows:  0,
		AsyncQueueSize:   8,
		MaxWindowTokens:  64,
		Admission:        scheduler.AdmissionPolicy{UntrustedToolResults: true, WeakInjectionSignal: true},
	})
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	t.Cleanup(func() { _ = sch.Shutdown(context.Background()) })
	cc := config.DefaultConfig().Correlation
	shadowCorr, err := correlation.New(cc.Rules, cc.Limits())
	if err != nil || shadowCorr == nil {
		t.Fatalf("default correlation.New = (%v, %v)", shadowCorr, err)
	}
	r, err := runner.New(runner.Config{
		Mode:      config.DecisionModeShadow,
		Scheduler: sch,
		Budget: preprocess.Budget{
			MaxInputBytes:      262144,
			MaxAnalysisBytes:   524288,
			MaxRepresentations: 8,
			MaxDecodeDepth:     2,
			MaxExpansionRatio:  4,
		},
		Signals:             []decision.Signal{decision.SignalInjection, decision.SignalHumanDirected},
		Thresholds:          runner.Thresholds{Main: 0.5, Aux: 0.64, Elevated: 0.3, Warning: 0.5, Critical: 0.8},
		Model:               runner.ModelIdentity{Name: "minilm-multihead", Version: "v5-fp32", Checksum: "abc123", ThresholdSet: "v1"},
		ThresholdSetMatches: true,
		ShadowCorrelator:    wrap(shadowCorr),
	})
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	return r
}

// Final review N-2: /control/decision carries shadow_matches_dropped (the
// runner's ShadowMatchesDropped), a content-free integer that is present
// at 0 and increments when a shadow match is dropped because its session
// instance ended while the match was being computed.
func TestDecisionStatus_ReportsShadowMatchesDropped(t *testing.T) {
	quietLogs(t)
	cfg := decisionTestConfig(t)
	cfg.Correlation.Enabled = true
	a := newDecisionApp(t, cfg, &gate{})
	a.initSessionStore()
	if err := a.setupDecision(context.Background()); err != nil {
		t.Fatalf("setupDecision: %v", err)
	}
	t.Cleanup(func() { a.shutdownDecision(context.Background()) })

	// The status reads the app's runner; this one drops exactly one match.
	u := &unbindingCorrelator{sess: session.NewSession("sess-drop", "https://backend", "127.0.0.1:1")}
	r := shadowRunnerWith(t, func(e *correlation.Engine) runner.ShadowCorrelator {
		u.inner = e
		return u
	})
	u.runner = r
	a.decisionRunner = r

	statusKey := func() (int64, bool) {
		raw, err := json.Marshal(a.DecisionStatus())
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		v, ok := m["shadow_matches_dropped"]
		if !ok {
			t.Fatalf("status lacks shadow_matches_dropped: %s", raw)
		}
		n, isNum := v.(float64)
		return int64(n), isNum
	}
	if n, isNum := statusKey(); !isNum || n != 0 {
		t.Fatalf("shadow_matches_dropped before any drop = %v (number %v), want 0", n, isNum)
	}

	// Three elevated messages complete the shipped slow_burn_injection
	// rule; the wrapper unbinds the session inside that Observe.
	for i, c := range []string{"First mildly odd note.", "Second mildly odd note.", "Third mildly odd note."} {
		r.AssessRequest(context.Background(), u.sess, fmt.Sprintf("req-%d", i), []runner.Message{{Role: "tool", Index: i, Content: c}})
	}
	if !u.fired {
		t.Fatal("fixture: the wrapper must have seen a match and unbound the session")
	}
	if got := r.ShadowMatchesDropped(); got != 1 {
		t.Fatalf("fixture: runner ShadowMatchesDropped = %d, want 1", got)
	}
	if n, _ := statusKey(); n != 1 {
		t.Fatalf("shadow_matches_dropped after a dropped match = %d, want 1", n)
	}
	if recs := u.sess.GetCorrelationShadow(); len(recs) != 0 {
		t.Fatalf("a dropped match must not be recorded on the session: %+v", recs)
	}
}
