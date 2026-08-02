package proxy

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/policy"
	"elida/internal/redaction"
	"elida/internal/session"
	"elida/internal/storage"
)

// TestPersistFlaggedSessionUsesJSONAwareRedaction is the e2e regression test
// for feedback #10: captured request/response bodies must survive redaction
// as valid, still-parseable JSON with numeric fields untouched, while string
// content (like an SSN) is still redacted. Drives a flagged session through
// the real capture -> redact -> persist path (persistFlaggedSession, the
// method proxy.go's capture redaction call site lives in).
func TestPersistFlaggedSessionUsesJSONAwareRedaction(t *testing.T) {
	const sessionID = "sess-redaction-capture"
	const triggerPattern = "TRIGGER_FLAG"

	// Policy engine with a request-content rule that flags the session and
	// enables capture, mirroring how the real proxy flags + captures.
	engine := policy.NewEngine(policy.Config{
		Enabled:        true,
		Mode:           "enforce",
		CaptureContent: true,
		MaxCaptureSize: 1 << 20,
		Rules: []policy.Rule{
			{
				Name:     "trigger",
				Type:     policy.RuleTypeContentMatch,
				Target:   policy.RuleTargetRequest,
				Patterns: []string{triggerPattern},
				Severity: policy.SeverityWarning,
				Action:   "flag",
			},
		},
	})

	// Flag the session via request content evaluation (as proxy.go does
	// before calling CaptureRequest).
	if result := engine.EvaluateContent(sessionID, "please "+triggerPattern+" now"); result == nil {
		t.Fatal("expected request content to flag the session")
	}
	engine.CaptureRequest(sessionID, policy.CapturedRequest{
		Timestamp:   time.Now(),
		Method:      "POST",
		Path:        "/v1/messages",
		RequestBody: "please " + triggerPattern + " now",
		StatusCode:  200,
	})

	// "id" is a bare (unquoted) 16-digit numeric field that happens to be
	// Luhn-valid (the well-known Visa test card number, digits-only) — a
	// realistic false-positive: request/trace IDs are exactly this shape.
	// Raw regex redaction matches it as a credit card and replaces it with
	// "[REDACTED_CC]" *unquoted*, producing invalid JSON. JSON-aware
	// redaction (RedactBody) only ever touches string values, so a bare
	// numeric field is structurally untouchable — this is the feedback #10
	// failure mode ("numeric JSON fields... left the audit trail
	// unparseable"), reproduced with the current (tightened) patterns.
	responseBody := `{"id":4111111111111111,"created":1753305600,"choices":[{"message":{"content":"ssn 123-45-6789"}}]}`
	engine.UpdateLastCaptureWithResponseAndStatus(sessionID, responseBody, 200)

	if !engine.IsFlagged(sessionID) {
		t.Fatal("session should be flagged")
	}

	// SQLite-backed storage so persistFlaggedSession has somewhere to write.
	dbPath := filepath.Join(t.TempDir(), "elida.db")
	store, err := storage.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.Close()

	cfg := &config.Config{Backend: "http://127.0.0.1:1"}
	sessStore := session.NewMemoryStore()
	manager := session.NewManager(sessStore, time.Hour)
	redactor := redaction.NewPatternRedactor()

	p, err := New(cfg, sessStore, manager, WithPolicyEngine(engine), WithRedactor(redactor))
	if err != nil {
		t.Fatalf("New proxy: %v", err)
	}
	p.SetStorage(store)

	sess := session.NewSession(sessionID, "default", "127.0.0.1")

	// The capture -> redact -> persist path (proxy.go:1610 lives inside this call).
	p.persistFlaggedSession(sess, "default")

	rec, err := store.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if rec == nil {
		t.Fatal("expected a persisted session record")
	}
	if len(rec.CapturedContent) != 1 {
		t.Fatalf("expected 1 captured request/response pair, got %d", len(rec.CapturedContent))
	}

	got := rec.CapturedContent[0].ResponseBody

	// (1) Still valid JSON.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("redacted response body is not valid JSON: %v\nbody: %s", err, got)
	}

	// (2) Numeric "created" field untouched.
	if parsed["created"] != float64(1753305600) {
		t.Errorf("numeric field mangled: created = %v (body: %s)", parsed["created"], got)
	}

	// (3) The SSN in string content was still redacted.
	if !strings.Contains(got, "[REDACTED") {
		t.Errorf("expected redaction marker in captured response body, got: %s", got)
	}
}
