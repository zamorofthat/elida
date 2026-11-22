// Package redaction provides PII and sensitive data redaction for audit logs.
package redaction

import (
	"net"
	"regexp"
	"strings"
	"sync"
)

// Redactor handles redaction of sensitive data
type Redactor interface {
	Redact(content string) string
	// RedactBody redacts a request/response body while preserving JSON
	// structure when the body is (or contains) JSON — see PatternRedactor.RedactBody.
	RedactBody(body string) string
}

// Pattern represents a redaction pattern
type Pattern struct {
	Name        string
	Regex       *regexp.Regexp
	Replacement string
	Validate    func(match string) bool // nil validates everything (previous behavior)
	// MayMatch is an optional cheap pre-check: when it returns false the
	// pattern cannot match the text, so the regexp is not run. It must never
	// return false for text the regexp would match. Nil means always run.
	MayMatch func(text string) bool
}

// Options configures a PatternRedactor.
type Options struct {
	RedactPrivateIPs bool      // also redact loopback/RFC1918 IPs (default false — feedback #10: every measured hit was loopback)
	CustomPatterns   []Pattern // appended after defaults
}

// PatternRedactor implements Redactor using regex patterns
type PatternRedactor struct {
	mu       sync.RWMutex
	patterns []Pattern
	enabled  bool
}

// luhnValid reports whether digits (0-9 only) pass the Luhn checksum.
func luhnValid(digits string) bool {
	if len(digits) < 13 {
		return false
	}
	sum, alt := 0, false
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if d < 0 || d > 9 {
			return false
		}
		if alt {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
	}
	return sum%10 == 0
}

// isPrivateOrLoopbackIP reports whether ip is loopback, RFC1918, or link-local.
func isPrivateOrLoopbackIP(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// hasDigit reports whether s contains an ASCII digit.
func hasDigit(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i]-'0' <= 9 {
			return true
		}
	}
	return false
}

// containsFold reports whether s contains lit, ignoring ASCII case. lit must
// be lower case. It looks for the rarest-first byte with IndexByte, which is
// vectorized, instead of running a regexp over the whole text.
func containsFold(s, lit string) bool {
	n := len(lit)
	if n == 0 {
		return true
	}
	// (?i) regexps also fold K to the Kelvin sign and S to the long s, so a
	// byte search cannot rule those out: non-ASCII text is never skipped.
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return true
		}
	}
	first, upper := lit[0], lit[0]-'a'+'A'
	// i only moves forward, so once one letter case no longer occurs in the
	// rest of s it never will again: stop searching for it. Searching on every
	// step made a long run of the other case quadratic.
	noLower, noUpper := false, false
	for i := 0; i+n <= len(s); i++ {
		j1, j2 := -1, -1
		if !noLower {
			if j1 = strings.IndexByte(s[i:], first); j1 < 0 {
				noLower = true
			}
		}
		if !noUpper {
			if j2 = strings.IndexByte(s[i:], upper); j2 < 0 {
				noUpper = true
			}
		}
		switch {
		case j1 < 0 && j2 < 0:
			return false
		case j1 < 0:
			i += j2
		case j2 < 0:
			i += j1
		default:
			i += min(j1, j2)
		}
		if i+n <= len(s) && strings.EqualFold(s[i:i+n], lit) {
			return true
		}
	}
	return false
}

func anyFold(lits ...string) func(string) bool {
	return func(s string) bool {
		for _, l := range lits {
			if containsFold(s, l) {
				return true
			}
		}
		return false
	}
}

// privateKeyBlockRegexp matches a PEM private key: the header, the Proc-Type
// and DEK-Info lines of an encrypted key, and the base64 body, through the END
// line when it follows. Line breaks are real whitespace or the escapes \n, \r
// and \t, which is how a key sits inside a JSON string, and a "/" may be
// written \/ . The body is runs of 16 or more base64 characters with a short
// last line, so text that does not look like a key body ends the match. A key
// header is only Proc-Type or DEK-Info, separated from what precedes it by any mix of whitespace and the escapes \\n, \\r and \\t.
var privateKeyBlockRegexp = func() *regexp.Regexp {
	const (
		brk    = `(?:\s|\\[nrt])*`
		sep    = `(?:\s|\\[nrt])+`
		header = `(?:` + sep + `(?:proc-type:[ \t]*[0-9]+,[a-z]+|dek-info:[ \t]*[a-z0-9-]+,[0-9a-f]+))*`
		b64    = `(?:[a-z0-9+=]|\\?/)`
		body   = `(?:` + brk + b64 + `{16,})*`
	)
	return regexp.MustCompile(`(?i)-----BEGIN [A-Z ]*PRIVATE KEY-----` + header +
		`(?:` + body + brk + b64 + `*` + brk + `-----END [A-Z ]*PRIVATE KEY-----|` + body + `)`)
}()

// DefaultPatterns returns the standard set of PII redaction patterns
func DefaultPatterns() []Pattern {
	return defaultPatterns(false)
}

// defaultPatterns returns the standard set of PII redaction patterns with configurable private IP handling
func defaultPatterns(redactPrivateIPs bool) []Pattern {
	return []Pattern{
		{
			Name:        "email",
			MayMatch:    func(s string) bool { return strings.IndexByte(s, '@') >= 0 },
			Regex:       regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Z|a-z]{2,}\b`),
			Replacement: "[REDACTED_EMAIL]",
		},
		{
			Name:        "ssn",
			MayMatch:    func(s string) bool { return strings.IndexByte(s, '-') >= 0 },
			Regex:       regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
			Replacement: "[REDACTED_SSN]",
		},
		{
			Name:        "credit_card",
			MayMatch:    hasDigit,
			Regex:       regexp.MustCompile(`\b(?:\d[ -]?){13,16}\b`),
			Replacement: "[REDACTED_CC]",
			Validate: func(m string) bool {
				digits := strings.ReplaceAll(strings.ReplaceAll(m, " ", ""), "-", "")
				return len(digits) >= 13 && len(digits) <= 16 && luhnValid(digits)
			},
		},
		{
			Name:        "phone_us",
			MayMatch:    hasDigit,
			Regex:       regexp.MustCompile(`(^|[^0-9.-])((?:\+?1[-.\s])?(?:\(\d{3}\)\s?|\d{3}[-.])\d{3}[-.]\d{4})\b`),
			Replacement: "${1}[REDACTED_PHONE]",
		},
		{
			Name:        "api_key_bearer",
			MayMatch:    anyFold("bearer"),
			Regex:       regexp.MustCompile(`(?i)(bearer\s+)([a-zA-Z0-9_.-]{20,})`),
			Replacement: "$1[REDACTED_TOKEN]",
		},
		{
			Name:        "api_key_sk",
			MayMatch:    anyFold("sk-"),
			Regex:       regexp.MustCompile(`(?i)(sk-[a-zA-Z0-9]{20,})`),
			Replacement: "[REDACTED_API_KEY]",
		},
		{
			Name:        "api_key_generic",
			MayMatch:    anyFold("key", "token"),
			Regex:       regexp.MustCompile(`(?i)(api[_-]?key|secret[_-]?key|auth[_-]?token)[:\s=]["']?([a-zA-Z0-9_.-]{16,})["']?`),
			Replacement: "$1=[REDACTED_KEY]",
		},
		{
			Name:        "password_json",
			MayMatch:    anyFold("password", "passwd", "pwd"),
			Regex:       regexp.MustCompile(`(?i)"(password|passwd|pwd)":\s*"([^"]{4,})"`),
			Replacement: `"$1": "[REDACTED_PASSWORD]"`,
		},
		{
			Name:        "password_field",
			MayMatch:    anyFold("password", "passwd", "pwd"),
			Regex:       regexp.MustCompile(`(?i)(password|passwd|pwd)[\s]*[=:][\s]*["']?([^\s"',}]{4,})["']?`),
			Replacement: "$1=[REDACTED_PASSWORD]",
		},
		{
			Name:        "ip_address",
			MayMatch:    func(s string) bool { return strings.IndexByte(s, '.') >= 0 },
			Regex:       regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`),
			Replacement: "[REDACTED_IP]",
			Validate: func(m string) bool {
				return redactPrivateIPs || !isPrivateOrLoopbackIP(m)
			},
		},
		{
			Name:        "jwt_token",
			MayMatch:    func(s string) bool { return strings.Contains(s, "eyJ") },
			Regex:       regexp.MustCompile(`eyJ[a-zA-Z0-9_-]*\.eyJ[a-zA-Z0-9_-]*\.[a-zA-Z0-9_-]*`),
			Replacement: "[REDACTED_JWT]",
		},
		{
			Name:        "aws_access_key",
			MayMatch:    anyFold("akia"),
			Regex:       regexp.MustCompile(`(?i)(AKIA[0-9A-Z]{16})`),
			Replacement: "[REDACTED_AWS_KEY]",
		},
		{
			Name:        "aws_temporary_key",
			MayMatch:    anyFold("asia"),
			Regex:       regexp.MustCompile(`(?i)\basia[0-9a-z]{16}\b`),
			Replacement: "[REDACTED_AWS_KEY]",
		},
		{
			Name:        "openai_project_key",
			MayMatch:    anyFold("sk-proj-", "sk-svcacct-", "sk-admin-"),
			Regex:       regexp.MustCompile(`(?i)\bsk-(?:proj|svcacct|admin)-[a-z0-9_-]{40,}`),
			Replacement: "[REDACTED_API_KEY]",
		},
		{
			Name:        "github_token",
			MayMatch:    anyFold("ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_"),
			Regex:       regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{40,})\b`),
			Replacement: "[REDACTED_GITHUB_TOKEN]",
		},
		{
			Name:        "slack_token",
			MayMatch:    anyFold("xox"),
			Regex:       regexp.MustCompile(`(?i)\bxox[abprs]-[a-z0-9-]{20,}`),
			Replacement: "[REDACTED_SLACK_TOKEN]",
		},
		{
			Name:        "anthropic_api_key",
			MayMatch:    anyFold("sk-ant-"),
			Regex:       regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`),
			Replacement: "[REDACTED_API_KEY]",
		},
		{
			// The header marks the start of a key; the body that follows is the
			// secret. A body is base64: runs of 16 or more base64 characters,
			// with a short last line. Redact the header and that body, through
			// the END line when it follows directly. Text that does not look
			// like a key body ends the match, so a prompt between a header and
			// a later END line, or prose after an unterminated header, stays
			// readable in the capture.
			Name:        "private_key_block",
			MayMatch:    anyFold("-----begin"),
			Regex:       privateKeyBlockRegexp,
			Replacement: "[REDACTED_PRIVATE_KEY]",
		},
		{
			Name:        "base64_secret",
			MayMatch:    anyFold("secret", "private"),
			Regex:       regexp.MustCompile(`(?i)(secret|private[_-]?key)[:\s=]["']?([A-Za-z0-9+/]{40,}={0,2})["']?`),
			Replacement: "$1=[REDACTED_SECRET]",
		},
	}
}

// NewPatternRedactor creates a new PatternRedactor with default patterns
func NewPatternRedactor() *PatternRedactor {
	return NewPatternRedactorWithOptions(Options{})
}

// NewPatternRedactorWithOptions creates a new PatternRedactor with custom options
func NewPatternRedactorWithOptions(opts Options) *PatternRedactor {
	patterns := defaultPatterns(opts.RedactPrivateIPs)
	patterns = append(patterns, opts.CustomPatterns...)
	return &PatternRedactor{
		patterns: patterns,
		enabled:  true,
	}
}

// NewPatternRedactorWithPatterns creates a PatternRedactor with custom patterns
func NewPatternRedactorWithPatterns(patterns []Pattern) *PatternRedactor {
	return &PatternRedactor{
		patterns: patterns,
		enabled:  true,
	}
}

// AddPattern adds a custom pattern to the redactor
func (r *PatternRedactor) AddPattern(name, pattern, replacement string) error {
	regex, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.patterns = append(r.patterns, Pattern{
		Name:        name,
		Regex:       regex,
		Replacement: replacement,
	})
	return nil
}

// SetEnabled enables or disables redaction
func (r *PatternRedactor) SetEnabled(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enabled = enabled
}

// IsEnabled returns whether redaction is enabled
func (r *PatternRedactor) IsEnabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.enabled
}

// Redact applies all patterns to redact sensitive data
func (r *PatternRedactor) Redact(content string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if !r.enabled {
		return content
	}

	result := content
	for _, pattern := range r.patterns {
		p := pattern
		if p.MayMatch != nil && !p.MayMatch(result) {
			continue
		}
		if p.Validate == nil {
			result = p.Regex.ReplaceAllString(result, p.Replacement)
		} else {
			result = p.Regex.ReplaceAllStringFunc(result, func(m string) string {
				if !p.Validate(m) {
					return m
				}
				return p.Regex.ReplaceAllString(m, p.Replacement)
			})
		}
	}
	return result
}

// RedactMap redacts all string values in a map
func (r *PatternRedactor) RedactMap(data map[string]interface{}) map[string]interface{} {
	if !r.IsEnabled() {
		return data
	}

	result := make(map[string]interface{}, len(data))
	for k, v := range data {
		switch val := v.(type) {
		case string:
			result[k] = r.Redact(val)
		case map[string]interface{}:
			result[k] = r.RedactMap(val)
		case []interface{}:
			result[k] = r.redactSlice(val)
		default:
			result[k] = v
		}
	}
	return result
}

// redactSlice redacts all string values in a slice
func (r *PatternRedactor) redactSlice(data []interface{}) []interface{} {
	result := make([]interface{}, len(data))
	for i, v := range data {
		switch val := v.(type) {
		case string:
			result[i] = r.Redact(val)
		case map[string]interface{}:
			result[i] = r.RedactMap(val)
		case []interface{}:
			result[i] = r.redactSlice(val)
		default:
			result[i] = v
		}
	}
	return result
}

// Config holds redaction configuration
type Config struct {
	Enabled          bool            `yaml:"enabled"`
	RedactPrivateIPs bool            `yaml:"redact_private_ips"`
	CustomPatterns   []PatternConfig `yaml:"patterns"`
}

// PatternConfig represents a custom pattern in config
type PatternConfig struct {
	Name        string `yaml:"name"`
	Pattern     string `yaml:"pattern"`
	Replacement string `yaml:"replacement"`
}

// NewFromConfig creates a Redactor from configuration
func NewFromConfig(cfg Config) (*PatternRedactor, error) {
	r := NewPatternRedactorWithOptions(Options{RedactPrivateIPs: cfg.RedactPrivateIPs})
	r.enabled = cfg.Enabled

	// Add custom patterns
	for _, pc := range cfg.CustomPatterns {
		if err := r.AddPattern(pc.Name, pc.Pattern, pc.Replacement); err != nil {
			return nil, err
		}
	}

	return r, nil
}

// NoopRedactor is a redactor that does nothing (for when redaction is disabled)
type NoopRedactor struct{}

// Redact returns the content unchanged
func (r *NoopRedactor) Redact(content string) string {
	return content
}

// RedactBody returns the body unchanged
func (r *NoopRedactor) RedactBody(body string) string {
	return body
}
