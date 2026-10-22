package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/control"
	"elida/internal/correlation"
	"elida/internal/decision/embedded"
	"elida/internal/session"
	"elida/internal/storage"
	"elida/internal/telemetry"
)

// The three planted sentinels. Each names one kind of content that must
// never reach a Plan C surface:
//   - planCSentinel: message content (the user's text and every tool result);
//   - planCToolArgSentinel: tool-call arguments (every tool_use input);
//   - planCPatternSentinel: the SOURCE of an operator-configured argument
//     pattern (correlation.tool_categories[].argument_patterns). The tool
//     arguments carry planCPatternMatch, which the pattern matches but which
//     is a different string, so a hit on planCPatternSentinel can only be the
//     pattern itself leaking.
const (
	planCSentinel        = "sentinelC9f3a"
	planCToolArgSentinel = "toolargC7e21"
	planCPatternSentinel = "patternC4b[5]d"
	planCPatternMatch    = "patternC4b5d"
)

// planCSentinels lists every planted sentinel, for assertNoSentinel.
var planCSentinels = []string{planCSentinel, planCToolArgSentinel, planCPatternSentinel}

// planCBodies is slowBurnBodies with the content sentinel planted in the
// user's text and every tool result, and the tool-argument sentinel (plus a
// string the operator pattern matches) in every tool_use input, so content
// has a chance to leak into every surface under test.
func planCBodies() []string {
	fragments := []string{
		slowBurnMarker + " one " + planCSentinel + ": the regional weather report lists sunny days.",
		slowBurnMarker + " two " + planCSentinel + ": the regional weather report lists cloudy days.",
		slowBurnMarker + " three " + planCSentinel + ": the regional weather report lists rainy days.",
	}
	msgs := []map[string]any{{"role": "user", "content": "read the three weather notes " + planCSentinel}}
	bodies := make([]string, 0, len(fragments))
	for i, f := range fragments {
		id := fmt.Sprintf("toolu_%d", i)
		input := map[string]any{"path": fmt.Sprintf("/notes/%s/%s/%d.txt", planCToolArgSentinel, planCPatternMatch, i)}
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": id, "name": "read_note", "input": input}}},
			map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": id, "content": f}}},
		)
		b, _ := json.Marshal(map[string]any{"model": "claude-test", "max_tokens": 16, "messages": msgs})
		bodies = append(bodies, string(b))
	}
	return bodies
}

type planCRun struct {
	a      *app
	id     string
	nozzle *recordingNozzle
	logs   *lockedBuffer
	otel   func(t *testing.T, marker string) string
}

// planCOTELChildEnv marks a re-executed test binary whose OTEL provider
// is the real stdout exporter (traces, logs, metrics). The OTEL stdout
// exporters bind the process stdout when their package initializes, so the
// only portable way to read what they export is to run the scenario in a
// child process and capture its stdout: planCOTELExport does that.
const planCOTELChildEnv = "ELIDA_PLANC_OTEL_CHILD"

func planCOTELChild() bool { return os.Getenv(planCOTELChildEnv) == "1" }

// planCTelemetry is the provider the scenario runs with: the stdout
// exporter in the child, the no-op provider in the parent. The returned
// function, in the child, flushes the exporters, prints marker (an ID the
// parent looks for in the export) and ends the child test; in the parent
// it re-runs the current test as a child and returns everything the child
// wrote to stdout.
func planCTelemetry(t *testing.T) (*telemetry.Provider, func(t *testing.T, marker string) string) {
	t.Helper()
	if !planCOTELChild() {
		return telemetry.NoopProvider(), func(t *testing.T, _ string) string {
			t.Helper()
			return planCOTELExport(t)
		}
	}
	tp, err := telemetry.NewProvider(telemetry.Config{Enabled: true, Exporter: "stdout", ServiceName: "elida-plan-c"})
	if err != nil {
		t.Fatalf("stdout telemetry: %v", err)
	}
	return tp, func(t *testing.T, marker string) string {
		t.Helper()
		if err := tp.Shutdown(context.Background()); err != nil {
			t.Fatalf("telemetry shutdown: %v", err)
		}
		fmt.Printf("\nplanc-marker=%s\n", marker)
		t.SkipNow()
		return ""
	}
}

// planCOTELExport re-runs the current test (and subtest) in a child test
// binary with planCOTELChildEnv set and returns the child's stdout: the
// whole OTEL export of the child's run. The child must end cleanly.
func planCOTELExport(t *testing.T) string {
	t.Helper()
	parts := strings.Split(t.Name(), "/")
	for i, p := range parts {
		parts[i] = "^" + regexp.QuoteMeta(p) + "$"
	}
	// #nosec G204 -- re-executes this test binary with a fixed test filter
	cmd := exec.Command(os.Args[0], "-test.run="+strings.Join(parts, "/"), "-test.count=1")
	cmd.Env = append(os.Environ(), planCOTELChildEnv+"=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("OTEL child run failed: %v\nstderr: %s\nstdout tail: %s", err, stderr.String(), planCTail(stdout.String()))
	}
	return stdout.String()
}

func planCTail(s string) string {
	if len(s) > 4000 {
		return s[len(s)-4000:]
	}
	return s
}

// planCMarker returns the marker the child printed before it ended.
func planCMarker(t *testing.T, out string) string {
	t.Helper()
	i := strings.LastIndex(out, "planc-marker=")
	if i < 0 {
		t.Fatalf("the OTEL child printed no marker: %s", planCTail(out))
	}
	return strings.TrimSpace(strings.SplitN(out[i+len("planc-marker="):], "\n", 2)[0])
}

// planCNozzleEvents copies the recorded OCSF events under the nozzle's lock.
func planCNozzleEvents(n *recordingNozzle) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.events...)
}

// planCApp is the production wiring from the shipped config with
// correlation on, semantic detection in mode, SQLite storage, the end
// callback, the control API, a real (stdout) OTEL provider and an OCSF
// emitter recording every finding. The operator taxonomy adds one argument
// pattern carrying planCPatternSentinel; it categorizes the read_note calls
// and no rule counts its category, so the match under test is unchanged.
func planCApp(t *testing.T, mode string) planCRun {
	t.Helper()
	logs := captureLogs(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","content":[{"type":"text","text":"noted"}]}`))
	}))
	t.Cleanup(backend.Close)
	cfg, err := config.Load(filepath.Join("..", "..", "configs", "elida.yaml"))
	if err != nil {
		t.Fatalf("load the shipped config: %v", err)
	}
	cfg.Correlation.Enabled = true
	cfg.Correlation.ToolCategories = append(cfg.Correlation.ToolCategories, correlation.ToolCategoryRule{
		Category: "planc_note_access", Tools: []string{"read_note"}, ArgumentPatterns: []string{planCPatternSentinel},
	})
	for name, b := range cfg.Backends {
		b.URL = backend.URL
		cfg.Backends[name] = b
	}
	cfg.Decision.Enabled = true
	cfg.Decision.Mode = mode
	cfg.Decision.ModelPath = goodModelPath
	cfg.Decision.InlineTimeout = 5 * time.Second
	cfg.Storage.Enabled = true
	cfg.Storage.Path = filepath.Join(t.TempDir(), "elida.db")

	a := &app{cfg: cfg, configPath: filepath.Join(t.TempDir(), "elida.yaml")}
	a.decisionPipeline = func(context.Context, string, *embedded.Manifest) (embedded.Pipeline, error) {
		return slowBurnPipeline{}, nil
	}
	pinInline(a)
	a.initSessionStore()
	a.initSQLiteStorage()
	t.Cleanup(func() { _ = a.sqliteStore.Close() })
	if err := a.setupPolicyEngine(); err != nil {
		t.Fatalf("setupPolicyEngine: %v", err)
	}
	if err := a.setupDecision(context.Background()); err != nil {
		t.Fatalf("setupDecision: %v", err)
	}
	t.Cleanup(func() { a.shutdownDecision(context.Background()) })
	nozzle := &recordingNozzle{}
	a.ocsfEmitter = telemetry.NewOCSFEmitterForTest([]telemetry.OCSFNozzle{nozzle})
	tp, otelOut := planCTelemetry(t)
	a.tp = tp
	a.tp.SetOCSFEmitter(a.ocsfEmitter)
	a.initSessionEndCallback()
	a.initProxy()
	a.initControlAPI()

	id := "sess-plan-c-" + mode
	for _, body := range planCBodies() {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Session-ID", id)
		w := httptest.NewRecorder()
		a.proxyHandler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
	}
	// The tool lane categorizes off the request path; flush it so the
	// operator pattern has run before anything is asserted.
	if err := a.policyEngine.FlushToolCalls(context.Background()); err != nil {
		t.Fatalf("FlushToolCalls: %v", err)
	}
	return planCRun{a: a, id: id, nozzle: nozzle, logs: logs, otel: otelOut}
}

func (r planCRun) get(t *testing.T, path string) (int, []byte) {
	t.Helper()
	w := httptest.NewRecorder()
	r.a.controlHandler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w.Code, w.Body.Bytes()
}

// jsonObjectKeys returns the keys of the JSON object at raw.
func jsonObjectKeys(t *testing.T, raw json.RawMessage) map[string]bool {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not an object: %v: %s", err, raw)
	}
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

// structJSONNames returns the json tag names of typ's fields.
func structJSONNames(typ reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" {
			out[tag] = true
		}
	}
	return out
}

// assertKeysAgree checks that every key Go emitted is in the fixture and
// every fixture key is a field of the Go struct: the dashboard fixture and
// the record cannot drift apart silently.
func assertKeysAgree(t *testing.T, what string, got map[string]bool, fixture map[string]bool, typ reflect.Type) {
	t.Helper()
	names := structJSONNames(typ)
	for k := range got {
		if !fixture[k] {
			t.Errorf("%s: Go emits %q but the dashboard fixture lacks it", what, k)
		}
	}
	for k := range fixture {
		if !names[k] {
			t.Errorf("%s: fixture key %q is not a field of %s", what, k, typ.Name())
		}
	}
}

func loadFixture(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "components", "__fixtures__", "history-correlation.json"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return m
}

func firstObject(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
		t.Fatalf("not a non-empty array: %v: %s", err, raw)
	}
	return arr[0]
}

// assertNoSentinel fails when s carries any planted sentinel, naming it.
func assertNoSentinel(t *testing.T, what string, s string) {
	t.Helper()
	for _, sentinel := range planCSentinels {
		if strings.Contains(s, sentinel) {
			t.Fatalf("%s carries the sentinel %q: %s", what, sentinel, s)
		}
	}
}

// planCTurnsWithoutToolArguments returns the turns response with the
// arguments of tool_call turns removed, and nothing else. Those arguments
// are the pre-existing timeline display of the session's tool calls
// (session.ToolCallHistory, before Plan C) and carry the tool-argument
// sentinel by design; every other field of every turn, including each
// violation turn's identity and correlation summary, must be content-free.
// The field is removed by name and turn type, never by substring.
func planCTurnsWithoutToolArguments(t *testing.T, raw []byte) []byte {
	t.Helper()
	var resp map[string]json.RawMessage
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("turns: %v", err)
	}
	var turns []map[string]json.RawMessage
	if err := json.Unmarshal(resp["turns"], &turns); err != nil {
		t.Fatalf("turns: %v", err)
	}
	for _, turn := range turns {
		if string(turn["type"]) == `"tool_call"` {
			delete(turn, "arguments")
		}
	}
	b, err := json.Marshal(turns)
	if err != nil {
		t.Fatal(err)
	}
	resp["turns"] = b
	out, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// assertEvidenceSurfacesClean checks the control APIs and SQLite event
// rows that every run exposes, whatever the mode.
func assertEvidenceSurfacesClean(t *testing.T, r planCRun) {
	t.Helper()
	for _, path := range []string{"/control/decision", "/control/health", "/control/events/" + r.id} {
		code, raw := r.get(t, path)
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, code, raw)
		}
		assertNoSentinel(t, path, string(raw))
	}
	evs, err := r.a.sqliteStore.GetSessionEvents(r.id)
	if err != nil || len(evs) == 0 {
		t.Fatalf("GetSessionEvents: %d %v", len(evs), err)
	}
	for _, ev := range evs {
		assertNoSentinel(t, "SQLite event "+string(ev.Type), string(ev.Data))
	}
}

func TestPlanC_AuditMatchReachesEverySurfaceWithoutContent(t *testing.T) {
	r := planCApp(t, config.DecisionModeAudit)

	// Live: turns name the match and link it; flagged detail carries the
	// evidence and the decisions it names.
	code, turnsRaw := r.get(t, "/control/sessions/"+r.id+"/turns?full=true")
	if code != http.StatusOK {
		t.Fatalf("turns: %d", code)
	}
	var turns struct {
		Turns []control.TurnEntry `json:"turns"`
	}
	_ = json.Unmarshal(turnsRaw, &turns)
	var matchTurn *control.TurnEntry
	for i := range turns.Turns {
		if turns.Turns[i].Correlation != nil {
			matchTurn = &turns.Turns[i]
		}
	}
	if matchTurn == nil || matchTurn.Correlation.EventCount != 3 || matchTurn.EventID != matchTurn.Correlation.MatchID || !matchTurn.EvidenceOnly {
		t.Fatalf("no linked match turn: %s", turnsRaw)
	}
	code, flaggedRaw := r.get(t, "/control/flagged/"+r.id)
	if code != http.StatusOK {
		t.Fatalf("flagged: %d", code)
	}
	var flagged struct {
		Violations []struct {
			EventID     string `json:"event_id"`
			Correlation *struct {
				MatchID  string   `json:"match_id"`
				EventIDs []string `json:"event_ids"`
			} `json:"correlation"`
		} `json:"violations"`
		Decisions []struct {
			EventID string `json:"event_id"`
		} `json:"semantic_decisions"`
	}
	_ = json.Unmarshal(flaggedRaw, &flagged)
	known := map[string]bool{}
	for _, d := range flagged.Decisions {
		known[d.EventID] = true
	}
	var linked int
	for _, v := range flagged.Violations {
		if v.Correlation == nil {
			continue
		}
		if v.Correlation.MatchID != matchTurn.EventID {
			t.Fatalf("turn and flagged disagree on the match: %q vs %q", v.Correlation.MatchID, matchTurn.EventID)
		}
		for _, id := range v.Correlation.EventIDs {
			if known[id] {
				linked++
			}
		}
	}
	if linked != 3 {
		t.Fatalf("the match's three contributing IDs must be semantic_decisions of this session, linked %d: %s", linked, flaggedRaw)
	}
	assertNoSentinel(t, "turns", string(planCTurnsWithoutToolArguments(t, turnsRaw)))
	assertNoSentinel(t, "flagged detail", string(flaggedRaw))

	// Session end: SQLite, violation_detected, OCSF, telemetry record.
	r.a.manager.DrainActiveSessions()
	rec, err := r.a.sqliteStore.GetSession(r.id)
	if err != nil || rec == nil {
		t.Fatalf("GetSession: %v %v", rec, err)
	}
	var stored *storage.Violation
	for i := range rec.Violations {
		if rec.Violations[i].Correlation != nil {
			stored = &rec.Violations[i]
		}
	}
	if stored == nil || stored.Correlation.MatchID != matchTurn.EventID || len(stored.Correlation.EventIDs) != 3 || len(rec.SemanticDecisions) < 3 {
		t.Fatalf("stored record: %+v", rec)
	}
	if score, _, _ := r.a.policyEngine.GetSessionRiskScore(r.id); score != 0 {
		t.Fatalf("audit match contributed risk: %v", score)
	}
	recJSON, _ := json.Marshal(rec)
	assertNoSentinel(t, "SQLite record", string(recJSON))

	evs, _ := r.a.sqliteStore.GetSessionEvents(r.id)
	var matchRows int
	for _, ev := range evs {
		if ev.Type != storage.EventViolationDetected {
			continue
		}
		assertNoSentinel(t, "violation_detected", string(ev.Data))
		var d storage.ViolationDetectedData
		_ = json.Unmarshal(ev.Data, &d)
		if d.EventCategory == "correlation_match" {
			matchRows++
			if d.EventID != matchTurn.EventID || d.SourceRole != "correlation" {
				t.Fatalf("violation_detected identity: %+v", d)
			}
		}
	}
	if matchRows != 1 {
		t.Fatalf("violation_detected rows for the match = %d, want 1", matchRows)
	}
	assertEvidenceSurfacesClean(t, r)

	if n := r.nozzle.count(`"elida.correlation"`); n != 1 {
		t.Fatalf("OCSF findings with elida.correlation = %d, want 1", n)
	}
	if n := r.nozzle.count(`"match_id":"` + matchTurn.EventID + `"`); n != 1 {
		t.Fatalf("OCSF finding must carry the match ID, got %d", n)
	}
	for _, e := range planCNozzleEvents(r.nozzle) {
		assertNoSentinel(t, "OCSF event", e)
	}
	sess, _ := r.a.manager.Get(r.id)
	snap := sess.Snapshot()
	tr := telemetryRecordFrom(rec, &snap, time.Now(), nil)
	var exported int
	for _, v := range tr.Violations {
		if v.Correlation != nil && v.Correlation.EventCount == 3 && v.Correlation.MatchID == matchTurn.EventID {
			exported++
		}
	}
	if exported != 1 {
		t.Fatalf("telemetry record: %+v", tr.Violations)
	}
	trJSON, _ := json.Marshal(tr)
	assertNoSentinel(t, "telemetry record", string(trJSON))

	// The real OTEL export of the same scenario (request spans, the
	// session span and its events, logs and metrics from the stdout
	// exporter, run in a child process): the match is there by its ID,
	// the sentinels are not.
	otelOut := r.otel(t, matchTurn.EventID)
	childMatch := planCMarker(t, otelOut)
	export := otelOut[:strings.LastIndex(otelOut, "planc-marker=")]
	if !strings.Contains(export, `"`+childMatch+`"`) || !strings.Contains(export, "elida.correlation.match_id") {
		t.Fatalf("the OTEL export must carry the match %q: %s", childMatch, planCTail(export))
	}
	assertNoSentinel(t, "OTEL export", otelOut)

	// History API and the dashboard fixture agree on the key sets.
	code, histRaw := r.get(t, "/control/history/"+r.id)
	if code != http.StatusOK {
		t.Fatalf("history: %d", code)
	}
	assertNoSentinel(t, "history detail", string(histRaw))
	var hist map[string]json.RawMessage
	_ = json.Unmarshal(histRaw, &hist)
	fixture := loadFixture(t)
	var histViolations []json.RawMessage
	_ = json.Unmarshal(hist["violations"], &histViolations)
	var gotCorr json.RawMessage
	for _, v := range histViolations {
		var probe struct {
			Correlation json.RawMessage `json:"correlation"`
		}
		_ = json.Unmarshal(v, &probe)
		if probe.Correlation != nil {
			gotCorr = probe.Correlation
		}
	}
	var fixViolations []json.RawMessage
	_ = json.Unmarshal(fixture["violations"], &fixViolations)
	var fixCorr json.RawMessage
	for _, v := range fixViolations {
		var probe struct {
			Correlation json.RawMessage `json:"correlation"`
		}
		_ = json.Unmarshal(v, &probe)
		if probe.Correlation != nil {
			fixCorr = probe.Correlation
		}
	}
	assertKeysAgree(t, "violations[].correlation", jsonObjectKeys(t, gotCorr), jsonObjectKeys(t, fixCorr), reflect.TypeOf(storage.CorrelationEvidence{}))
	assertKeysAgree(t, "semantic_decisions[0]", jsonObjectKeys(t, firstObject(t, hist["semantic_decisions"])), jsonObjectKeys(t, firstObject(t, fixture["semantic_decisions"])), reflect.TypeOf(storage.SemanticDecision{}))

	code, listRaw := r.get(t, "/control/history")
	if code != http.StatusOK || !strings.Contains(string(listRaw), matchTurn.EventID) {
		t.Fatalf("the history list carries violations[].correlation (D9): %d %s", code, listRaw)
	}
	assertNoSentinel(t, "history list", string(listRaw))
	assertNoSentinel(t, "logs", r.logs.String())
}

func TestPlanC_ShadowMatchReachesHistoryAndLogsWithoutContent(t *testing.T) {
	r := planCApp(t, config.DecisionModeShadow)
	code, liveRaw := r.get(t, "/control/sessions/"+r.id)
	if code != http.StatusOK || !strings.Contains(string(liveRaw), `"correlation_shadow"`) {
		t.Fatalf("live detail must carry correlation_shadow before the session ends: %d %s", code, liveRaw)
	}
	r.a.manager.DrainActiveSessions()
	rec, err := r.a.sqliteStore.GetSession(r.id)
	if err != nil || rec == nil || len(rec.CorrelationShadow) != 1 || len(rec.CorrelationShadow[0].EventIDs) != 3 || len(rec.Violations) != 0 {
		t.Fatalf("stored shadow record: %+v %v", rec, err)
	}
	recJSON, _ := json.Marshal(rec)
	assertNoSentinel(t, "SQLite record", string(recJSON))
	assertEvidenceSurfacesClean(t, r)
	if evs := planCNozzleEvents(r.nozzle); len(evs) != 0 {
		t.Fatalf("a shadow match must produce no OCSF finding: %v", evs)
	}
	sess, _ := r.a.manager.Get(r.id)
	snap := sess.Snapshot()
	tr := telemetryRecordFrom(rec, &snap, time.Now(), nil)
	if len(tr.CorrelationShadow) != 1 || tr.CorrelationShadow[0].Metadata.EventCount != 3 || tr.CorrelationShadow[0].Metadata.MatchID != rec.CorrelationShadow[0].MatchID {
		t.Fatalf("telemetry record shadow list: %+v", tr.CorrelationShadow)
	}
	trJSON, _ := json.Marshal(tr)
	assertNoSentinel(t, "telemetry record", string(trJSON))

	// The real OTEL export: one shadow log naming the match, no sentinel.
	otelOut := r.otel(t, rec.CorrelationShadow[0].MatchID)
	childMatch := planCMarker(t, otelOut)
	export := otelOut[:strings.LastIndex(otelOut, "planc-marker=")]
	if n := strings.Count(export, "(calibration only, never a violation)"); n != 1 {
		t.Fatalf("OTEL shadow logs = %d, want 1: %s", n, planCTail(export))
	}
	if !strings.Contains(export, `"`+childMatch+`"`) {
		t.Fatalf("the OTEL shadow log must carry the match ID %q: %s", childMatch, planCTail(export))
	}
	assertNoSentinel(t, "OTEL export", otelOut)

	code, histRaw := r.get(t, "/control/history/"+r.id)
	if code != http.StatusOK {
		t.Fatalf("history: %d", code)
	}
	assertNoSentinel(t, "history detail", string(histRaw))
	var hist map[string]json.RawMessage
	_ = json.Unmarshal(histRaw, &hist)
	fixture := loadFixture(t)
	assertKeysAgree(t, "correlation_shadow[0]", jsonObjectKeys(t, firstObject(t, hist["correlation_shadow"])), jsonObjectKeys(t, firstObject(t, fixture["correlation_shadow"])), reflect.TypeOf(storage.CorrelationShadow{}))
	assertKeysAgree(t, "semantic_shadow[0]", jsonObjectKeys(t, firstObject(t, hist["semantic_shadow"])), jsonObjectKeys(t, firstObject(t, fixture["semantic_shadow"])), reflect.TypeOf(storage.SemanticShadow{}))
	if score, _, _ := r.a.policyEngine.GetSessionRiskScore(r.id); score != 0 {
		t.Fatalf("shadow moved risk: %v", score)
	}
	logs := r.logs.String()
	if !strings.Contains(logs, "shadow correlation match recorded") {
		t.Fatal("the content-free shadow match log line is expected")
	}
	assertNoSentinel(t, "logs", logs)
}

// TestPlanC_ShadowLogsBoundedAtTheSessionCap (final-review gate "bounds at
// ceilings"): a session holding 50 shadow correlation matches exports 50
// shadow logs, and one that saw 51 exports 50, through the production
// session-end path (session snapshot -> stored record -> telemetryRecordFrom
// -> ExportSessionRecord) into a real OTEL log exporter (in a child process,
// see planCTelemetry).
func TestPlanC_ShadowLogsBoundedAtTheSessionCap(t *testing.T) {
	for _, n := range []int{session.MaxCorrelationShadow, session.MaxCorrelationShadow + 1} {
		t.Run(fmt.Sprintf("recorded=%d", n), func(t *testing.T) {
			quietLogs(t)
			cfg := decisionTestConfig(t)
			cfg.Decision.Enabled = false
			cfg.Storage.Enabled = true
			cfg.Storage.Path = filepath.Join(t.TempDir(), "elida.db")
			a := newDecisionApp(t, cfg, nil)
			a.initSessionStore()
			a.initSQLiteStorage()
			t.Cleanup(func() { _ = a.sqliteStore.Close() })
			tp, otelOut := planCTelemetry(t)
			a.tp = tp
			a.initSessionEndCallback()

			id := fmt.Sprintf("sess-plan-c-cap-%d", n)
			sess := a.manager.GetOrCreate(id, "default", "127.0.0.1:1")
			base := time.Now().Add(-time.Minute)
			for i := 0; i < n; i++ {
				ts := base.Add(time.Duration(i) * time.Millisecond)
				sess.RecordCorrelationShadow(session.CorrelationShadow{
					Timestamp: ts, MatchID: fmt.Sprintf("cm-cap-%03d", i), Rule: "slow_burn_injection",
					Shape: "repeated", Severity: "warning", Mode: "audit",
					EventIDs:   []string{fmt.Sprintf("sd-%03d-a", i), fmt.Sprintf("sd-%03d-b", i), fmt.Sprintf("sd-%03d-c", i)},
					Categories: []string{"injection_elevated"}, FirstEvent: ts, LastEvent: ts,
				})
			}
			a.manager.DrainActiveSessions()

			want := min(n, session.MaxCorrelationShadow)
			rec, err := a.sqliteStore.GetSession(id)
			if err != nil || rec == nil || len(rec.CorrelationShadow) != want {
				t.Fatalf("stored correlation_shadow: %v %v", rec, err)
			}
			out := otelOut(t, fmt.Sprint(len(rec.CorrelationShadow)))
			if planCMarker(t, out) != fmt.Sprint(want) {
				t.Fatalf("the child stored %s shadow matches, want %d", planCMarker(t, out), want)
			}
			if got := strings.Count(out, "(calibration only, never a violation)"); got != want {
				t.Fatalf("OTEL shadow logs = %d, want %d", got, want)
			}
			if n > session.MaxCorrelationShadow && strings.Contains(out, "cm-cap-000") {
				t.Fatal("the oldest match beyond the cap must not be exported")
			}
		})
	}
}
