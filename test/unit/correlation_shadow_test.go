package unit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/control"
	"elida/internal/correlation"
	"elida/internal/decision"
	"elida/internal/decision/decisiontest"
	"elida/internal/decision/runner"
	"elida/internal/decision/scheduler"
	"elida/internal/policy"
	"elida/internal/session"
)

// shadowCorrRunner is auditRunner's setup plus a shadow correlator built
// from the SHIPPED default correlation config.
func shadowCorrRunner(t *testing.T, mode string, scores map[decision.Signal]float64) (*runner.Runner, *policy.Engine, *correlation.Engine) {
	t.Helper()
	return shadowCorrRunnerWith(t, mode, decisiontest.NewFake(scores))
}

// shadowCorrRunnerWith is shadowCorrRunner over a caller-built fake (for
// a ScoreFunc gate).
func shadowCorrRunnerWith(t *testing.T, mode string, f *decisiontest.FakeProvider) (*runner.Runner, *policy.Engine, *correlation.Engine) {
	t.Helper()
	return shadowCorrRunnerWrapped(t, mode, f, nil)
}

// shadowCorrRunnerWrapped is shadowCorrRunnerWith whose runner sees the
// shipped correlator through wrap (nil: the correlator itself), for a test
// that gates Observe. The returned engine is the unwrapped one.
func shadowCorrRunnerWrapped(t *testing.T, mode string, f *decisiontest.FakeProvider, wrap func(*correlation.Engine) runner.ShadowCorrelator) (*runner.Runner, *policy.Engine, *correlation.Engine) {
	t.Helper()
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

	// The shadow correlator is built from the SHIPPED correlation block
	// (same rules, same limits) the policy engine's correlator uses.
	cc := config.DefaultConfig().Correlation
	shadowCorr, err := correlation.New(cc.Rules, cc.Limits())
	if err != nil || shadowCorr == nil {
		t.Fatalf("default correlation.New = (%v, %v)", shadowCorr, err)
	}
	var seen runner.ShadowCorrelator = shadowCorr
	if wrap != nil {
		seen = wrap(shadowCorr)
	}
	pe := newRiskLadderEngine(baselineRules(), baselineThresholds())
	r, err := runner.New(runner.Config{
		Mode:       mode,
		PolicyMode: "enforce",
		Scheduler:  sch,
		Budget:     runnerBudget(),
		Signals:    []decision.Signal{decision.SignalInjection, decision.SignalHumanDirected},
		Thresholds: runner.Thresholds{Main: 0.5, Aux: 0.64, Elevated: 0.3, Warning: 0.5, Critical: 0.8},
		Model:      runner.ModelIdentity{Name: "minilm-multihead", Version: "v5-fp32", Checksum: "abc123", ThresholdSet: "v1"},
		Policy:     pe,
		RiskLookup: func(sessionID string) (float64, string) {
			score, action, _ := pe.GetSessionRiskScore(sessionID)
			return score, action
		},
		ThresholdSetMatches: true,
		ShadowCorrelator:    seen,
	})
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	return r, pe, shadowCorr
}

func elevatedScores() map[decision.Signal]float64 {
	return map[decision.Signal]float64{decision.SignalInjection: 0.35, decision.SignalHumanDirected: 0.02}
}

func assessTool(r *runner.Runner, sess *session.Session, i int, content string) {
	r.AssessRequest(context.Background(), sess, fmt.Sprintf("req-%d", i),
		[]runner.Message{{Role: "tool", Index: i, Content: content}})
}

func TestShadowCorrelation_ThreeElevatedMessagesRecordAShadowMatch(t *testing.T) {
	r, pe, _ := shadowCorrRunner(t, "shadow", elevatedScores())
	sess := session.NewSession("sess-shadow-sb", "https://backend", "127.0.0.1:1")
	for i, p := range []string{"First mildly odd note.", "Second mildly odd note.", "Third mildly odd note."} {
		assessTool(r, sess, i, p)
	}
	recs := sess.GetCorrelationShadow()
	if len(recs) != 1 {
		t.Fatalf("want one correlation_shadow record, got %+v", recs)
	}
	rec := recs[0]
	if rec.Rule != "slow_burn_injection" || len(rec.EventIDs) != 3 || !strings.HasPrefix(rec.MatchID, "cm_") ||
		rec.Shape != "repeated" || rec.Mode != "audit" {
		t.Fatalf("record: %+v", rec)
	}
	decisionIDs := map[string]bool{}
	for _, sh := range sess.GetSemanticShadow() {
		decisionIDs[sh.DecisionID] = true
	}
	for _, id := range rec.EventIDs {
		if !decisionIDs[id] {
			t.Fatalf("contributing ID %q is not a recorded shadow DecisionID", id)
		}
	}
	if fs := pe.GetFlaggedSession("sess-shadow-sb"); fs != nil {
		t.Fatalf("shadow mode must record no violation, not even for a match: %+v", fs)
	}
}

func TestShadowCorrelation_OnlyTheElevatedBandIsAnInput(t *testing.T) {
	cases := []struct {
		name   string
		scores map[decision.Signal]float64
	}{
		{"below elevated", map[decision.Signal]float64{decision.SignalInjection: 0.12, decision.SignalHumanDirected: 0.02}},
		{"at or above main", map[decision.Signal]float64{decision.SignalInjection: 0.91, decision.SignalHumanDirected: 0.02}},
		{"vetoed", map[decision.Signal]float64{decision.SignalInjection: 0.35, decision.SignalHumanDirected: 0.9}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _, sc := shadowCorrRunner(t, "shadow", tc.scores)
			sess := session.NewSession("sess-band", "https://backend", "127.0.0.1:1")
			for i := 0; i < 4; i++ {
				assessTool(r, sess, i, fmt.Sprintf("Note number %d.", i))
			}
			if got := sc.Stats().EventsObserved; got != 0 {
				t.Fatalf("only elevated-band, non-vetoed decisions are inputs, observed %d", got)
			}
			if len(sess.GetCorrelationShadow()) != 0 {
				t.Fatal("no record expected")
			}
		})
	}
}

func TestShadowCorrelation_OneLongMessageIsNotASlowBurn(t *testing.T) {
	r, _, _ := shadowCorrRunner(t, "shadow", elevatedScores())
	sess := session.NewSession("sess-shadow-long", "https://backend", "127.0.0.1:1")
	assessTool(r, sess, 0, strings.Repeat("This sentence is ordinary filler that the window splitter will cut. ", 20))
	if n := len(sess.GetSemanticShadow()); n < 3 {
		t.Fatalf("precondition: want at least 3 shadow windows for the long message, got %d (lengthen the content)", n)
	}
	if len(sess.GetCorrelationShadow()) != 0 {
		t.Fatal("windows of one message count once (count_by: message)")
	}
}

func TestShadowCorrelation_AuditModeDoesNotFeedTheShadowCorrelator(t *testing.T) {
	r, _, sc := shadowCorrRunner(t, "audit", elevatedScores())
	sess := session.NewSession("sess-audit-no-shadow", "https://backend", "127.0.0.1:1")
	for i := 0; i < 3; i++ {
		assessTool(r, sess, i, fmt.Sprintf("Note %d.", i))
	}
	if sc.Stats().EventsObserved != 0 || len(sess.GetCorrelationShadow()) != 0 {
		t.Fatal("in audit the decision reaches correlation as a violation event; feeding the shadow correlator too would double count")
	}
}

func TestShadowCorrelation_UnbindForgets(t *testing.T) {
	r, _, sc := shadowCorrRunner(t, "shadow", elevatedScores())
	sess := session.NewSession("sess-unbind", "https://backend", "127.0.0.1:1")
	r.Bind(sess)
	assessTool(r, sess, 0, "One mildly odd note.")
	if sc.Stats().Sessions != 1 {
		t.Fatalf("precondition: one tracked session, got %+v", sc.Stats())
	}
	r.Unbind(sess)
	if sc.Stats().Sessions != 0 {
		t.Fatal("session end (Unbind) must forget shadow correlation state")
	}
}

func TestCorrelationShadow_NewestFirstCappedAndCopied(t *testing.T) {
	sess := session.NewSession("sess-cap", "https://backend", "127.0.0.1:1")
	for i := 0; i < session.MaxCorrelationShadow+10; i++ {
		sess.RecordCorrelationShadow(session.CorrelationShadow{MatchID: fmt.Sprintf("cm_%d", i), EventIDs: []string{"dec_x"}})
	}
	got := sess.GetCorrelationShadow()
	if len(got) != session.MaxCorrelationShadow || got[0].MatchID != fmt.Sprintf("cm_%d", session.MaxCorrelationShadow+9) {
		t.Fatalf("want %d newest-first entries, got %d (first %q)", session.MaxCorrelationShadow, len(got), got[0].MatchID)
	}
	got[0].EventIDs[0] = "tampered"
	snap := sess.Snapshot()
	if sess.GetCorrelationShadow()[0].EventIDs[0] != "dec_x" || snap.CorrelationShadow[0].EventIDs[0] != "dec_x" {
		t.Fatal("Get and Snapshot must deep-copy the list")
	}
}

func TestCorrelationShadow_OnTheSessionDetailAPIOnly(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	sess := manager.GetOrCreate("sess-cs-api", "https://backend", "127.0.0.1:1")
	sess.RecordCorrelationShadow(session.CorrelationShadow{MatchID: "cm_1", Rule: "slow_burn_injection", EventIDs: []string{"dec_a", "dec_b", "dec_c"}})
	h := control.New(store, manager)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/control/sessions/sess-cs-api", nil))
	var detail struct {
		CorrelationShadow []session.CorrelationShadow `json:"correlation_shadow"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || len(detail.CorrelationShadow) != 1 {
		t.Fatalf("detail must carry correlation_shadow: err=%v body=%s", err, w.Body.String())
	}

	wl := httptest.NewRecorder()
	h.ServeHTTP(wl, httptest.NewRequest(http.MethodGet, "/control/sessions", nil))
	if contains(wl.Body.String(), "correlation_shadow") {
		t.Fatal("the list endpoint must not carry correlation_shadow (same rule as semantic_shadow)")
	}
}

// A typed nil (what a nil *correlation.Engine is behind the interface)
// means disabled, not an error and not a nil-pointer call later.
func TestShadowCorrelation_TypedNilCorrelatorIsDisabled(t *testing.T) {
	var nilEngine *correlation.Engine
	r, err := runner.New(runner.Config{
		Mode:             "shadow",
		Scheduler:        mustShadowScheduler(t),
		Budget:           runnerBudget(),
		Signals:          []decision.Signal{decision.SignalInjection, decision.SignalHumanDirected},
		Thresholds:       runner.Thresholds{Main: 0.5, Aux: 0.64, Elevated: 0.3, Warning: 0.5, Critical: 0.8},
		Model:            runner.ModelIdentity{Name: "minilm-multihead", Version: "v5-fp32", Checksum: "abc123", ThresholdSet: "v1"},
		ShadowCorrelator: nilEngine,
	})
	if err != nil {
		t.Fatalf("a typed-nil ShadowCorrelator means disabled, not an error: %v", err)
	}
	sess := session.NewSession("sess-typed-nil", "https://backend", "127.0.0.1:1")
	r.Bind(sess)
	for i := 0; i < 3; i++ {
		assessTool(r, sess, i, fmt.Sprintf("Note %d.", i))
	}
	r.Unbind(sess)
	if len(sess.GetSemanticShadow()) == 0 {
		t.Fatal("precondition: shadow decisions are still recorded")
	}
	if len(sess.GetCorrelationShadow()) != 0 {
		t.Fatal("disabled shadow correlation records nothing")
	}
}

func mustShadowScheduler(t *testing.T) *scheduler.Inline {
	t.Helper()
	sch, err := scheduler.New(scheduler.Config{
		Provider:         decisiontest.NewFake(elevatedScores()),
		TokenCounter:     decisiontest.ByteTokenCounter{BytesPerToken: 4},
		Signals:          []decision.Signal{decision.SignalInjection, decision.SignalHumanDirected},
		MaxConcurrency:   2,
		InlineTimeout:    2 * time.Second,
		MaxInlineTokens:  4096,
		MaxInlineWindows: 8,
		AsyncQueueSize:   8,
		MaxWindowTokens:  64,
		Admission:        scheduler.AdmissionPolicy{UntrustedToolResults: true, WeakInjectionSignal: true},
	})
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	t.Cleanup(func() { _ = sch.Shutdown(context.Background()) })
	return sch
}

// A runtime switch uses the live effective mode: decisions made in audit go
// to the policy engine only, decisions made in shadow to the shadow
// correlator only. Evidence splits (a possible missed match); no decision is
// fed to both.
func TestShadowCorrelation_RuntimeSwitchSplitsEvidenceNeverDoubleCounts(t *testing.T) {
	r, pe, sc := shadowCorrRunner(t, "shadow", elevatedScores())
	sess := session.NewSession("sess-switch", "https://backend", "127.0.0.1:1")
	assessTool(r, sess, 0, "First mildly odd note.")
	assessTool(r, sess, 1, "Second mildly odd note.")
	l := r.Live()
	l.Mode = "audit"
	if err := r.Reconfigure(l); err != nil {
		t.Fatalf("Reconfigure: %v", err)
	}
	assessTool(r, sess, 2, "Third mildly odd note.")
	if got := sc.Stats().EventsObserved; got != 2 {
		t.Fatalf("only the two shadow-mode decisions reach the shadow correlator, observed %d", got)
	}
	if len(sess.GetCorrelationShadow()) != 0 {
		t.Fatal("split evidence: two shadow events cannot complete a count-3 match")
	}
	fs := pe.GetFlaggedSession("sess-switch")
	if fs == nil || len(fs.Violations) != 1 {
		t.Fatalf("the audit-mode decision reaches the policy engine once: %+v", fs)
	}
}

// Race detector target: AssessRequest, Reconfigure, Bind/Unbind (Forget)
// and the session readers all run concurrently. A start barrier releases
// every goroutine together, the mode keeps switching until the workers are
// done (so both modes are exercised: some decisions reach the shadow
// correlator and some do not), and a second group races an AssessRequest
// against its own session's end and Unbind (Task 8 review M3). Whatever the
// interleaving, no shadow correlation state may survive the sessions.
func TestShadowCorrelation_ConcurrentAssessReconfigureUnbind(t *testing.T) {
	r, _, sc := shadowCorrRunner(t, "shadow", elevatedScores())
	const workers, iterations, messages = 4, 20, 3
	const racers = 16
	start := make(chan struct{})
	var wg, reconf sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			for i := 0; i < iterations; i++ {
				sess := session.NewSession(fmt.Sprintf("sess-conc-%d-%d", w, i%3), "https://backend", "127.0.0.1:1")
				r.Bind(sess)
				for m := 0; m < messages; m++ {
					assessTool(r, sess, m, fmt.Sprintf("Note %d %d.", i, m))
				}
				_ = sess.GetCorrelationShadow()
				_ = sess.Snapshot()
				r.Unbind(sess)
			}
		}(w)
	}
	// Assess versus Unbind: each racer session is bound up front, then its
	// messages race the session's end and Unbind.
	raced := make([]*session.Session, racers)
	for i := range raced {
		sess := session.NewSession(fmt.Sprintf("sess-race-%d", i), "https://backend", "127.0.0.1:1")
		r.Bind(sess)
		raced[i] = sess
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			for m := 0; m < messages; m++ {
				assessTool(r, sess, 100+m, fmt.Sprintf("Racing note %d.", m))
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			sess.SetState(session.Completed)
			r.Unbind(sess)
		}()
	}
	workersDone := make(chan struct{})
	reconf.Add(1)
	go func() {
		defer reconf.Done()
		<-start
		for i := 0; ; i++ {
			select {
			case <-workersDone:
				return
			default:
			}
			l := r.Live()
			if i%2 == 0 {
				l.Mode = "audit"
			} else {
				l.Mode = "shadow"
			}
			_ = r.Reconfigure(l)
		}
	}()
	close(start)
	wg.Wait()
	close(workersDone)
	reconf.Wait()

	st := sc.Stats()
	if st.Sessions != 0 {
		t.Fatalf("every session was unbound, so no shadow state may remain: %+v", st)
	}
	for _, sess := range raced {
		if r.Bound(sess.ID) {
			t.Fatalf("an AssessRequest racing Unbind re-bound the ended session %s", sess.ID)
		}
	}
	total := uint64(workers*iterations*messages + racers*messages)
	if st.EventsObserved == 0 || st.EventsObserved >= total {
		t.Fatalf("both modes must be exercised: %d of %d decisions observed by the shadow correlator", st.EventsObserved, total)
	}
}
