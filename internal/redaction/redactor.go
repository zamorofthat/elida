// Package redaction provides PII and sensitive data redaction for audit logs.
package redaction

import (
	"regexp"
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
}

// PatternRedactor implements Redactor using regex patterns
type PatternRedactor struct {
	mu       sync.RWMutex
	patterns []Pattern
	enabled  bool
}

// DefaultPatterns returns the standard set of PII redaction patterns
func DefaultPatterns() []Pattern {
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
			Regex:       regexp.MustCompile(`\b(?:\d[ -]*?){13,16}\b`),
			Replacement: "[REDACTED_CC]",
		},
		{
			Name:        "phone_us",
			Regex:       regexp.MustCompile(`\b(?:\+?1[-.\s]?)?\(?\d{3}\)?[-.\s]?\d{3}[-.\s]?\d{4}\b`),
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
	return &PatternRedactor{
		patterns: DefaultPatterns(),
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
		result = pattern.Regex.ReplaceAllString(result, pattern.Replacement)
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
