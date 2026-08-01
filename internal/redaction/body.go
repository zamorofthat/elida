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
//     non-JSON payloads (e.g. [DONE]) pass through the raw redactor.
//   - Anything else: the raw-string redactor (previous behavior).
func (r *PatternRedactor) RedactBody(body string) string {
	if !r.IsEnabled() || body == "" {
		return body
	}
	if isSSE(body) {
		return r.redactSSE(body)
	}
	if out, ok := r.redactJSONString(body); ok {
		return out
	}
	return r.Redact(body)
}

func isSSE(body string) bool {
	trimmed := strings.TrimLeft(body, "\n\r ")
	return strings.HasPrefix(trimmed, "data: ") || strings.HasPrefix(trimmed, "event:")
}

// redactJSONString returns (redacted, true) when body parses as a JSON
// object or array; (_, false) otherwise.
func (r *PatternRedactor) redactJSONString(body string) (string, bool) {
	trimmed := strings.TrimSpace(body)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return "", false
	}
	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
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
		payload, found := strings.CutPrefix(line, "data: ")
		if !found {
			continue
		}
		if out, ok := r.redactJSONString(payload); ok {
			lines[i] = "data: " + out
		} else {
			lines[i] = "data: " + r.Redact(payload)
		}
	}
	return strings.Join(lines, "\n")
}
