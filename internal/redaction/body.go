package redaction

import (
	"encoding/json"
	"strings"
)

// RedactBody redacts a captured request/response body while preserving its
// structure — the fix for feedback #10, where raw-string redaction rewrote
// numeric JSON fields (unix timestamps as "phone numbers") and left 59% of
// the audit trail unparseable.
//
//   - JSON object/array: parse, redact string values only, re-marshal.
//   - SSE stream: each `data: ` line's JSON payload is handled as above;
//     non-JSON payloads (e.g. [DONE]) pass through the raw redactor; all
//     non-`data: ` lines are redacted with the raw redactor.
//   - Anything else: the raw-string redactor (previous behavior).
func (r *PatternRedactor) RedactBody(body string) string {
	if !r.IsEnabled() || body == "" {
		return body
	}
	// Try whole-body JSON first (it wins even if body contains "data: " string)
	if out, ok := r.redactJSONString(body); ok {
		return out
	}
	// Then try SSE
	if isSSE(body) {
		return r.redactSSE(body)
	}
	// Fall back to raw redaction
	return r.Redact(body)
}

func isSSE(body string) bool {
	// Detect SSE when ANY line starts with data: (may have retry:/id:/etc preamble)
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, "\r ")
		if strings.HasPrefix(trimmed, "data: ") {
			return true
		}
	}
	return false
}

// redactJSONString returns (redacted, true) when body parses as a JSON
// object or array; (_, false) otherwise. Uses json.Number to preserve
// numeric precision (19+ digit integers, exact float values).
func (r *PatternRedactor) redactJSONString(body string) (string, bool) {
	trimmed := strings.TrimSpace(body)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return "", false
	}

	// Use json.Number to preserve precision
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var v any
	if err := decoder.Decode(&v); err != nil {
		return "", false
	}

	switch data := v.(type) {
	case map[string]any:
		v = r.RedactMap(data)
	case []any:
		v = r.redactSlice(data)
	default:
		return "", false
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "", false // fall back to raw path rather than emit nothing
	}
	return string(out), true
}

func (r *PatternRedactor) redactSSE(body string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		// Track if line ends with \r for CRLF preservation
		hasCR := strings.HasSuffix(line, "\r")
		lineContent := strings.TrimSuffix(line, "\r")

		payload, found := strings.CutPrefix(lineContent, "data: ")
		if !found {
			// Non-data: lines are redacted with raw redactor
			lines[i] = r.Redact(lineContent)
			if hasCR {
				lines[i] += "\r"
			}
			continue
		}

		// Handle data: line — try JSON redaction first, fall back to raw
		var redacted string
		if out, ok := r.redactJSONString(payload); ok {
			redacted = "data: " + out
		} else {
			redacted = "data: " + r.Redact(payload)
		}

		if hasCR {
			redacted += "\r"
		}
		lines[i] = redacted
	}
	return strings.Join(lines, "\n")
}
