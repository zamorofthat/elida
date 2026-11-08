package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/fingerprint"
	"elida/internal/panel"
	"elida/internal/session"
	"elida/internal/storage"
	"elida/internal/telemetry"
)

func TestRiskPointsFromVerdict_ReproducesM3LiteLadder(t *testing.T) {
	for _, b := range []string{fingerprint.BucketNormal, fingerprint.BucketMinor, fingerprint.BucketNotable, fingerprint.BucketAnomalous, fingerprint.BucketSevere} {
		v := panel.Verdict{RiskScore: float64(fingerprint.BucketRiskPoints(b)) / float64(fingerprint.RiskSevere), Driver: "m3-lite"}
		got, src := riskPointsFromVerdict(v)
		if got != fingerprint.BucketRiskPoints(b) || src != "m3-lite" {
			t.Fatalf("bucket %s: points=%d src=%q, want %d / m3-lite", b, got, src, fingerprint.BucketRiskPoints(b))
		}
	}
}

func TestRiskPointsFromVerdict_ToolChainDriverAndWeight(t *testing.T) {
	// 0.99 percentile at weight 0.5 contributes 0.495 -> round(0.495*20) = 10 points.
	got, src := riskPointsFromVerdict(panel.Verdict{RiskScore: 0.5 * 0.99, Driver: "tool-chain"})
	if got != 10 || src != "tool-chain" {
		t.Fatalf("points=%d src=%q, want 10 / tool-chain", got, src)
	}
}

func TestSeatToolChain_DefaultsShadowAndConfiguredPosture(t *testing.T) {
	art, err := panel.LoadToolChainArtifact("../../internal/panel/testdata/tool-chain.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		cfg    config.PanelConfig
		shadow bool
		weight float64
	}{
		{config.PanelConfig{ToolChainShadow: true, ToolChainWeight: 1.0}, true, 1.0},
		{config.PanelConfig{ToolChainShadow: false, ToolChainWeight: 0.5}, false, 0.5},
	} {
		p := panel.NewPanel()
		seatToolChain(p, c.cfg, art)
		m := p.Members()[0]
		if m.Name != "tool-chain" || m.Shadow != c.shadow || m.Weight != c.weight {
			t.Fatalf("seated %+v, want shadow=%v weight=%v", m, c.shadow, c.weight)
		}
	}
}

func TestPanelOpinions_AllowlistDropsUnknownDetailKeys(t *testing.T) {
	v := panel.Verdict{Members: []panel.MemberOpinion{
		{Member: "tool-chain", Shadow: true, Weight: 1, Anomaly: 0.97, Detail: map[string]any{
			"perplexity": 3.2, "percentile": 0.97, "class_used": "global", "content": "SECRET-PROMPT", "tool_args": "SECRET-ARGS"}},
		{Member: "m3-lite", Weight: 1, Anomaly: 0.25, Detail: map[string]any{"bucket": "notable", "distance": 4.5, "shadow": false, "x": "SECRET"}},
	}}
	got := panelOpinions(v)
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "SECRET") {
		t.Fatalf("content leaked into opinions: %s", raw)
	}
	if len(got) != 2 || got[0].Percentile != 0.97 || got[0].ClassUsed != "global" || got[1].Bucket != "notable" || got[1].Distance != 4.5 {
		t.Fatalf("allowlisted fields lost: %+v", got)
	}
}

// A detail value of the wrong type is dropped rather than panicking or
// stringifying: every read is a checked type assertion.
func TestPanelOpinions_WrongTypedDetailValuesAreDropped(t *testing.T) {
	v := panel.Verdict{Members: []panel.MemberOpinion{
		{Member: "tool-chain", Shadow: true, Weight: 0.5, Anomaly: 0.5, Veto: true, Detail: map[string]any{
			"perplexity": "SECRET", "percentile": nil, "class_used": 7, "reason": []string{"SECRET"}}},
		{Member: "m3-lite", Weight: 1, Detail: nil},
	}}
	got := panelOpinions(v)
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "SECRET") {
		t.Fatalf("a wrongly-typed detail value leaked: %s", raw)
	}
	want := []storageOpinionShape{
		{Member: "tool-chain", Shadow: true, Weight: 0.5, Anomaly: 0.5, Veto: true},
		{Member: "m3-lite", Weight: 1},
	}
	for i, w := range want {
		if got[i].Member != w.Member || got[i].Shadow != w.Shadow || got[i].Weight != w.Weight || got[i].Anomaly != w.Anomaly || got[i].Veto != w.Veto {
			t.Fatalf("opinion %d = %+v, want the seat fields %+v with no detail", i, got[i], w)
		}
		if got[i].Bucket != "" || got[i].Distance != 0 || got[i].Perplexity != 0 || got[i].Percentile != 0 || got[i].ClassUsed != "" || got[i].Reason != "" {
			t.Fatalf("opinion %d kept a detail field: %+v", i, got[i])
		}
	}
	if panelOpinions(panel.Verdict{}) != nil {
		t.Fatal("a verdict with no members must produce no opinions")
	}
}

// storageOpinionShape is the seat half of a storage.PanelOpinion, for the
// table above.
type storageOpinionShape struct {
	Member  string
	Shadow  bool
	Weight  float64
	Anomaly float64
	Veto    bool
}

// capturedRecord is one slog record, message and attributes, as seen by
// captureRecords.
type capturedRecord struct {
	Message string
	Attrs   map[string]any
}

func (c capturedRecord) String() string { return fmt.Sprintf("%s %+v", c.Message, c.Attrs) }

type captureHandler struct {
	mu   *sync.Mutex
	recs *[]capturedRecord
}

func (h captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h captureHandler) Handle(_ context.Context, r slog.Record) error {
	rec := capturedRecord{Message: r.Message, Attrs: make(map[string]any, r.NumAttrs())}
	r.Attrs(func(a slog.Attr) bool {
		rec.Attrs[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.recs = append(*h.recs, rec)
	return nil
}

func (h captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h captureHandler) WithGroup(string) slog.Handler      { return h }

// captureRecords installs a handler that records every slog record and
// returns an accessor for them. The previous default is restored on cleanup.
func captureRecords(t *testing.T) func() []capturedRecord {
	t.Helper()
	var mu sync.Mutex
	var recs []capturedRecord
	prev := slog.Default()
	slog.SetDefault(slog.New(captureHandler{mu: &mu, recs: &recs}))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() []capturedRecord {
		mu.Lock()
		defer mu.Unlock()
		return append([]capturedRecord(nil), recs...)
	}
}

func countRecords(recs []capturedRecord, msg string) int {
	n := 0
	for _, r := range recs {
		if r.Message == msg {
			n++
		}
	}
	return n
}

// panelScoreApp builds an app from the shipped config with SQLite storage,
// the real M3-lite fingerprinter and the panel seated by initPanel. mutate
// adjusts the loaded config before anything is wired.
func panelScoreApp(t *testing.T, mutate func(*config.Config)) *app {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "..", "configs", "elida.yaml"))
	if err != nil {
		t.Fatalf("load the shipped config: %v", err)
	}
	cfg.Storage.Enabled = true
	cfg.Storage.Path = filepath.Join(t.TempDir(), "elida.db")
	if mutate != nil {
		mutate(cfg)
	}
	a := &app{cfg: cfg, configPath: filepath.Join(t.TempDir(), "elida.yaml")}
	a.initSQLiteStorage()
	t.Cleanup(func() { _ = a.sqliteStore.Close() })
	a.initFingerprint()
	a.initPanel()
	if a.fingerprinter == nil || a.panel == nil {
		t.Fatalf("fingerprinter/panel not wired: %v %v", a.fingerprinter, a.panel)
	}
	return a
}

// panelScoreSession builds a finished session snapshot. i varies the feature
// vector so the baseline's covariance is not degenerate.
func panelScoreSession(id string, i int) *session.Session {
	start := time.Now().Add(-time.Duration(10+i) * time.Minute)
	end := start.Add(time.Duration(60+i) * time.Second)
	return &session.Session{
		ID: id, StartTime: start, LastActivity: end, EndTime: &end,
		RequestCount: 4 + i%3, BytesIn: int64(1000 + i), BytesOut: int64(2000 + 2*i),
		TokensIn: int64(300 + i), TokensOut: int64(100 + i%7),
		ToolCalls: 2 + i%2, ToolCallCounts: map[string]int{"read": 1 + i%2, "edit": 1},
		BackendsUsed: map[string]int{"primary": 4 + i%3},
		Messages: []session.Message{
			{Role: "user", Timestamp: start},
			{Role: "assistant", Timestamp: start.Add(time.Duration(2+i%5) * time.Second)},
			{Role: "user", Timestamp: start.Add(time.Duration(7+i%3) * time.Second)},
		},
	}
}

// Behavior preservation: with the shipped config (member C unseated) a scored
// session still produces exactly one "fingerprint score" record, and the
// warm-up sessions before it produce none — the same line count as before the
// panel grew an eval surface.
func TestScoreFingerprint_DefaultConfigEmitsExactlyOneScoreLine(t *testing.T) {
	quietLogs(t)
	a := panelScoreApp(t, nil)
	if len(a.panel.Members()) != 1 || a.panel.Members()[0].Name != "m3-lite" {
		t.Fatalf("the shipped config must seat m3-lite alone: %+v", a.panel.Members())
	}
	warmUp := a.cfg.Fingerprint.WarmUp
	if warmUp < 2 {
		t.Fatalf("the shipped warm_up must exceed 1 for this test: %d", warmUp)
	}
	recs := captureRecords(t)

	// Warm-up: opinions are still computed, nothing is scored and no line is
	// logged.
	for i := 0; i < warmUp-1; i++ {
		res := a.scoreFingerprint(panelScoreSession(fmt.Sprintf("sess-warm-%d", i), i))
		if res.Scored || res.Bucket != "" || res.Distance != 0 {
			t.Fatalf("warm-up %d scored: %+v", i, res)
		}
		if len(res.Opinions) != 1 || res.Opinions[0].Member != "m3-lite" || res.Opinions[0].Bucket != "" {
			t.Fatalf("warm-up %d opinions = %+v, want one bucket-less m3-lite opinion", i, res.Opinions)
		}
	}
	if n := countRecords(recs(), "fingerprint score"); n != 0 {
		t.Fatalf("warm-up logged %d fingerprint score records, want 0", n)
	}

	res := a.scoreFingerprint(panelScoreSession("sess-scored", warmUp))
	if !res.Scored || res.Bucket == "" || res.Class == "" {
		t.Fatalf("the warm session must be scored: %+v", res)
	}
	got := recs()
	if n := countRecords(got, "fingerprint score"); n != 1 {
		t.Fatalf("a scored session logged %d fingerprint score records, want exactly 1: %v", n, got)
	}
	for _, r := range got {
		if r.Message != "fingerprint score" {
			continue
		}
		for _, k := range []string{"session_id", "class", "distance", "bucket", "shadow", "panel_driver", "panel_risk", "members"} {
			if _, ok := r.Attrs[k]; !ok {
				t.Fatalf("the fingerprint score line lost attribute %q: %+v", k, r.Attrs)
			}
		}
		if ops, ok := r.Attrs["members"].([]storage.PanelOpinion); !ok || len(ops) != 1 || ops[0].Member != "m3-lite" {
			t.Fatalf("members attribute = %#v, want the typed opinion slice", r.Attrs["members"])
		}
	}

	// A panel-less app is a no-op that logs nothing, as before.
	before := len(recs())
	if res := (&app{cfg: a.cfg}).scoreFingerprint(panelScoreSession("sess-nil", 1)); !reflect.DeepEqual(res, fingerprintResult{}) {
		t.Fatalf("a nil panel must return the zero result: %+v", res)
	}
	if len(recs()) != before {
		t.Fatalf("a nil panel logged: %v", recs()[before:])
	}
}

// Step 7, at the unit level: a session whose content, tool arguments and even
// tool names carry the planted sentinels must leave no sentinel in the
// opinions, in the stored panel_opinions column or on the log line.
func TestScoreFingerprint_OpinionsAndLogAreContentFree(t *testing.T) {
	quietLogs(t)
	a := panelScoreApp(t, func(c *config.Config) {
		c.Panel.ToolChainArtifact = filepath.Join("..", "..", "internal", "panel", "testdata", "tool-chain.golden.json")
		c.Panel.ToolChainShadow = true
		c.Panel.ToolChainWeight = 1.0
		c.Fingerprint.WarmUp = 1
	})
	if len(a.panel.Members()) != 2 {
		t.Fatalf("both members must be seated: %+v", a.panel.Members())
	}
	recs := captureRecords(t)

	snap := panelScoreSession("sess-panel-content", 3)
	snap.SystemPrompt = planCSentinel
	snap.Messages[0].Content = planCSentinel
	at := snap.StartTime
	// The middle tool NAME is a sentinel too: an out-of-vocabulary tool must
	// not ride into the opinion or the log line either.
	for i, tool := range []string{"read", planCToolArgSentinel, "edit"} {
		snap.ToolCallHistory = append(snap.ToolCallHistory, session.ToolCallRecord{
			Timestamp: at.Add(time.Duration(i) * time.Second), ToolName: tool,
			Arguments: map[string]any{"path": planCToolArgSentinel}, Result: planCSentinel,
		})
	}

	res := a.scoreFingerprint(snap)
	if !res.Scored {
		t.Fatalf("the session must be scored: %+v", res)
	}
	if len(res.Opinions) != 2 || res.Opinions[1].Member != "tool-chain" || res.Opinions[1].ClassUsed != "global" || !res.Opinions[1].Shadow {
		t.Fatalf("the tool-chain opinion must have run over the planted trajectory: %+v", res.Opinions)
	}
	raw, merr := json.Marshal(res.Opinions)
	if merr != nil {
		t.Fatal(merr)
	}
	assertNoSentinel(t, "panel_opinions JSON", string(raw))
	assertNoSentinel(t, "panel opinions", fmt.Sprintf("%+v", res.Opinions))
	for _, r := range recs() {
		assertNoSentinel(t, "log record "+r.Message, r.String())
	}
	if n := countRecords(recs(), "fingerprint score"); n != 1 {
		t.Fatalf("fingerprint score records = %d, want 1", n)
	}

	// The stored column is content-free too.
	rec := storage.SessionRecord{ID: snap.ID, State: "ended", StartTime: snap.StartTime, EndTime: *snap.EndTime,
		Backend: "https://backend", ClientAddr: "127.0.0.1:1", PanelOpinions: res.Opinions}
	if serr := a.sqliteStore.SaveSession(rec); serr != nil {
		t.Fatalf("SaveSession: %v", serr)
	}
	var col sql.NullString
	if qerr := a.sqliteStore.DB().QueryRow(`SELECT panel_opinions FROM sessions WHERE id = ?`, snap.ID).Scan(&col); qerr != nil {
		t.Fatalf("read panel_opinions: %v", qerr)
	}
	if !col.Valid || col.String == "" {
		t.Fatalf("panel_opinions not stored: %+v", col)
	}
	assertNoSentinel(t, "the stored panel_opinions column", col.String)
	loaded, err := a.sqliteStore.GetSession(snap.ID)
	if err != nil || loaded == nil || len(loaded.PanelOpinions) != 2 {
		t.Fatalf("GetSession: %+v %v", loaded, err)
	}
}

// notableAnomaly is the weighted anomaly at which a member earns a finding:
// the risk-notable rung of the ladder, expressed on the 0..1 scale.
const notableAnomaly = float64(fingerprint.RiskNotable) / float64(panel.MaxRiskPoints)

// liveM3Lite is the opinion a live M3-lite member emits for bucket b.
func liveM3Lite(b string) panel.MemberOpinion {
	return panel.MemberOpinion{
		Member:  "m3-lite",
		Weight:  1.0,
		Anomaly: float64(fingerprint.BucketRiskPoints(b)) / float64(fingerprint.RiskSevere),
		Detail:  map[string]any{"bucket": b, "distance": 4.5, "shadow": false},
	}
}

func hasMember(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// The exact-equivalence proof: for a live M3-lite seat at weight 1.0 the
// per-member finding threshold selects precisely the buckets today's
// `bucket != normal && bucket != minor` condition emits for.
func TestAnomalyFindingMembers_M3LiteMatchesTodaysBucketCondition(t *testing.T) {
	for _, b := range []string{fingerprint.BucketNormal, fingerprint.BucketMinor, fingerprint.BucketNotable, fingerprint.BucketAnomalous, fingerprint.BucketSevere} {
		want := b != fingerprint.BucketNormal && b != fingerprint.BucketMinor
		got := anomalyFindingMembers(panel.Verdict{Members: []panel.MemberOpinion{liveM3Lite(b)}})
		if hasMember(got, "m3-lite") != want {
			t.Fatalf("bucket %s: members = %v, want m3-lite present = %v", b, got, want)
		}
		if len(got) > 1 {
			t.Fatalf("bucket %s: unexpected extra members: %v", b, got)
		}
	}
}

// Shadow seats are observed, never reported: a shadow member at the top of
// the scale earns no finding.
func TestAnomalyFindingMembers_ShadowMembersNeverAppear(t *testing.T) {
	v := panel.Verdict{Members: []panel.MemberOpinion{
		{Member: "m3-lite", Shadow: true, Weight: 1.0, Anomaly: 1.0, Detail: map[string]any{"bucket": fingerprint.BucketSevere, "shadow": true}},
		{Member: "tool-chain", Shadow: true, Weight: 1.0, Anomaly: 0.99, Detail: map[string]any{"percentile": 0.99}},
	}}
	if got := anomalyFindingMembers(v); len(got) != 0 {
		t.Fatalf("shadow members produced findings: %v", got)
	}
}

// A live member's seat weight gates its finding exactly as it gates its
// contribution to RiskScore.
func TestAnomalyFindingMembers_WeightGatesToolChain(t *testing.T) {
	for _, c := range []struct {
		weight float64
		want   bool
	}{
		{0.5, true},  // 0.5 * 0.99 = 0.495 >= 0.25
		{0.2, false}, // 0.2 * 0.99 = 0.198 <  0.25
	} {
		v := panel.Verdict{Members: []panel.MemberOpinion{
			{Member: "tool-chain", Weight: c.weight, Anomaly: 0.99, Detail: map[string]any{"percentile": 0.99}},
		}}
		got := anomalyFindingMembers(v)
		if hasMember(got, "tool-chain") != c.want {
			t.Fatalf("weight %.1f: members = %v, want tool-chain present = %v", c.weight, got, c.want)
		}
	}
	// Exactly at the rung, a member is reported.
	v := panel.Verdict{Members: []panel.MemberOpinion{{Member: "tool-chain", Weight: 1.0, Anomaly: notableAnomaly}}}
	if got := anomalyFindingMembers(v); !hasMember(got, "tool-chain") {
		t.Fatalf("a member exactly at the notable rung must be reported: %v", got)
	}
}

// R4: each live member that clears the rung earns its own finding, in seat
// order — the panel reports every member, not just the driver.
func TestAnomalyFindingMembers_LiveToolChainAndNotableM3Lite(t *testing.T) {
	v := panel.Verdict{Members: []panel.MemberOpinion{
		liveM3Lite(fingerprint.BucketNotable),
		{Member: "tool-chain", Weight: 1.0, Anomaly: 0.99, Detail: map[string]any{"percentile": 0.99}},
	}}
	got := anomalyFindingMembers(v)
	if len(got) != 2 || got[0] != "m3-lite" || got[1] != "tool-chain" {
		t.Fatalf("members = %v, want [m3-lite tool-chain] in seat order", got)
	}
	if n := anomalyFindingMembers(panel.Verdict{}); len(n) != 0 {
		t.Fatalf("an empty verdict must produce no findings: %v", n)
	}
}

func TestOpinionFor_FoundAndMissing(t *testing.T) {
	v := panel.Verdict{Members: []panel.MemberOpinion{liveM3Lite(fingerprint.BucketNotable)}}
	if op := opinionFor(v, "m3-lite"); op.Member != "m3-lite" || op.Detail["bucket"] != fingerprint.BucketNotable {
		t.Fatalf("opinionFor(m3-lite) = %+v", op)
	}
	if op := opinionFor(v, "tool-chain"); op.Member != "" || op.Detail != nil {
		t.Fatalf("a member that did not sit must yield the zero opinion: %+v", op)
	}
}

// stubMember is a seatable member with a fixed opinion, so the OCSF path can
// be driven without depending on a real scorer's numbers.
type stubMember struct {
	name    string
	anomaly float64
	detail  map[string]any
}

func (m stubMember) Name() string    { return m.name }
func (m stubMember) Version() string { return "stub" }
func (m stubMember) Assess(panel.SessionFeatures) panel.MemberOpinion {
	return panel.MemberOpinion{Member: m.name, Anomaly: m.anomaly, Detail: m.detail}
}

// A live member above the rung gets its own OCSF finding, with its own
// analytic and numbers; a live member the OCSF path does not know about gets
// none; and the M3-lite finding is still emitted exactly when today's bucket
// condition holds.
func TestScoreFingerprint_EmitsPerMemberOCSFFindings(t *testing.T) {
	quietLogs(t)
	a := panelScoreApp(t, func(c *config.Config) {
		c.Fingerprint.Shadow = false
		c.Fingerprint.WarmUp = 1
	})
	a.panel.Seat(stubMember{name: "tool-chain", anomaly: 0.99,
		detail: map[string]any{"perplexity": 12.5, "percentile": 0.99, "class_used": "global"}}, false, 1.0)
	// A future live member with no OCSF mapping must emit nothing.
	a.panel.Seat(stubMember{name: "shape", anomaly: 1.0, detail: map[string]any{"reason": "stub"}}, false, 1.0)
	nozzle := &recordingNozzle{}
	a.ocsfEmitter = telemetry.NewOCSFEmitterForTest([]telemetry.OCSFNozzle{nozzle})

	snap := panelScoreSession("sess-ocsf-members", 5)
	snap.Backend = "https://backend"
	snap.SystemPrompt = planCSentinel
	snap.Messages[0].Content = planCSentinel
	at := snap.StartTime
	for i, tool := range []string{"read", planCToolArgSentinel, "edit"} {
		snap.ToolCallHistory = append(snap.ToolCallHistory, session.ToolCallRecord{
			Timestamp: at.Add(time.Duration(i) * time.Second), ToolName: tool,
			Arguments: map[string]any{"path": planCToolArgSentinel}, Result: planCSentinel,
		})
	}

	res := a.scoreFingerprint(snap)
	if !res.Scored {
		t.Fatalf("the session must be scored: %+v", res)
	}

	if n := nozzle.count(`"tool-chain Markov"`); n != 1 {
		t.Fatalf("tool-chain findings = %d, want 1", n)
	}
	if n := nozzle.count(`"elida.tool_chain"`); n != 1 {
		t.Fatalf("findings carrying elida.tool_chain = %d, want 1", n)
	}
	if n := nozzle.count(`"percentile":0.99`); n != 1 {
		t.Fatalf("the tool-chain finding lost its percentile: %v", nozzle.count(`"percentile":0.99`))
	}
	if n := nozzle.count(`"severity_id":5`); n != 1 {
		t.Fatalf("findings at critical severity = %d, want 1 (percentile 0.99)", n)
	}
	if n := nozzle.count(`"elida.backend":"https://backend"`); n != 1 {
		t.Fatalf("findings carrying the backend = %d, want 1", n)
	}

	// The M3-lite finding keeps today's condition exactly.
	wantM3 := 0
	if res.Bucket != fingerprint.BucketNormal && res.Bucket != fingerprint.BucketMinor {
		wantM3 = 1
	}
	if n := nozzle.count(`"M3-lite"`); n != wantM3 {
		t.Fatalf("M3-lite findings = %d, want %d for bucket %q", n, wantM3, res.Bucket)
	}
	// Exactly the mapped members emitted: nothing for "shape".
	if n := nozzle.count(`"behavioral_anomaly"`); n != 1+wantM3 {
		t.Fatalf("behavioral_anomaly findings = %d, want %d", n, 1+wantM3)
	}
	if n := nozzle.count(planCSentinel) + nozzle.count(planCToolArgSentinel); n != 0 {
		t.Fatalf("a sentinel reached %d OCSF findings", n)
	}
}

// R5: a shadow M3-lite score is persisted but never reported — no risk
// points, no findings, whatever the bucket.
func TestScoreFingerprint_ShadowEmitsNoFindings(t *testing.T) {
	quietLogs(t)
	a := panelScoreApp(t, func(c *config.Config) {
		c.Fingerprint.Shadow = true
		c.Fingerprint.WarmUp = 1
	})
	a.panel.Seat(stubMember{name: "tool-chain", anomaly: 0.99,
		detail: map[string]any{"perplexity": 12.5, "percentile": 0.99, "class_used": "global"}}, false, 1.0)
	nozzle := &recordingNozzle{}
	a.ocsfEmitter = telemetry.NewOCSFEmitterForTest([]telemetry.OCSFNozzle{nozzle})

	res := a.scoreFingerprint(panelScoreSession("sess-ocsf-shadow", 6))
	if !res.Scored {
		t.Fatalf("a shadow score is still persisted: %+v", res)
	}
	if n := nozzle.count(`"class_uid":2004`); n != 0 {
		t.Fatalf("a shadow session emitted %d findings, want 0", n)
	}
}
