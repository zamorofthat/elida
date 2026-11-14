package unit

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/control"
	"elida/internal/session"
	ws "elida/internal/websocket"
)

// A frame each of the two protocols these tests switch between.
const (
	openAIFrame     = `{"type":"session.create","session":{"model":"gpt-4o-realtime","voice":"alloy"}}`
	elevenLabsFrame = `{"text":"hello there","voice_settings":{"stability":0.5}}`
)

func newVoiceWSHandler(t *testing.T, enabled bool, protocols []string) *ws.Handler {
	t.Helper()
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	return ws.NewHandler(&config.WebSocketConfig{
		Enabled: true,
		VoiceSessions: config.VoiceSessionConfig{
			Enabled:   enabled,
			Protocols: protocols,
		},
	}, "X-Session-ID", manager, nil)
}

// statsBody mirrors only the part of /control/stats these tests assert on.
type statsBody struct {
	Total        int `json:"total"`
	Active       int `json:"active"`
	Capabilities struct {
		Voice struct {
			Enabled   bool     `json:"enabled"`
			Protocols []string `json:"protocols"`
		} `json:"voice"`
	} `json:"capabilities"`
}

func getStats(t *testing.T, h *control.Handler) statsBody {
	t.Helper()
	req := httptest.NewRequest("GET", "/control/stats", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET /control/stats: expected 200, got %d", w.Code)
	}
	var body statsBody
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decoding /control/stats: %v", err)
	}
	return body
}

// Without the WebSocket transport no handler exists, so no frame can ever
// start a voice session. The dashboard relies on this to not offer the tab.
func TestStats_VoiceCapability_OffWithoutWebSocket(t *testing.T) {
	handler, _ := newTestHandler()

	caps := getStats(t, handler).Capabilities.Voice
	if caps.Enabled {
		t.Error("expected voice capability to be false with no WebSocket handler")
	}
	if caps.Protocols == nil {
		t.Error("expected protocols to serialize as [], not null")
	}
	if len(caps.Protocols) != 0 {
		t.Errorf("expected no protocols, got %v", caps.Protocols)
	}
}

// The transport being up does not mean voice is tracked: voice_sessions.enabled
// gates that separately, and this is the case that answers 200 with an empty
// list rather than 503, so a status code alone cannot distinguish it.
func TestStats_VoiceCapability_OffWhenTrackingDisabled(t *testing.T) {
	handler, _ := newTestHandler()
	handler.SetWebSocketHandler(newVoiceWSHandler(t, false, nil))

	caps := getStats(t, handler).Capabilities.Voice
	if caps.Enabled {
		t.Error("expected voice capability false when voice_sessions.enabled is false")
	}
	if len(caps.Protocols) != 0 {
		t.Errorf("expected no protocols when tracking is off, got %v", caps.Protocols)
	}
}

func TestStats_VoiceCapability_ReportsConfiguredProtocols(t *testing.T) {
	handler, _ := newTestHandler()
	handler.SetWebSocketHandler(newVoiceWSHandler(t, true, []string{"elevenlabs", "deepgram"}))

	caps := getStats(t, handler).Capabilities.Voice
	if !caps.Enabled {
		t.Fatal("expected voice capability true")
	}
	// Reported in the order they are tried, which is the parser's order and
	// not the order they were named in the config.
	want := []string{"deepgram", "elevenlabs"}
	if !reflect.DeepEqual(caps.Protocols, want) {
		t.Errorf("expected protocols %v, got %v", want, caps.Protocols)
	}
}

// Capabilities are added alongside the existing statistics, which stay at the
// top level of the response; clients decoding into session.Stats still work.
func TestStats_KeepsSessionFields(t *testing.T) {
	handler, manager := newTestHandler()
	manager.GetOrCreate("a", "http://backend", "127.0.0.1")
	manager.GetOrCreate("b", "http://backend", "127.0.0.1")

	body := getStats(t, handler)
	if body.Total != 2 {
		t.Errorf("expected total 2, got %d", body.Total)
	}
	if body.Active != 2 {
		t.Errorf("expected active 2, got %d", body.Active)
	}
}

func TestSessionControlParser_EmptyProtocolListEnablesAll(t *testing.T) {
	parser := ws.NewSessionControlParserFor(nil, nil)

	want := []string{"openai_realtime", "deepgram", "elevenlabs", "livekit"}
	if got := parser.EnabledProtocols(); !reflect.DeepEqual(got, want) {
		t.Errorf("expected all protocols %v, got %v", want, got)
	}
	if msg := parser.Parse([]byte(openAIFrame)); msg == nil {
		t.Error("expected the OpenAI frame to parse when no filter is set")
	}
}

func TestSessionControlParser_HonoursProtocolFilter(t *testing.T) {
	parser := ws.NewSessionControlParserFor(nil, []string{"elevenlabs"})

	if got := parser.EnabledProtocols(); !reflect.DeepEqual(got, []string{"elevenlabs"}) {
		t.Errorf("expected only elevenlabs, got %v", got)
	}
	if msg := parser.Parse([]byte(openAIFrame)); msg != nil {
		t.Errorf("expected the OpenAI frame to be ignored, got protocol %q", msg.Protocol)
	}
	msg := parser.Parse([]byte(elevenLabsFrame))
	if msg == nil {
		t.Fatal("expected the ElevenLabs frame to parse")
	}
	if msg.Protocol != "elevenlabs" {
		t.Errorf("expected protocol elevenlabs, got %q", msg.Protocol)
	}
}

// Protocol names are hand-written in YAML, so surrounding space and casing
// must not silently drop a protocol the operator asked for.
func TestSessionControlParser_ProtocolNamesAreForgiving(t *testing.T) {
	parser := ws.NewSessionControlParserFor(nil, []string{"  ElevenLabs  "})

	if got := parser.EnabledProtocols(); !reflect.DeepEqual(got, []string{"elevenlabs"}) {
		t.Errorf("expected elevenlabs to be matched, got %v", got)
	}
}

// An unrecognised name must not fall back to enabling everything: that would
// parse frames the operator deliberately excluded.
func TestSessionControlParser_UnknownProtocolSelectsNothing(t *testing.T) {
	parser := ws.NewSessionControlParserFor(nil, []string{"not_a_protocol"})

	if got := parser.EnabledProtocols(); len(got) != 0 {
		t.Errorf("expected no protocols for an unknown name, got %v", got)
	}
	if msg := parser.Parse([]byte(openAIFrame)); msg != nil {
		t.Error("expected no frame to parse when no known protocol is enabled")
	}
}

// Custom patterns are configured separately and must survive filtering.
func TestSessionControlParser_CustomPatternsSurviveFilter(t *testing.T) {
	custom := &ws.CustomPatternConfig{
		Patterns: []ws.CustomPattern{
			{Name: "start", TypeStr: "invite", Pattern: `"action":\s*"start_call"`},
		},
	}
	parser := ws.NewSessionControlParserFor(custom, []string{"elevenlabs"})

	want := []string{"elevenlabs", "custom"}
	if got := parser.EnabledProtocols(); !reflect.DeepEqual(got, want) {
		t.Errorf("expected %v, got %v", want, got)
	}
	if msg := parser.Parse([]byte(`{"action": "start_call"}`)); msg == nil {
		t.Error("expected the custom pattern to still parse")
	}
}
