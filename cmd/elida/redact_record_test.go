package main

import (
	"encoding/json"
	"strings"
	"testing"

	"elida/internal/redaction"
	"elida/internal/storage"
)

// redactRecord is the third feedback-#10 call site (alongside
// proxy.persistFlaggedSession and OCSFEmitter.Emit): the session-end
// callback path (enrichRecordFromPolicy/enrichRecordFromCaptureBuffer ->
// redactRecord -> persistToSQLite) that persists captured request/response
// bodies to SQLite history. It must use RedactBody for bodies (JSON-aware),
// while Violations[i].MatchedText — a matched text fragment, not a
// document — stays on raw Redact, same split as proxy.go:1610-1614.
func TestRedactRecordUsesJSONAwareRedactionForBodies(t *testing.T) {
	a := &app{redactor: redaction.NewPatternRedactor()}

	// Bare (unquoted) 16-digit numeric id that happens to be Luhn-valid
	// (the standard Visa test number) — raw regex redaction mistakes it
	// for a credit card and replaces it with an unquoted [REDACTED_CC],
	// producing invalid JSON. RedactBody only ever touches string values,
	// so numeric fields are structurally untouchable.
	responseBody := `{"id":4111111111111111,"created":1753305600,"choices":[{"message":{"content":"ssn 123-45-6789"}}]}`

	record := &storage.SessionRecord{
		CapturedContent: []storage.CapturedRequest{
			{
				RequestBody:  `{"prompt":"my ssn is 123-45-6789"}`,
				ResponseBody: responseBody,
			},
		},
		Violations: []storage.Violation{
			{MatchedText: "123-45-6789"},
		},
	}

	a.redactRecord(record)

	got := record.CapturedContent[0].ResponseBody
	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("redacted response body is not valid JSON: %v\nbody: %s", err, got)
	}
	if parsed["created"] != float64(1753305600) {
		t.Errorf("numeric field mangled: created = %v (body: %s)", parsed["created"], got)
	}
	if !strings.Contains(got, "[REDACTED") {
		t.Errorf("expected redaction marker in captured response body, got: %s", got)
	}

	reqGot := record.CapturedContent[0].RequestBody
	if !strings.Contains(reqGot, "[REDACTED") {
		t.Errorf("expected redaction marker in captured request body, got: %s", reqGot)
	}

	// MatchedText is a text fragment, not a document — it must still be
	// redacted (via raw Redact), same as before.
	if !strings.Contains(record.Violations[0].MatchedText, "[REDACTED") {
		t.Errorf("expected MatchedText to still be redacted, got: %s", record.Violations[0].MatchedText)
	}
}

// Nil redactor: no-op, no panic.
func TestRedactRecordNilRedactorNoop(t *testing.T) {
	a := &app{}
	record := &storage.SessionRecord{
		CapturedContent: []storage.CapturedRequest{{ResponseBody: `{"id":1}`}},
	}
	a.redactRecord(record)
	if record.CapturedContent[0].ResponseBody != `{"id":1}` {
		t.Errorf("nil redactor should not modify content: %q", record.CapturedContent[0].ResponseBody)
	}
}
