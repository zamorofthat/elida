package session

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// wireExcluded names the exported Session fields that deliberately never
// reach Redis, each with the reason. Adding a durable field to Session
// without adding it to sessionData fails TestSessionWire_RoundTripsEveryDurableField.
var wireExcluded = map[string]string{
	"Messages":         "conversation content",
	"SystemPrompt":     "conversation content",
	"RequestTimes":     "rate-limit window; already non-functional under Redis (follow-up)",
	"SystemPromptHash": "session-local cache, json:\"-\"",
}

// wireTime carries a nanosecond component on purpose: StartTime identity
// (Persist's reused-ID guard, runner.sameInstance, runner.shadowKey) needs
// sub-second fidelity through the blob, and whole-second fixtures could not
// tell a truncating wire format from a correct one.
func wireTime(sec int) time.Time {
	return time.Date(2026, 11, 7, 10, 0, sec, 123456789, time.UTC)
}

// fullWireSession populates every exported Session field, including the
// excluded ones, so the symmetry test can tell "not populated" from "lost".
func fullWireSession() *Session {
	end := wireTime(59)
	s := &Session{
		ID:               "sess-wire",
		State:            Killed,
		StartTime:        wireTime(1),
		LastActivity:     wireTime(2),
		EndTime:          &end,
		RequestCount:     7,
		BytesIn:          100,
		BytesOut:         200,
		Backend:          "http://b1",
		ClientAddr:       "10.0.0.1:1234",
		Metadata:         map[string]string{"k": "v"},
		BackendsUsed:     map[string]int{"b1": 3, "b2": 4},
		Terminated:       true,
		SystemPromptHash: "hash",
		IsWebSocket:      true,
		FrameCount:       11,
		TextFrames:       6,
		BinaryFrames:     5,
		RequestTimes:     []time.Time{wireTime(3)},
		killChan:         make(chan struct{}),
		TokensIn:         31,
		TokensOut:        32,
		ToolCalls:        2,
		ToolCallCounts:   map[string]int{"bash": 2},
		Messages:         []Message{{Role: "user", Content: "hi", Timestamp: wireTime(4)}},
		SystemPrompt:     "be nice",
		FailedBackends:   []string{"b9"},
	}
	for i := 0; i < maxToolCallHistory; i++ {
		s.ToolCallHistory = append(s.ToolCallHistory, ToolCallRecord{
			Timestamp: wireTime(i % 60), ToolName: "bash", ToolType: "function", RequestID: "req",
		})
	}
	for i := 0; i < MaxSemanticShadow; i++ {
		s.SemanticShadow = append(s.SemanticShadow, SemanticShadow{
			Timestamp: wireTime(i % 60), DecisionID: "dec", Signal: "injection", Probability: 0.4,
			AuxProbability: 0.1, Vetoed: true, SourceRole: "tool", MessageIndex: i, Transform: "base64",
			TransformDepth: 1, WindowStartByte: 1, WindowEndByte: 9, Model: "m", ModelVersion: "v",
			ModelChecksum: "c", ThresholdSet: "t", ExecutionMode: "inline", ProtectionScope: "current_request",
			CoverageComplete: true, LatencyMs: 12,
		})
	}
	for i := 0; i < MaxCorrelationShadow; i++ {
		s.CorrelationShadow = append(s.CorrelationShadow, CorrelationShadow{
			Timestamp: wireTime(i % 60), MatchID: "cm", Rule: "r", Shape: "repeated", Severity: "warning",
			Mode: "audit", EventIDs: []string{"e1", "e2"}, Categories: []string{"c1"},
			FirstEvent: wireTime(5), LastEvent: wireTime(6),
		})
	}
	return s
}

func wireRoundTrip(t *testing.T, src *Session) *Session {
	t.Helper()
	blob, err := json.Marshal(sessionToData(src))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var sd sessionData
	if err := json.Unmarshal(blob, &sd); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return newSessionFromData(&sd, make(chan struct{}))
}

func TestSessionWire_RoundTripsEveryDurableField(t *testing.T) {
	src := fullWireSession()
	got := wireRoundTrip(t, src)

	st := reflect.TypeOf(src).Elem()
	sv := reflect.ValueOf(src).Elem()
	gv := reflect.ValueOf(got).Elem()
	for name := range wireExcluded {
		if _, ok := st.FieldByName(name); !ok {
			t.Errorf("wireExcluded names %q, which is not a Session field: drop the stale entry", name)
		}
	}
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if !f.IsExported() {
			continue
		}
		if _, excluded := wireExcluded[f.Name]; excluded {
			continue
		}
		if sv.Field(i).IsZero() {
			t.Errorf("fullWireSession does not populate Session.%s: populate it so the round trip can prove it", f.Name)
			continue
		}
		if !reflect.DeepEqual(sv.Field(i).Interface(), gv.Field(i).Interface()) {
			t.Errorf("Session.%s does not survive the Redis wire round trip: add it to sessionData, or to wireExcluded with a reason", f.Name)
		}
	}
}

func TestSessionWire_ContentNeverReachesTheBlob(t *testing.T) {
	s := NewSession("sess-content", "http://b", "127.0.0.1")
	s.RecordMessage("user", "SENTINEL-MESSAGE-CONTENT", "b")
	s.SetSystemPrompt("SENTINEL-SYSTEM-PROMPT")
	s.RecordToolCall("bash", "function", "req-1", `{"cmd":"SENTINEL-TOOL-ARGUMENT"}`)
	s.ToolCallHistory[0].Result = "SENTINEL-TOOL-RESULT"

	blob, err := json.Marshal(sessionToData(s))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, sentinel := range []string{"SENTINEL-MESSAGE-CONTENT", "SENTINEL-SYSTEM-PROMPT", "SENTINEL-TOOL-ARGUMENT", "SENTINEL-TOOL-RESULT"} {
		if strings.Contains(string(blob), sentinel) {
			t.Fatalf("content %q reached the Redis blob: %s", sentinel, blob)
		}
	}
	if !strings.Contains(string(blob), `"tool_name":"bash"`) {
		t.Fatalf("the content-free tool projection must ride: %s", blob)
	}

	got := wireRoundTrip(t, s)
	if len(got.GetMessages()) != 0 || got.SystemPrompt != "" {
		t.Fatal("conversation content must not round trip")
	}
	h := got.GetToolCallHistory()
	if len(h) != 1 || h[0].ToolName != "bash" || h[0].RequestID != "req-1" || h[0].Arguments != nil || h[0].Result != "" {
		t.Fatalf("tool history = %+v, want the name/type/request projection only", h)
	}
}

func TestSessionWire_TimesKeepNanosecondFidelity(t *testing.T) {
	src := fullWireSession()
	got := wireRoundTrip(t, src)
	if src.StartTime.Nanosecond() == 0 {
		t.Fatal("fixture: StartTime must carry nanoseconds")
	}
	if got.StartTime.UnixNano() != src.StartTime.UnixNano() || !got.StartTime.Equal(src.StartTime) {
		t.Fatalf("StartTime = %d ns, want %d ns: instance identity depends on sub-second fidelity", got.StartTime.UnixNano(), src.StartTime.UnixNano())
	}
	if got.LastActivity.UnixNano() != src.LastActivity.UnixNano() {
		t.Fatalf("LastActivity lost precision: %d != %d", got.LastActivity.UnixNano(), src.LastActivity.UnixNano())
	}
	if got.EndTime == nil || got.EndTime.UnixNano() != src.EndTime.UnixNano() {
		t.Fatalf("EndTime lost precision: %v != %v", got.EndTime, src.EndTime)
	}
}

// The in-memory cap and the wire clamp are one constant: raising the cap
// must not silently truncate the tail on every Redis reload.
func TestSessionWire_ToolHistoryCapIsTheSharedConstant(t *testing.T) {
	s := NewSession("sess-toolcap", "http://b", "127.0.0.1")
	for i := 0; i < maxToolCallHistory+7; i++ {
		s.RecordToolCall("bash", "function", "r", `{}`)
	}
	if n := len(s.ToolCallHistory); n != maxToolCallHistory {
		t.Fatalf("RecordToolCall kept %d records, want maxToolCallHistory=%d", n, maxToolCallHistory)
	}
	got := wireRoundTrip(t, s)
	if n := len(got.ToolCallHistory); n != maxToolCallHistory {
		t.Fatalf("a full in-memory history must round trip whole: %d records, want %d", n, maxToolCallHistory)
	}
}

func TestSessionWire_RestoreClampsToTheCaps(t *testing.T) {
	sd := &sessionData{ID: "sess-caps", StartTime: wireTime(1)}
	for i := 0; i < MaxSemanticShadow+10; i++ {
		sd.SemanticShadow = append(sd.SemanticShadow, SemanticShadow{DecisionID: "d", MessageIndex: i})
	}
	for i := 0; i < MaxCorrelationShadow+10; i++ {
		sd.CorrelationShadow = append(sd.CorrelationShadow, CorrelationShadow{MatchID: "m", Rule: "r"})
	}
	total := maxToolCallHistory + 30
	for i := 0; i < total; i++ {
		sd.ToolCallHistory = append(sd.ToolCallHistory, toolCallWire{ToolName: "t", RequestID: "r"})
	}
	sd.ToolCallHistory[total-1].ToolName = "newest"

	got := newSessionFromData(sd, make(chan struct{}))
	if len(got.SemanticShadow) != MaxSemanticShadow || got.SemanticShadow[0].MessageIndex != 0 {
		t.Fatalf("semantic shadow clamp: len=%d first=%d (newest-first list keeps its head)", len(got.SemanticShadow), got.SemanticShadow[0].MessageIndex)
	}
	if len(got.CorrelationShadow) != MaxCorrelationShadow {
		t.Fatalf("correlation shadow clamp: len=%d", len(got.CorrelationShadow))
	}
	h := got.ToolCallHistory
	if len(h) != maxToolCallHistory || h[maxToolCallHistory-1].ToolName != "newest" {
		t.Fatalf("tool history clamp: len=%d last=%q (oldest-first list keeps its tail)", len(h), h[len(h)-1].ToolName)
	}
}

func TestSessionWire_OldElevenFieldBlobStillDecodes(t *testing.T) {
	old := `{"id":"old","state":0,"start_time":"2026-11-07T10:00:01Z","last_activity":"2026-11-07T10:00:02Z","request_count":3,"bytes_in":1,"bytes_out":2,"backend":"b","client_addr":"c"}`
	var sd sessionData
	if err := json.Unmarshal([]byte(old), &sd); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := newSessionFromData(&sd, make(chan struct{}))
	if got.ID != "old" || got.RequestCount != 3 || got.Metadata == nil || got.BackendsUsed == nil || got.Terminated {
		t.Fatalf("old blob decoded as %+v", got)
	}
}

// sessionToData must deep-copy under the session lock: the blob is marshaled
// after the lock is released, while request goroutines keep mutating the
// session. Run under -race; map aliasing would crash or be reported.
func TestSessionWire_SessionToDataDoesNotAliasLiveState(t *testing.T) {
	s := NewSession("sess-race", "http://b", "127.0.0.1")
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s.AddTokens(1, 1)
				s.RecordToolCall("bash", "function", "r", `{}`)
				s.TouchAndRecord(1, "b")
				s.SetMetadata("k", "v")
				s.RecordSemanticShadow(SemanticShadow{DecisionID: "d"})
				s.RecordCorrelationShadow(CorrelationShadow{MatchID: "m", EventIDs: []string{"e"}})
				s.AddFailedBackend("b")
			}
		}
	}()
	for i := 0; i < 200; i++ {
		if _, err := json.Marshal(sessionToData(s)); err != nil {
			t.Fatalf("marshal: %v", err)
		}
	}
	close(stop)
	wg.Wait()
}
