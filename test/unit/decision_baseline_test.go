package unit

import (
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/panel"
	"elida/internal/policy"
	"elida/internal/proxy"
	"elida/internal/session"
)

// baselineRules is the single-rule set every baseline risk test uses. A
// "warning" content_match on a user message is worth
// SeverityWeights["warning"] (3.0) x SourceRoleWeights["user"] (1.0) = 3.0
// points at t=0.
func baselineRules() []policy.Rule {
	return []policy.Rule{{
		Name:     "baseline_marker",
		Type:     policy.RuleTypeContentMatch,
		Target:   policy.RuleTargetRequest,
		Patterns: []string{"zzmarkerzz"},
		Severity: policy.SeverityWarning,
		Action:   "flag",
	}}
}

func baselineThresholds() []policy.RiskThreshold {
	return []policy.RiskThreshold{
		{Score: 5, Action: policy.ActionWarn},
		{Score: 15, Action: policy.ActionThrottle, ThrottleRate: 10},
		{Score: 30, Action: policy.ActionBlock},
		{Score: 50, Action: policy.ActionTerminate},
	}
}

// TestBaseline_RiskAccumulatesAcrossRequests pins that each evaluation of the
// same rule appends another ViolationEvent and raises the score, i.e. risk is
// cumulative across requests and is NOT deduplicated by rule name.
func TestBaseline_RiskAccumulatesAcrossRequests(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())

	var scores []float64
	for i := 0; i < 4; i++ {
		e.EvaluateMessages("sess-accum", []policy.MessageToScan{
			{Role: "user", Index: i, Content: "please zzmarkerzz now"},
		})
		score, _, _ := e.GetSessionRiskScore("sess-accum")
		scores = append(scores, score)
	}

	for i := 1; i < len(scores); i++ {
		if scores[i] <= scores[i-1] {
			t.Fatalf("risk must accumulate across requests: scores=%v", scores)
		}
	}
	// 4 warning/user events with negligible decay over a few microseconds.
	if !approxEqual(scores[3], 12.0, 0.1) {
		t.Fatalf("4 warning/user events should score ~12.0, got %v (all=%v)", scores[3], scores)
	}

	fs := e.GetFlaggedSession("sess-accum")
	if fs == nil {
		t.Fatal("session should be flagged")
	}
	if len(fs.ViolationEvents) != 4 {
		t.Fatalf("expected 4 violation events, got %d", len(fs.ViolationEvents))
	}
	// The display list IS deduplicated by rule name; the event list is not.
	// Both halves of that asymmetry are load-bearing.
	if len(fs.Violations) != 1 {
		t.Fatalf("expected 1 deduplicated violation entry, got %d", len(fs.Violations))
	}
	if fs.ViolationCounts["baseline_marker"] != 4 {
		t.Fatalf("expected violation count 4, got %d", fs.ViolationCounts["baseline_marker"])
	}
}

// TestBaseline_RiskDecaysWithElapsedTime pins the exponential decay constant
// and that it is applied from the event timestamp.
func TestBaseline_RiskDecaysWithElapsedTime(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	e.EvaluateMessages("sess-decay", []policy.MessageToScan{
		{Role: "user", Index: 0, Content: "zzmarkerzz"},
	})

	fs := e.GetFlaggedSession("sess-decay")
	if fs == nil || len(fs.ViolationEvents) != 1 {
		t.Fatalf("expected exactly one event, got %#v", fs)
	}

	// ComputeRiskCurve evaluates the same events at arbitrary times, so it is
	// the only exported way to observe decay without sleeping.
	points := e.ComputeRiskCurve("sess-decay")
	if len(points) < 2 {
		t.Fatalf("expected at least two curve points, got %d", len(points))
	}
	first := points[0].Score
	if !approxEqual(first, 3.0, 0.05) {
		t.Fatalf("one warning/user event at t=0 should score ~3.0, got %v", first)
	}

	// Decay law: score(t) = 3.0 * exp(-0.002 * t_seconds).
	want := 3.0 * math.Exp(-policy.DefaultDecayLambda*600)
	if !approxEqual(want, 0.9036, 0.001) {
		t.Fatalf("decay constant drifted: 3.0*exp(-lambda*600)=%v", want)
	}
	if policy.DefaultDecayLambda != 0.002 {
		t.Fatalf("DefaultDecayLambda must remain 0.002, got %v", policy.DefaultDecayLambda)
	}
	if policy.MaxRiskScore != 100.0 {
		t.Fatalf("MaxRiskScore must remain 100.0, got %v", policy.MaxRiskScore)
	}
}

// TestBaseline_SourceRoleWeighting pins that the same rule firing on
// different roles produces different risk.
func TestBaseline_SourceRoleWeighting(t *testing.T) {
	cases := []struct {
		role string
		want float64
	}{
		{"user", 3.0},
		{"tool", 2.4},
		{"assistant", 0.6},
		{"system", 0.3},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			e := newRiskLadderEngine(baselineRules(), baselineThresholds())
			e.EvaluateMessages("sess-"+tc.role, []policy.MessageToScan{
				{Role: tc.role, Index: 0, Content: "zzmarkerzz"},
			})
			score, _, _ := e.GetSessionRiskScore("sess-" + tc.role)
			if !approxEqual(score, tc.want, 0.05) {
				t.Fatalf("role %q: score=%v want ~%v", tc.role, score, tc.want)
			}
		})
	}
	// Pin the weight table itself so a later task cannot retune it silently.
	for role, want := range map[string]float64{"user": 1.0, "tool": 0.8, "assistant": 0.2, "system": 0.1, "": 1.0} {
		if got := policy.SourceRoleWeights[role]; got != want {
			t.Fatalf("SourceRoleWeights[%q]=%v want %v", role, got, want)
		}
	}
	for sev, want := range map[policy.Severity]float64{policy.SeverityInfo: 1.0, policy.SeverityWarning: 3.0, policy.SeverityCritical: 10.0} {
		if got := policy.SeverityWeights[sev]; got != want {
			t.Fatalf("SeverityWeights[%q]=%v want %v", sev, got, want)
		}
	}
}

// TestBaseline_RiskCurveAgreesWithScore pins that ComputeRiskCurve's last
// point equals the authoritative score. Task 27 changes both functions and
// this test is what proves they stay in agreement.
func TestBaseline_RiskCurveAgreesWithScore(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	for i := 0; i < 3; i++ {
		e.EvaluateMessages("sess-curve", []policy.MessageToScan{
			{Role: "user", Index: i, Content: "zzmarkerzz"},
		})
	}
	score, _, _ := e.GetSessionRiskScore("sess-curve")
	points := e.ComputeRiskCurve("sess-curve")
	if len(points) == 0 {
		t.Fatal("expected curve points")
	}
	last := points[len(points)-1].Score
	if !approxEqual(last, score, 0.02) {
		t.Fatalf("curve tail %v disagrees with score %v", last, score)
	}
}

// TestBaseline_PreForwardBlockByRisk pins that the risk-ladder block check
// happens BEFORE the request reaches the backend (proxy.go:386-396), and that
// a sub-threshold session is forwarded untouched.
func TestBaseline_PreForwardBlockByRisk(t *testing.T) {
	var forwarded int
	var gotBody string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded++
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer backend.Close()

	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	cfg := &config.Config{
		Backend: backend.URL,
		Session: config.SessionConfig{Header: "X-Session-ID", GenerateIfMissing: true, Timeout: 5 * time.Minute},
	}
	// Terminate threshold is raised out of reach so the ladder blocks rather
	// than killing the session; this test is about the pre-forward gate.
	pe := newRiskLadderEngine(baselineRules(), []policy.RiskThreshold{
		{Score: 5, Action: policy.ActionBlock},
		{Score: 1000, Action: policy.ActionTerminate},
	})
	p, err := proxy.New(cfg, store, manager, proxy.WithPolicyEngine(pe))
	if err != nil {
		t.Fatalf("proxy.New: %v", err)
	}

	body := `{"messages":[{"role":"user","content":"hello world"}]}`
	send := func() int {
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Session-ID", "sess-block")
		w := httptest.NewRecorder()
		p.ServeHTTP(w, req)
		return w.Result().StatusCode
	}

	if code := send(); code != http.StatusOK {
		t.Fatalf("clean request should be forwarded, got %d", code)
	}
	if forwarded != 1 {
		t.Fatalf("expected 1 forwarded request, got %d", forwarded)
	}
	if gotBody != body {
		t.Fatalf("forwarded body was rewritten:\n got %q\nwant %q", gotBody, body)
	}

	// Drive the score over the block threshold (5) with two user events (6.0).
	for i := 0; i < 2; i++ {
		pe.EvaluateMessages("sess-block", []policy.MessageToScan{
			{Role: "user", Index: i, Content: "zzmarkerzz"},
		})
	}
	if !pe.ShouldBlockByRisk("sess-block") {
		score, _, _ := pe.GetSessionRiskScore("sess-block")
		t.Fatalf("expected ShouldBlockByRisk after 2 events, score=%v", score)
	}
	if code := send(); code != http.StatusForbidden {
		t.Fatalf("over-threshold request should be 403, got %d", code)
	}
	if forwarded != 1 {
		t.Fatalf("blocked request must not reach the backend; forwarded=%d", forwarded)
	}
}

// TestBaseline_CompoundAnomalyScores pins the compound detector's warmup and
// alarm behavior so preprocessing work cannot perturb it.
func TestBaseline_CompoundAnomalyScores(t *testing.T) {
	det := policy.NewSessionDetector(policy.CompoundAnomalyConfig{})
	now := time.Now()
	for i := 0; i < policy.DefaultWarmupRequests; i++ {
		if s := det.Update(now.Add(time.Duration(i)*100*time.Millisecond), []byte("data")); s != 0 {
			t.Fatalf("warmup request %d scored %v, want 0", i, s)
		}
	}
	steady := []byte(`{"role":"user","content":"normal request"}`)
	for i := 0; i < 40; i++ {
		s := det.Update(now.Add(time.Duration(i)*500*time.Millisecond), steady)
		if s > policy.DefaultCompoundThreshold {
			t.Fatalf("steady traffic alarmed at request %d: score=%v", i, s)
		}
	}
}

// stubMember is a panel member whose opinion is fixed, used to prove that a
// shadow seat never moves the verdict.
type stubMember struct {
	name    string
	anomaly float64
	class   string
}

func (m stubMember) Name() string    { return m.name }
func (m stubMember) Version() string { return "baseline" }
func (m stubMember) Assess(panel.SessionFeatures) panel.MemberOpinion {
	return panel.MemberOpinion{Member: m.name, Anomaly: m.anomaly, Class: m.class, Confidence: 1}
}

// TestBaseline_ShadowPanelMemberDoesNotMoveVerdict pins the shadow-seat
// contract the tool-chain member relies on (cmd/elida/main.go:308-327).
func TestBaseline_ShadowPanelMemberDoesNotMoveVerdict(t *testing.T) {
	snap := &session.Session{ID: "sess-panel"}
	features := panel.BuildFeatures(snap)

	base := panel.NewPanel()
	base.Seat(stubMember{name: "live", anomaly: 0.25, class: "coding"}, false, 1.0)
	want := base.Assess(features)

	withShadow := panel.NewPanel()
	withShadow.Seat(stubMember{name: "live", anomaly: 0.25, class: "coding"}, false, 1.0)
	withShadow.Seat(stubMember{name: "shadow", anomaly: 0.99, class: "exfil"}, true, 0)
	got := withShadow.Assess(features)

	if got.RiskScore != want.RiskScore {
		t.Fatalf("shadow seat moved RiskScore: %v -> %v", want.RiskScore, got.RiskScore)
	}
	if got.Class != want.Class {
		t.Fatalf("shadow seat moved Class: %q -> %q", want.Class, got.Class)
	}
	if len(got.Members) != 2 {
		t.Fatalf("shadow member must still be reported, got %d opinions", len(got.Members))
	}
}
