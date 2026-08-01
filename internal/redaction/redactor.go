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
}

// Pattern represents a redaction pattern
type Pattern struct {
	Name        string
	Regex       *regexp.Regexp
	Replacement string
	Validate    func(match string) bool // nil validates everything (previous behavior)
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

// DefaultPatterns returns the standard set of PII redaction patterns
func DefaultPatterns() []Pattern {
	return defaultPatterns(false)
}

// defaultPatterns returns the standard set of PII redaction patterns with configurable private IP handling
func defaultPatterns(redactPrivateIPs bool) []Pattern {
	return []Pattern{
		{
			Name:        "email",
			Regex:       regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Z|a-z]{2,}\b`),
			Replacement: "[REDACTED_EMAIL]",
		},
		{
			Name:        "ssn",
			Regex:       regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
			Replacement: "[REDACTED_SSN]",
		},
		{
			Name:        "credit_card",
			Regex:       regexp.MustCompile(`\b(?:\d[ -]?){13,16}\b`),
			Replacement: "[REDACTED_CC]",
			Validate: func(m string) bool {
				digits := strings.ReplaceAll(strings.ReplaceAll(m, " ", ""), "-", "")
				return len(digits) >= 13 && len(digits) <= 16 && luhnValid(digits)
			},
		},
		{
			Name:        "phone_us",
			Regex:       regexp.MustCompile(`(?:\+?1[-.\s])?(?:\(\d{3}\)\s?|\b\d{3}[-.])\d{3}[-.]\d{4}\b`),
			Replacement: "[REDACTED_PHONE]",
		},
		{
			Name:        "api_key_bearer",
			Regex:       regexp.MustCompile(`(?i)(bearer\s+)([a-zA-Z0-9_.-]{20,})`),
			Replacement: "$1[REDACTED_TOKEN]",
		},
		{
			Name:        "api_key_sk",
			Regex:       regexp.MustCompile(`(?i)(sk-[a-zA-Z0-9]{20,})`),
			Replacement: "[REDACTED_API_KEY]",
		},
		{
			Name:        "api_key_generic",
			Regex:       regexp.MustCompile(`(?i)(api[_-]?key|secret[_-]?key|auth[_-]?token)[:\s=]["']?([a-zA-Z0-9_.-]{16,})["']?`),
			Replacement: "$1=[REDACTED_KEY]",
		},
		{
			Name:        "password_json",
			Regex:       regexp.MustCompile(`(?i)"(password|passwd|pwd)":\s*"([^"]{4,})"`),
			Replacement: `"$1": "[REDACTED_PASSWORD]"`,
		},
		{
			Name:        "password_field",
			Regex:       regexp.MustCompile(`(?i)(password|passwd|pwd)[\s]*[=:][\s]*["']?([^\s"',}]{4,})["']?`),
			Replacement: "$1=[REDACTED_PASSWORD]",
		},
		{
			Name:        "ip_address",
			Regex:       regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`),
			Replacement: "[REDACTED_IP]",
			Validate: func(m string) bool {
				return redactPrivateIPs || !isPrivateOrLoopbackIP(m)
			},
		},
		{
			Name:        "jwt_token",
			Regex:       regexp.MustCompile(`eyJ[a-zA-Z0-9_-]*\.eyJ[a-zA-Z0-9_-]*\.[a-zA-Z0-9_-]*`),
			Replacement: "[REDACTED_JWT]",
		},
		{
			Name:        "aws_access_key",
			Regex:       regexp.MustCompile(`(?i)(AKIA[0-9A-Z]{16})`),
			Replacement: "[REDACTED_AWS_KEY]",
		},
		{
			Name:        "base64_secret",
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
		if p.Validate == nil {
			result = p.Regex.ReplaceAllString(result, p.Replacement)
		} else {
			// For patterns with validators, use FindAllStringIndex to get positions
			// so we can check context (e.g., preceding digits for phone patterns)
			indices := p.Regex.FindAllStringIndex(result, -1)
			if indices == nil {
				continue
			}
			// Build replacements in reverse order to maintain indices
			for i := len(indices) - 1; i >= 0; i-- {
				start, end := indices[i][0], indices[i][1]
				m := result[start:end]
				// Check context for phone pattern: reject if preceded by a digit
				if p.Name == "phone_us" && start > 0 {
					precedingChar := result[start-1]
					if precedingChar >= '0' && precedingChar <= '9' {
						continue
					}
				}
				// Apply custom validator if present
				if !p.Validate(m) {
					continue
				}
				replacement := p.Regex.ReplaceAllString(m, p.Replacement)
				result = result[:start] + replacement + result[end:]
			}
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
	Enabled        bool            `yaml:"enabled"`
	CustomPatterns []PatternConfig `yaml:"patterns"`
}

// PatternConfig represents a custom pattern in config
type PatternConfig struct {
	Name        string `yaml:"name"`
	Pattern     string `yaml:"pattern"`
	Replacement string `yaml:"replacement"`
}

// NewFromConfig creates a Redactor from configuration
func NewFromConfig(cfg Config) (*PatternRedactor, error) {
	r := &PatternRedactor{
		patterns: DefaultPatterns(),
		enabled:  cfg.Enabled,
	}

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
