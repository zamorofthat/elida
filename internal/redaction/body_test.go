package redaction

import (
	"encoding/json"
	"strings"
	"testing"
)

// Replay-ability guarantee (the acceptance bar from feedback #10): JSON in,
// VALID JSON out — numeric fields untouched, only string values scanned.
func TestRedactBodyKeepsJSONValid(t *testing.T) {
	r := NewPatternRedactor()
	body := `{"id":"chatcmpl-1","created":1753305600,"model":"gemma","n_params":7241000000,` +
		`"choices":[{"message":{"role":"assistant","content":"my ssn is 123-45-6789"}}]}`
	out := r.RedactBody(body)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if parsed["created"] != float64(1753305600) {
		t.Errorf("numeric field mangled: created = %v", parsed["created"])
	}
	if !strings.Contains(out, "[REDACTED") {
		t.Error("real SSN in string content not redacted")
	}
}

// The 623-corrupted-SSE-lines case: every data: line stays parseable.
func TestRedactBodySSEStaysParseable(t *testing.T) {
	r := NewPatternRedactor()
	sse := "data: {\"created\":1753305600,\"choices\":[{\"delta\":{\"content\":\"call 555-867-5309\"}}]}\n\n" +
		"data: {\"created\":1753305601,\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
		"data: [DONE]\n\n"
	out := r.RedactBody(sse)

	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "data: ") || strings.Contains(line, "[DONE]") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var v map[string]any
		if err := json.Unmarshal([]byte(payload), &v); err != nil {
			t.Fatalf("SSE line no longer parseable: %v\n%s", err, line)
		}
		if v["created"] == nil {
			t.Errorf("created lost from SSE line: %s", line)
		}
	}
	if !strings.Contains(out, "[REDACTED_PHONE]") {
		t.Error("real phone in SSE delta not redacted")
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Error("[DONE] sentinel mangled")
	}
}

// Non-JSON bodies keep the raw-string path (no behavior regression).
func TestRedactBodyNonJSONFallsBack(t *testing.T) {
	r := NewPatternRedactor()
	in := "plain text with ssn 123-45-6789"
	if out := r.RedactBody(in); !strings.Contains(out, "[REDACTED") {
		t.Errorf("non-JSON fallback lost redaction: %q", out)
	}
}

// Top-level JSON arrays too.
func TestRedactBodyJSONArray(t *testing.T) {
	r := NewPatternRedactor()
	out := r.RedactBody(`[{"content":"123-45-6789","created":1753305600}]`)
	var v []map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("array output invalid: %v", err)
	}
	if v[0]["created"] != float64(1753305600) {
		t.Errorf("numeric mangled in array: %v", v[0]["created"])
	}
}

// Disabled redactor: identity.
func TestRedactBodyDisabled(t *testing.T) {
	r := NewPatternRedactor()
	r.SetEnabled(false)
	in := `{"content":"123-45-6789"}`
	if out := r.RedactBody(in); out != in {
		t.Errorf("disabled redactor modified body: %q", out)
	}
}
