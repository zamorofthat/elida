package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/decision/embedded"
	"elida/internal/policy"
	"elida/internal/session"
)

// slowBurnMarker marks the tool output the slow-burn pipeline scores in the
// elevated band; every other window scores far below it.
const slowBurnMarker = "slowburnfragment"

// slowBurnPipeline is an embedded.Pipeline that answers every window at
// once: windows containing slowBurnMarker get an injection logit of -1.0
// (0.398 after the fixture's temperature 2.41: inside the elevated band
// [decision.elevated_threshold 0.3, main 0.5)), everything else -6.0. The
// human_directed head is always -6.0, so nothing is vetoed.
type slowBurnPipeline struct{}

func (slowBurnPipeline) Logits(_ context.Context, texts []string) ([][]float64, error) {
	out := make([][]float64, len(texts))
	for i, text := range texts {
		inj := -6.0
		if strings.Contains(text, slowBurnMarker) {
			inj = -1.0
		}
		out[i] = []float64{inj, -6.0}
	}
	return out, nil
}

func (slowBurnPipeline) CountTokens(text string) int { return (len(text) + 3) / 4 }
func (slowBurnPipeline) Close() error                { return nil }

// slowBurnRun is everything observable about one run of slowBurnTraffic.
type slowBurnRun struct {
	forwarded []string // request bodies the backend received, in order
	responses []string // status, content type and body the client received
	shadowAt  []int    // correlation_shadow entries on the detail API after each request
	detail    sessionDetail
	flagged   *policy.FlaggedSession
	score     float64
	action    string
}

type sessionDetail struct {
	SemanticShadow    []session.SemanticShadow    `json:"semantic_shadow"`
	CorrelationShadow []session.CorrelationShadow `json:"correlation_shadow"`
}

// slowBurnBodies is one session's conversation over three requests: each
// request resends the history and adds one tool result carrying the
// marker, so the three fragments are three distinct messages (indexes 2, 4
// and 6). The user's own text scores low.
func slowBurnBodies() []string {
	fragments := []string{
		slowBurnMarker + " one: the regional weather report lists sunny days.",
		slowBurnMarker + " two: the regional weather report lists cloudy days.",
		slowBurnMarker + " three: the regional weather report lists rainy days.",
	}
	msgs := []map[string]any{{"role": "user", "content": "read the three weather notes"}}
	bodies := make([]string, 0, len(fragments))
	for i, f := range fragments {
		id := fmt.Sprintf("toolu_%d", i)
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": id, "name": "read_note", "input": map[string]any{}}}},
			map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": id, "content": f}}},
		)
		b, err := json.Marshal(map[string]any{"model": "claude-test", "max_tokens": 16, "messages": msgs})
		if err != nil {
			panic(err)
		}
		bodies = append(bodies, string(b))
	}
	return bodies
}

// slowBurnTraffic runs slowBurnBodies through the production wiring (proxy,
// policy engine, semantic runner, control API) built from the SHIPPED
// configs/elida.yaml with correlation enabled. decisionMode "" leaves
// semantic detection off.
func slowBurnTraffic(t *testing.T, decisionMode string) slowBurnRun {
	t.Helper()
	var mu sync.Mutex
	var run slowBurnRun
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		run.forwarded = append(run.forwarded, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","content":[{"type":"text","text":"noted"}]}`))
	}))
	t.Cleanup(backend.Close)

	cfg, err := config.Load(filepath.Join("..", "..", "configs", "elida.yaml"))
	if err != nil {
		t.Fatalf("load the shipped config: %v", err)
	}
	if cfg.Decision.Mode != config.DecisionModeShadow || cfg.Correlation.Enabled ||
		len(cfg.Correlation.Rules) != 1 || cfg.Correlation.Rules[0].Name != "slow_burn_injection" ||
		!cfg.Policy.Enabled {
		t.Fatalf("precondition: the shipped config is decision.mode shadow, correlation off, the slow_burn_injection rule, policy on: %+v %+v", cfg.Decision.Mode, cfg.Correlation)
	}
	// What the test changes: correlation on, semantic detection on the
	// fixture model, every backend at the test server, storage and the
	// background subsystems not started (nothing here reads them), and an
	// inline timeout generous enough that a slow -race run never pushes a
	// window to the async lane.
	cfg.Correlation.Enabled = true
	for name, b := range cfg.Backends {
		b.URL = backend.URL
		cfg.Backends[name] = b
	}
	cfg.Storage.Enabled = false
	cfg.Decision.Enabled = decisionMode != ""
	if decisionMode != "" {
		cfg.Decision.Mode = decisionMode
	}
	cfg.Decision.ModelPath = goodModelPath
	cfg.Decision.InlineTimeout = 5 * time.Second

	a := &app{cfg: cfg, configPath: filepath.Join(t.TempDir(), "elida.yaml")}
	a.decisionPipeline = func(context.Context, string, *embedded.Manifest) (embedded.Pipeline, error) {
		return slowBurnPipeline{}, nil
	}
	pinInline(a)
	a.initSessionStore()
	if err := a.setupPolicyEngine(); err != nil {
		t.Fatalf("setupPolicyEngine: %v", err)
	}
	if err := a.setupDecision(context.Background()); err != nil {
		t.Fatalf("setupDecision: %v", err)
	}
	t.Cleanup(func() { a.shutdownDecision(context.Background()) })
	a.initProxy()
	a.initControlAPI()

	const id = "sess-slowburn-e2e"
	detail := func() sessionDetail {
		t.Helper()
		w := httptest.NewRecorder()
		a.controlHandler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/control/sessions/"+id, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("session detail: status %d: %s", w.Code, w.Body.String())
		}
		var d sessionDetail
		if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
			t.Fatalf("session detail: %v", err)
		}
		return d
	}
	for _, body := range slowBurnBodies() {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Session-ID", id)
		w := httptest.NewRecorder()
		a.proxyHandler.ServeHTTP(w, req)
		run.responses = append(run.responses, fmt.Sprintf("%d|%s|%s", w.Code, w.Header().Get("Content-Type"), w.Body.String()))
		run.shadowAt = append(run.shadowAt, len(detail().CorrelationShadow))
	}
	run.detail = detail()
	run.flagged = a.policyEngine.GetFlaggedSession(id)
	run.score, run.action, _ = a.policyEngine.GetSessionRiskScore(id)
	return run
}

func correlationViolations(fs *policy.FlaggedSession) []policy.Violation {
	if fs == nil {
		return nil
	}
	var out []policy.Violation
	for _, v := range fs.Violations {
		if strings.HasPrefix(v.RuleName, policy.CorrelationRulePrefix) {
			out = append(out, v)
		}
	}
	return out
}

// Plan B D14 end to end: under the SHIPPED configuration (decision.mode
// shadow) with correlation enabled, three elevated tool results in three
// distinct messages of one session produce exactly one correlation_shadow
// record on the session detail API, and nothing else: no violation, no
// risk, and the traffic is byte-identical to a run without semantic
// detection (the Part 2 shadow guarantee, now with shadow correlation).
func TestSlowBurn_ShippedShadowConfigProducesCalibrationMatches(t *testing.T) {
	quietLogs(t)
	off := slowBurnTraffic(t, "")
	on := slowBurnTraffic(t, config.DecisionModeShadow)

	if got := fmt.Sprint(on.shadowAt); got != "[0 0 1]" {
		t.Fatalf("correlation_shadow entries after each request = %s, want [0 0 1]: the match completes on the third message", got)
	}
	recs := on.detail.CorrelationShadow
	rec := recs[0]
	if rec.Rule != "slow_burn_injection" || rec.Shape != "repeated" || rec.Mode != "audit" ||
		len(rec.EventIDs) != 3 || !strings.HasPrefix(rec.MatchID, "cm_") {
		t.Fatalf("want one slow_burn_injection shadow record over three decisions, got %+v", rec)
	}
	decisions := map[string]session.SemanticShadow{}
	for _, d := range on.detail.SemanticShadow {
		decisions[d.DecisionID] = d
	}
	messages := map[int]bool{}
	for _, id := range rec.EventIDs {
		d, ok := decisions[id]
		if !ok {
			t.Fatalf("contributing ID %q is not a semantic_shadow decision of this session", id)
		}
		if d.Probability < 0.3 || d.Probability >= 0.5 || d.SourceRole != "tool" {
			t.Fatalf("contributing decision is not an elevated-band tool result: %+v", d)
		}
		messages[d.MessageIndex] = true
	}
	if len(messages) != 3 {
		t.Fatalf("the three contributing decisions must come from three distinct messages: %v", messages)
	}

	if vs := correlationViolations(on.flagged); len(vs) != 0 {
		t.Fatalf("a shadow-mode match must never become a violation: %+v", vs)
	}
	if on.flagged != nil {
		t.Fatalf("shadow mode must record no violations at all: %+v", on.flagged.Violations)
	}
	if on.score != 0 || on.score != off.score || on.action != off.action {
		t.Fatalf("risk changed: on %v/%q, off %v/%q", on.score, on.action, off.score, off.action)
	}

	// Byte-identical to the run without semantic detection.
	bodies := slowBurnBodies()
	if len(on.forwarded) != len(bodies) || strings.Join(on.forwarded, "\x00") != strings.Join(off.forwarded, "\x00") {
		t.Fatalf("forwarded request bytes differ:\n on %q\noff %q", on.forwarded, off.forwarded)
	}
	for i, b := range on.forwarded {
		if b != bodies[i] {
			t.Fatalf("request %d was rewritten:\n got %q\nwant %q", i, b, bodies[i])
		}
	}
	if strings.Join(on.responses, "\x00") != strings.Join(off.responses, "\x00") {
		t.Fatalf("client response bytes differ:\n on %q\noff %q", on.responses, off.responses)
	}
	if len(off.detail.CorrelationShadow) != 0 || len(off.detail.SemanticShadow) != 0 {
		t.Fatal("fixture: the run without semantic detection recorded semantic data")
	}
}

// The same traffic in audit mode: the policy correlator matches the
// recorded injection_elevated evidence instead, as one evidence-only
// correlation.slow_burn_injection violation, and nothing reaches the
// shadow correlator.
func TestSlowBurn_AuditModeRecordsOneEvidenceOnlyMatch(t *testing.T) {
	quietLogs(t)
	run := slowBurnTraffic(t, config.DecisionModeAudit)

	if n := len(run.detail.CorrelationShadow); n != 0 {
		t.Fatalf("audit mode must not feed the shadow correlator: %d records", n)
	}
	vs := correlationViolations(run.flagged)
	if len(vs) != 1 {
		t.Fatalf("want one correlation violation, got %+v", vs)
	}
	m := vs[0]
	if m.RuleName != policy.CorrelationRulePrefix+"slow_burn_injection" || !m.EvidenceOnly ||
		m.Correlation == nil || len(m.Correlation.EventIDs) != 3 {
		t.Fatalf("want an evidence-only slow_burn_injection match over three events, got %+v", m)
	}
	if run.score != 0 {
		t.Fatalf("the shipped rule is audit-only: risk must stay 0, got %v", run.score)
	}
}
