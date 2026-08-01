package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
)

// safeIDValue matches derived values usable verbatim in a session ID.
var safeIDValue = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// DeriveIDFromBody derives a session ID from a JSON request body.
// Precedence: bodyPath (explicit config) over the OpenAI "user" field.
// Returns "" when nothing derivable — callers fall back to IP-hash sessions.
// Derived IDs deliberately contain no backend component, so one conversation
// keeps one session across backend failover (integration feedback #4).
func DeriveIDFromBody(body []byte, openaiUser bool, bodyPath string) string {
	if len(body) == 0 || (!openaiUser && bodyPath == "") {
		return ""
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return ""
	}
	if bodyPath != "" {
		if v := lookupPath(data, bodyPath); v != "" {
			return formatDerivedID(v)
		}
	}
	if openaiUser {
		if u, ok := data["user"].(string); ok && u != "" {
			return formatDerivedID(u)
		}
	}
	return ""
}

// lookupPath walks a dot-separated key path and returns the string leaf, or "".
func lookupPath(data map[string]any, path string) string {
	cur := any(data)
	for _, key := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur, ok = obj[key]
		if !ok {
			return ""
		}
	}
	s, _ := cur.(string)
	return s
}

// formatDerivedID returns "user-<value>" for safe values, or a bounded
// stable hash form for long/unsafe ones.
func formatDerivedID(v string) string {
	if safeIDValue.MatchString(v) {
		return "user-" + v
	}
	sum := sha256.Sum256([]byte(v))
	return "user-" + hex.EncodeToString(sum[:8])
}
