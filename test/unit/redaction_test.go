package unit

import (
	"strings"
	"testing"

	"elida/internal/redaction"
)

func TestRedactor_Email(t *testing.T) {
	r := redaction.NewPatternRedactor()

	tests := []struct {
		input    string
		expected string
	}{
		{"Contact: user@example.com", "Contact: [REDACTED_EMAIL]"},
		{"Email: test.user+tag@sub.domain.co.uk", "Email: [REDACTED_EMAIL]"},
		{"No email here", "No email here"},
		{"Multiple: a@b.com and c@d.org", "Multiple: [REDACTED_EMAIL] and [REDACTED_EMAIL]"},
	}

	for _, tt := range tests {
		result := r.Redact(tt.input)
		if result != tt.expected {
			t.Errorf("Redact(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestRedactor_SSN(t *testing.T) {
	r := redaction.NewPatternRedactor()

	tests := []struct {
		input    string
		contains string
	}{
		{"SSN: 123-45-6789", "[REDACTED_SSN]"},
		{"Multiple: 111-22-3333 and 444-55-6666", "[REDACTED_SSN]"},
	}

	for _, tt := range tests {
		result := r.Redact(tt.input)
		if !strings.Contains(result, tt.contains) {
			t.Errorf("Redact(%q) should contain %q, got %q", tt.input, tt.contains, result)
		}
	}
}

func TestRedactor_CreditCard(t *testing.T) {
	r := redaction.NewPatternRedactor()

	input := "Card: 4111 1111 1111 1111"
	result := r.Redact(input)
	if !strings.Contains(result, "[REDACTED_CC]") {
		t.Errorf("expected credit card redaction, got %q", result)
	}
}

func TestRedactor_Phone(t *testing.T) {
	r := redaction.NewPatternRedactor()

	tests := []struct {
		input string
	}{
		{"Call: 555-123-4567"},
		{"Phone: (555) 123-4567"},
		{"Tel: +1-555-123-4567"},
	}

	for _, tt := range tests {
		result := r.Redact(tt.input)
		if !strings.Contains(result, "[REDACTED_PHONE]") {
			t.Errorf("expected phone redaction for %q, got %q", tt.input, result)
		}
	}
}

func TestRedactor_APIKey(t *testing.T) {
	r := redaction.NewPatternRedactor()

	tests := []struct {
		input    string
		contains string
	}{
		{"sk-1234567890abcdefghijklmnop", "[REDACTED_API_KEY]"},
		{"Authorization: Bearer abc123def456ghi789jkl0mn", "[REDACTED_TOKEN]"},
	}

	for _, tt := range tests {
		result := r.Redact(tt.input)
		if !strings.Contains(result, tt.contains) {
			t.Errorf("expected %q in result for %q, got %q", tt.contains, tt.input, result)
		}
	}
}

func TestRedactor_JWT(t *testing.T) {
	r := redaction.NewPatternRedactor()

	input := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"
	result := r.Redact(input)
	if !strings.Contains(result, "[REDACTED_JWT]") {
		t.Errorf("expected JWT redaction, got %q", result)
	}
}

func TestRedactor_Password(t *testing.T) {
	r := redaction.NewPatternRedactor()

	tests := []struct {
		input    string
		contains string
	}{
		{"password: mysecretpass123", "[REDACTED_PASSWORD]"},
		{"passwd=super_secret", "[REDACTED_PASSWORD]"},
		{`{"pwd": "hidden123"}`, "[REDACTED_PASSWORD]"},
	}

	for _, tt := range tests {
		result := r.Redact(tt.input)
		if !strings.Contains(result, tt.contains) {
			t.Errorf("expected %q in result for %q, got %q", tt.contains, tt.input, result)
		}
	}
}

func TestRedactor_IPAddress(t *testing.T) {
	r := redaction.NewPatternRedactor()

	input := "Client IP: 192.168.1.100 connected"
	result := r.Redact(input)
	if !strings.Contains(result, "[REDACTED_IP]") {
		t.Errorf("expected IP redaction, got %q", result)
	}
}

func TestRedactor_AWSKey(t *testing.T) {
	r := redaction.NewPatternRedactor()

	input := "AWS Key: AKIAIOSFODNN7EXAMPLE"
	result := r.Redact(input)
	if !strings.Contains(result, "[REDACTED_AWS_KEY]") {
		t.Errorf("expected AWS key redaction, got %q", result)
	}
}

func TestRedactor_Disabled(t *testing.T) {
	r := redaction.NewPatternRedactor()
	r.SetEnabled(false)

	input := "Email: user@example.com SSN: 123-45-6789"
	result := r.Redact(input)

	// Should return unchanged when disabled
	if result != input {
		t.Errorf("expected unchanged input when disabled, got %q", result)
	}
}

func TestRedactor_CustomPattern(t *testing.T) {
	r := redaction.NewPatternRedactor()

	err := r.AddPattern("customer_id", `CUST-\d{8}`, "[REDACTED_CUSTOMER]")
	if err != nil {
		t.Fatalf("failed to add pattern: %v", err)
	}

	input := "Customer: CUST-12345678"
	result := r.Redact(input)
	if !strings.Contains(result, "[REDACTED_CUSTOMER]") {
		t.Errorf("expected custom pattern redaction, got %q", result)
	}
}

func TestRedactor_RedactMap(t *testing.T) {
	r := redaction.NewPatternRedactor()

	data := map[string]interface{}{
		"email": "user@example.com",
		"ssn":   "123-45-6789",
		"name":  "John Doe",
		"nested": map[string]interface{}{
			"api_key": "sk-abcdefghij1234567890",
		},
		"list": []interface{}{
			"another@email.com",
			"regular text",
		},
	}

	result := r.RedactMap(data)

	// Check email
	if email, ok := result["email"].(string); !ok || email != "[REDACTED_EMAIL]" {
		t.Errorf("expected email redaction, got %v", result["email"])
	}

	// Check SSN
	if ssn, ok := result["ssn"].(string); !ok || ssn != "[REDACTED_SSN]" {
		t.Errorf("expected SSN redaction, got %v", result["ssn"])
	}

	// Check name unchanged
	if name, ok := result["name"].(string); !ok || name != "John Doe" {
		t.Errorf("expected name unchanged, got %v", result["name"])
	}

	// Check nested
	if nested, ok := result["nested"].(map[string]interface{}); ok {
		if apiKey, ok := nested["api_key"].(string); !ok || !strings.Contains(apiKey, "[REDACTED_API_KEY]") {
			t.Errorf("expected nested API key redaction, got %v", nested["api_key"])
		}
	} else {
		t.Error("expected nested map")
	}

	// Check list
	if list, ok := result["list"].([]interface{}); ok {
		if email, ok := list[0].(string); !ok || email != "[REDACTED_EMAIL]" {
			t.Errorf("expected email in list redaction, got %v", list[0])
		}
		if text, ok := list[1].(string); !ok || text != "regular text" {
			t.Errorf("expected regular text unchanged, got %v", list[1])
		}
	} else {
		t.Error("expected list")
	}
}

func TestRedactor_NoopRedactor(t *testing.T) {
	r := &redaction.NoopRedactor{}

	input := "Email: user@example.com SSN: 123-45-6789"
	result := r.Redact(input)

	if result != input {
		t.Errorf("NoopRedactor should return unchanged, got %q", result)
	}
}

func TestRedactor_FromConfig(t *testing.T) {
	cfg := redaction.Config{
		Enabled: true,
		CustomPatterns: []redaction.PatternConfig{
			{
				Name:        "test_pattern",
				Pattern:     `TEST-\d+`,
				Replacement: "[REDACTED_TEST]",
			},
		},
	}

	r, err := redaction.NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create from config: %v", err)
	}

	// Test default pattern
	result := r.Redact("Email: user@example.com")
	if !strings.Contains(result, "[REDACTED_EMAIL]") {
		t.Error("expected default pattern to work")
	}

	// Test custom pattern
	result = r.Redact("ID: TEST-12345")
	if !strings.Contains(result, "[REDACTED_TEST]") {
		t.Errorf("expected custom pattern to work, got %q", result)
	}
}

func TestRedactor_InvalidPattern(t *testing.T) {
	r := redaction.NewPatternRedactor()

	err := r.AddPattern("invalid", "[invalid(regex", "replacement")
	if err == nil {
		t.Error("expected error for invalid regex")
	}
}

func TestRedactor_MultipleMatches(t *testing.T) {
	r := redaction.NewPatternRedactor()

	input := "Contact user@a.com and admin@b.org about SSN 123-45-6789 or call 555-123-4567"
	result := r.Redact(input)

	if strings.Contains(result, "@") {
		t.Error("expected all emails redacted")
	}
	if strings.Contains(result, "123-45-6789") {
		t.Error("expected SSN redacted")
	}
	if strings.Contains(result, "555-123-4567") {
		t.Error("expected phone redacted")
	}
}
