package unit

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"elida/internal/control"
	"elida/internal/session"
)

// postureStatus is a status whose every field is something an attacker would
// want: enforcement posture, detector health, calibration state and model
// identity. Nothing in it may reach an unauthenticated caller.
func postureStatus() control.DecisionStatus {
	return control.DecisionStatus{
		Enabled:             true,
		Mode:                "enforce",
		EffectiveMode:       "shadow",
		Capability:          "degraded",
		ThresholdSetMatches: false,
		BreakerOpen:         true,
		Model:               "minilm-multihead",
		ModelChecksum:       "deadbeef",
		ThresholdSet:        "v1",
		Reason:              "/etc/elida/models/injection could not be verified",
	}
}

func newPostureHandler(t *testing.T, st control.DecisionStatus, opts ...control.Option) *control.Handler {
	t.Helper()
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	h := control.New(store, manager, opts...)
	h.SetDecisionProvider(fixedDecision{st})
	return h
}

// The unauthenticated route says the process is up and nothing else. It
// carries no decision block at all: not a trimmed one, absent. This replaces
// the D4(a) contract shipped in v0.30.0, which put the capability enum, the
// effective mode, the threshold-set match and the breaker state on it.
//
// An absent block, rather than a trimmed one, is the point: a trimmed block
// is a place for the next convenient field to land.
func TestHealthPosture_HealthCarriesNoDecisionBlock(t *testing.T) {
	h := newPostureHandler(t, postureStatus())

	w := doJSON(t, h, http.MethodGet, "/control/health", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", w.Code)
	}

	keys := jsonKeys(t, w.Body.Bytes())
	if keys["decision"] {
		t.Fatalf("health must carry no decision block: %s", w.Body.String())
	}
	if !keys["status"] || !keys["timestamp"] {
		t.Fatalf("health must keep status and timestamp: %s", w.Body.String())
	}
	if len(keys) != 2 {
		t.Fatalf("health keys = %v, want exactly status and timestamp", keys)
	}

	// Belt and braces: no posture value may appear anywhere in the body,
	// whatever shape a future handler gives it.
	for _, leak := range []string{
		"shadow", "enforce", "degraded", "breaker", "threshold",
		"minilm", "deadbeef", "/etc/elida", "could not be verified",
	} {
		if contains(w.Body.String(), leak) {
			t.Fatalf("health leaks %q: %s", leak, w.Body.String())
		}
	}
}

// The posture did not vanish; it is on the authenticated route, where it
// already was before this change.
func TestHealthPosture_DecisionStillCarriesThePosture(t *testing.T) {
	h := newPostureHandler(t, postureStatus())

	w := doJSON(t, h, http.MethodGet, "/control/decision", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("decision status = %d, want 200", w.Code)
	}
	keys := jsonKeys(t, w.Body.Bytes())
	for _, want := range []string{"effective_mode", "threshold_set_matches", "breaker_open", "capability"} {
		if !keys[want] {
			t.Fatalf("/control/decision lost %q: %s", want, w.Body.String())
		}
	}
}

// Readiness is a status code and nothing else. A body is a place for facts to
// accumulate, so there is none.
func TestHealthPosture_ReadyIsAStatusCodeOnly(t *testing.T) {
	cases := []struct {
		name string
		st   control.DecisionStatus
		want int
	}{
		{
			name: "healthy detector is ready",
			st:   control.DecisionStatus{Enabled: true, Capability: "inline"},
			want: http.StatusOK,
		},
		{
			name: "async_only is ready: results protect later activity",
			st:   control.DecisionStatus{Enabled: true, Capability: "async_only"},
			want: http.StatusOK,
		},
		{
			name: "detection off is ready: nothing was promised",
			st:   control.DecisionStatus{Enabled: false, Capability: "disabled"},
			want: http.StatusOK,
		},
		{
			name: "enabled but degraded is NOT ready",
			st:   control.DecisionStatus{Enabled: true, Capability: "degraded"},
			want: http.StatusServiceUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newPostureHandler(t, tc.st)
			w := doJSON(t, h, http.MethodGet, "/control/ready", nil)
			if w.Code != tc.want {
				t.Fatalf("ready status = %d, want %d", w.Code, tc.want)
			}
			if w.Body.Len() != 0 {
				t.Fatalf("ready must have an empty body, got %q", w.Body.String())
			}
		})
	}
}

// With no provider wired the feature is off, so the deployment is ready.
func TestHealthPosture_ReadyWithoutAProviderIsOK(t *testing.T) {
	store := session.NewMemoryStore()
	h := control.New(store, session.NewManager(store, 5*time.Minute))

	w := doJSON(t, h, http.MethodGet, "/control/ready", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("ready status = %d, want 200", w.Code)
	}
}

// Probes cannot carry credentials, which is the whole reason the health
// exemption exists. Readiness inherits it.
func TestHealthPosture_ReadyIsExemptFromAuth(t *testing.T) {
	h := newPostureHandler(t,
		control.DecisionStatus{Enabled: true, Capability: "inline"},
		control.WithAuth("secret-key-123"))

	w := doJSON(t, h, http.MethodGet, "/control/ready", nil)
	if w.Code == http.StatusUnauthorized {
		t.Fatal("/control/ready must not require a credential")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("ready status = %d, want 200", w.Code)
	}
}

// A probe uses GET or HEAD. Anything else is a mistake worth reporting.
func TestHealthPosture_ReadyRejectsNonProbeMethods(t *testing.T) {
	h := newPostureHandler(t, control.DecisionStatus{Enabled: true, Capability: "inline"})

	if w := doJSON(t, h, http.MethodHead, "/control/ready", nil); w.Code != http.StatusOK {
		t.Fatalf("HEAD /control/ready = %d, want 200", w.Code)
	}
	if w := doJSON(t, h, http.MethodPost, "/control/ready", nil); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /control/ready = %d, want 405", w.Code)
	}
}

// The deprecated compatibility shim. Off by default — every other test in
// this file proves that — and when switched on it serves exactly the block
// v0.30.0 and v0.31.0 served, so a deployment that scrapes those fields can
// upgrade without losing them. Enabling it restores the disclosure, which is
// the point of making it explicit and temporary.
func TestHealthPosture_LegacyDecisionBlockIsOptIn(t *testing.T) {
	h := newPostureHandler(t, postureStatus(), control.WithLegacyHealthDecision())

	w := doJSON(t, h, http.MethodGet, "/control/health", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", w.Code)
	}

	var resp struct {
		Status    string         `json:"status"`
		Timestamp string         `json:"timestamp"`
		Decision  map[string]any `json:"decision"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode health: %v", err)
	}

	if resp.Status != "ok" {
		t.Fatalf("status = %q, want ok", resp.Status)
	}
	if resp.Timestamp == "" {
		t.Fatal("timestamp must stay on the legacy payload")
	}

	// The v0.30.0 key set, exactly: no more, no fewer.
	want := map[string]any{
		"capability":            "degraded",
		"reason":                "semantic detection was enabled but the model could not be loaded or verified; no detection is active",
		"effective_mode":        "shadow",
		"threshold_set_matches": false,
		"breaker_open":          true,
	}
	if len(resp.Decision) != len(want) {
		t.Fatalf("legacy decision block has %d keys (%v), want exactly %d", len(resp.Decision), resp.Decision, len(want))
	}
	for k, v := range want {
		got, ok := resp.Decision[k]
		if !ok {
			t.Errorf("legacy decision block is missing %q", k)
			continue
		}
		if got != v {
			t.Errorf("legacy decision[%q] = %v, want %v", k, got, v)
		}
	}

	// Still no model identity, checksum, threshold set or provider reason:
	// the shim restores the old block, not a wider one.
	for _, forbidden := range []string{"model", "model_checksum", "threshold_set", "enabled", "mode"} {
		if _, ok := resp.Decision[forbidden]; ok {
			t.Errorf("legacy decision block must not carry %q: %s", forbidden, w.Body.String())
		}
	}
	for _, leak := range []string{"deadbeef", "minilm", "/etc/elida"} {
		if contains(w.Body.String(), leak) {
			t.Errorf("legacy health payload leaks %q: %s", leak, w.Body.String())
		}
	}
}

// The shim does not touch readiness: /control/ready stays a status code with
// no body whether or not the legacy block is restored.
func TestHealthPosture_LegacyShimLeavesReadinessAlone(t *testing.T) {
	st := postureStatus()
	st.Enabled = true
	st.Capability = "degraded"
	h := newPostureHandler(t, st, control.WithLegacyHealthDecision())

	w := doJSON(t, h, http.MethodGet, "/control/ready", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status = %d, want 503", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("ready must carry no body, got %q", w.Body.String())
	}
}
