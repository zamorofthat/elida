package unit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"elida/internal/control"
	"elida/internal/session"
)

func postKill(t *testing.T, h http.Handler, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	var req *http.Request
	if rd != nil {
		req = httptest.NewRequest("POST", "/control/sessions/"+id+"/kill", rd)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest("POST", "/control/sessions/"+id+"/kill", nil)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func killBlockHandler() (*control.Handler, *session.Manager) {
	store := session.NewMemoryStore()
	manager := session.NewManagerWithKillBlock(store, 5*time.Minute,
		session.KillBlockConfig{Mode: session.KillBlockDuration, Duration: time.Minute})
	return control.New(store, manager), manager
}

func sessionInfo(t *testing.T, h http.Handler, id string) map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", "/control/sessions/"+id, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET session: %d", w.Code)
	}
	var info map[string]any
	if err := json.NewDecoder(w.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	return info
}

// blockedUntil reads blocked_until from session info, failing the test when it
// is missing or not a time.
func blockedUntil(t *testing.T, info map[string]any) time.Time {
	t.Helper()
	raw, ok := info["blocked_until"].(string)
	if !ok {
		t.Fatalf("blocked_until = %v, want a time", info["blocked_until"])
	}
	until, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatalf("blocked_until: %v", err)
	}
	return until
}

func TestHandler_Kill_AcceptsAPermanentBlock(t *testing.T) {
	h, m := killBlockHandler()
	m.GetOrCreate("kb-perm", "http://backend", "127.0.0.1")

	w := postKill(t, h, "kb-perm", `{"block":"permanent"}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "killed" || resp["block"] != "permanent" {
		t.Errorf("response = %v, want status killed and block permanent", resp)
	}
	info := sessionInfo(t, h, "kb-perm")
	if info["kill_block"] != "permanent" {
		t.Errorf("session info kill_block = %v, want permanent", info["kill_block"])
	}
	if _, has := info["blocked_until"]; has {
		t.Error("a permanent block has no end time")
	}
}

func TestHandler_Kill_AcceptsADuration(t *testing.T) {
	h, m := killBlockHandler()
	m.GetOrCreate("kb-dur", "http://backend", "127.0.0.1")

	w := postKill(t, h, "kb-dur", `{"block":"45m"}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	info := sessionInfo(t, h, "kb-dur")
	if info["kill_block"] != "duration" {
		t.Fatalf("kill_block = %v, want duration", info["kill_block"])
	}
	until := blockedUntil(t, info)
	if d := time.Until(until); d < 44*time.Minute || d > 45*time.Minute {
		t.Errorf("blocked_until is %v from now, want about 45m", d)
	}
}

func TestHandler_Kill_WithoutABlockUsesTheConfiguredOne(t *testing.T) {
	for name, body := range map[string]string{"no body": "", "empty object": `{}`, "default": `{"block":"default"}`} {
		t.Run(name, func(t *testing.T) {
			h, m := killBlockHandler()
			m.GetOrCreate("kb-def", "http://backend", "127.0.0.1")

			if w := postKill(t, h, "kb-def", body); w.Code != http.StatusOK {
				t.Fatalf("status %d", w.Code)
			}
			info := sessionInfo(t, h, "kb-def")
			if info["kill_block"] != "duration" {
				t.Fatalf("kill_block = %v, want duration", info["kill_block"])
			}
			if until := blockedUntil(t, info); time.Until(until) > time.Minute {
				t.Errorf("want the configured 1m duration block, got kill_block=%v blocked_until=%v", info["kill_block"], info["blocked_until"])
			}
		})
	}
}

// A request that asks for something the server cannot give must not kill:
// the operator would believe a 30 minute block is in force when it is not.
func TestHandler_Kill_RejectsABadBlockAndKillsNothing(t *testing.T) {
	for name, body := range map[string]string{
		"unknown word":  `{"block":"forever"}`,
		"zero duration": `{"block":"0s"}`,
		"negative":      `{"block":"-5m"}`,
		"bare number":   `{"block":"30"}`,
		"not json":      `block=permanent`,
		"wrong type":    `{"block":30}`,
	} {
		t.Run(name, func(t *testing.T) {
			h, m := killBlockHandler()
			m.GetOrCreate("kb-bad", "http://backend", "127.0.0.1")

			w := postKill(t, h, "kb-bad", body)

			if w.Code != http.StatusBadRequest {
				t.Errorf("status %d, want 400", w.Code)
			}
			if sess, _ := m.Get("kb-bad"); sess.GetState() != session.Active {
				t.Error("the session was killed by a request that was rejected")
			}
		})
	}
}

func TestHandler_Kill_SessionInfoShowsNoBlockForALiveSession(t *testing.T) {
	h, m := killBlockHandler()
	m.GetOrCreate("kb-live", "http://backend", "127.0.0.1")
	info := sessionInfo(t, h, "kb-live")
	if _, has := info["kill_block"]; has {
		t.Errorf("an active session reports kill_block = %v", info["kill_block"])
	}
}
