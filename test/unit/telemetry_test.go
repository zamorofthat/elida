package unit

import (
	"context"
	"testing"

	"elida/internal/telemetry"
)

// ============================================================
// Provider Tests
// ============================================================

func TestNewProvider_Disabled(t *testing.T) {
	cfg := telemetry.Config{
		Enabled: false,
	}

	provider, err := telemetry.NewProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if provider == nil {
		t.Fatal("provider should not be nil even when disabled")
	}

	if provider.Enabled() {
		t.Error("disabled provider should return Enabled() = false")
	}

	// Tracer should still be available (noop)
	if provider.Tracer() == nil {
		t.Error("tracer should not be nil even when disabled")
	}
}

func TestNewProvider_StdoutExporter(t *testing.T) {
	cfg := telemetry.Config{
		Enabled:     true,
		Exporter:    "stdout",
		ServiceName: "elida-test",
	}

	provider, err := telemetry.NewProvider(cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer func() { _ = provider.Shutdown(context.Background()) }()

	if !provider.Enabled() {
		t.Error("provider should be enabled with stdout exporter")
	}

	if provider.Tracer() == nil {
		t.Error("tracer should not be nil")
	}
}

func TestNewProvider_NoneExporter(t *testing.T) {
	cfg := telemetry.Config{
		Enabled:  true,
		Exporter: "none",
	}

	provider, err := telemetry.NewProvider(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// "none" exporter should result in disabled provider
	if provider.Enabled() {
		t.Error("provider with 'none' exporter should not be enabled")
	}
}

func TestNewProvider_DefaultServiceName(t *testing.T) {
	cfg := telemetry.Config{
		Enabled:     true,
		Exporter:    "stdout",
		ServiceName: "", // Empty = should default to "elida"
	}

	provider, err := telemetry.NewProvider(cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer func() { _ = provider.Shutdown(context.Background()) }()

	// Provider should work with default service name
	if !provider.Enabled() {
		t.Error("provider should be enabled")
	}
}

// ============================================================
// NoopProvider Tests
// ============================================================

func TestNoopProvider(t *testing.T) {
	provider := telemetry.NoopProvider()

	if provider.Enabled() {
		t.Error("noop provider should not be enabled")
	}

	if provider.Tracer() == nil {
		t.Error("noop provider should still have a tracer")
	}

	// Should not panic on shutdown
	err := provider.Shutdown(context.Background())
	if err != nil {
		t.Errorf("noop provider shutdown should not error: %v", err)
	}
}

// ============================================================
// ExportSessionRecord Tests
// ============================================================

func TestExportSessionRecord_Disabled(t *testing.T) {
	provider := telemetry.NoopProvider()

	record := telemetry.SessionRecord{
		SessionID:    "test-session",
		State:        "killed",
		Backend:      "anthropic",
		ClientAddr:   "127.0.0.1:12345",
		DurationMs:   1000,
		RequestCount: 5,
		BytesIn:      500,
		BytesOut:     1500,
		Violations: []telemetry.Violation{
			{
				RuleName:    "test_rule",
				Description: "Test violation",
				Severity:    "warning",
				MatchedText: "test",
				Action:      "flag",
			},
		},
		CaptureCount: 1,
	}

	// Should not panic when disabled
	provider.ExportSessionRecord(context.Background(), record)
}

func TestExportSessionRecord_WithStdout(t *testing.T) {
	cfg := telemetry.Config{
		Enabled:     true,
		Exporter:    "stdout",
		ServiceName: "elida-test",
	}

	provider, err := telemetry.NewProvider(cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer func() { _ = provider.Shutdown(context.Background()) }()

	record := telemetry.SessionRecord{
		SessionID:    "test-session-123",
		State:        "killed",
		Backend:      "anthropic",
		ClientAddr:   "10.0.0.5:54321",
		DurationMs:   5000,
		RequestCount: 10,
		BytesIn:      2048,
		BytesOut:     8192,
		Violations: []telemetry.Violation{
			{
				RuleName:    "prompt_injection",
				Description: "LLM01: Prompt injection detected",
				Severity:    "critical",
				MatchedText: "ignore all previous instructions",
				Action:      "block",
			},
			{
				RuleName:    "pii_ssn",
				Description: "PII: SSN pattern detected",
				Severity:    "warning",
				MatchedText: "123-45-6789",
				Action:      "flag",
			},
		},
		CaptureCount: 3,
	}

	// Should not panic - actually exports the span
	provider.ExportSessionRecord(context.Background(), record)
}

func TestExportSessionRecord_NoViolations(t *testing.T) {
	cfg := telemetry.Config{
		Enabled:     true,
		Exporter:    "stdout",
		ServiceName: "elida-test",
	}

	provider, err := telemetry.NewProvider(cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer func() { _ = provider.Shutdown(context.Background()) }()

	record := telemetry.SessionRecord{
		SessionID:    "clean-session",
		State:        "completed",
		Backend:      "openai",
		ClientAddr:   "192.168.1.100:8080",
		DurationMs:   30000,
		RequestCount: 25,
		BytesIn:      10240,
		BytesOut:     51200,
		Violations:   nil, // No violations
		CaptureCount: 0,
	}

	// Should not panic with empty violations
	provider.ExportSessionRecord(context.Background(), record)
}

// ============================================================
// SessionRecord Tests
// ============================================================

func TestSessionRecord_Struct(t *testing.T) {
	record := telemetry.SessionRecord{
		SessionID:    "sess-123",
		State:        "killed",
		Backend:      "anthropic",
		ClientAddr:   "127.0.0.1:9999",
		DurationMs:   1500,
		RequestCount: 3,
		BytesIn:      256,
		BytesOut:     1024,
		Violations: []telemetry.Violation{
			{
				RuleName:    "test_rule",
				Description: "Test desc",
				Severity:    "critical",
				MatchedText: "match",
				Action:      "block",
			},
		},
		CaptureCount: 1,
	}

	if record.SessionID != "sess-123" {
		t.Error("SessionID mismatch")
	}
	if record.State != "killed" {
		t.Error("State mismatch")
	}
	if record.Backend != "anthropic" {
		t.Error("Backend mismatch")
	}
	if record.ClientAddr != "127.0.0.1:9999" {
		t.Error("ClientAddr mismatch")
	}
	if record.DurationMs != 1500 {
		t.Error("DurationMs mismatch")
	}
	if record.RequestCount != 3 {
		t.Error("RequestCount mismatch")
	}
	if record.BytesIn != 256 {
		t.Error("BytesIn mismatch")
	}
	if record.BytesOut != 1024 {
		t.Error("BytesOut mismatch")
	}
	if record.CaptureCount != 1 {
		t.Error("CaptureCount mismatch")
	}
	if len(record.Violations) != 1 {
		t.Error("Violations count mismatch")
	}
	if record.Violations[0].Severity != "critical" {
		t.Error("Violation severity mismatch")
	}
}

func TestViolation_Struct(t *testing.T) {
	v := telemetry.Violation{
		RuleName:    "prompt_injection_ignore",
		Description: "LLM01: Prompt injection - instruction override",
		Severity:    "critical",
		MatchedText: "ignore all previous instructions",
		Action:      "block",
	}

	if v.RuleName != "prompt_injection_ignore" {
		t.Error("RuleName mismatch")
	}
	if v.Description != "LLM01: Prompt injection - instruction override" {
		t.Error("Description mismatch")
	}
	if v.Severity != "critical" {
		t.Error("Severity mismatch")
	}
	if v.MatchedText != "ignore all previous instructions" {
		t.Error("MatchedText mismatch")
	}
	if v.Action != "block" {
		t.Error("Action mismatch")
	}
}

// ============================================================
// StartRequestSpan / EndRequestSpan Tests
// ============================================================

func TestStartRequestSpan(t *testing.T) {
	cfg := telemetry.Config{
		Enabled:     true,
		Exporter:    "stdout",
		ServiceName: "elida-test",
	}

	provider, err := telemetry.NewProvider(cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer func() { _ = provider.Shutdown(context.Background()) }()

	ctx := context.Background()
	ctx, span := provider.StartRequestSpan(ctx, "test-session", "POST", "/v1/chat/completions", true)

	if span == nil {
		t.Fatal("span should not be nil")
	}

	// Span should be recording
	if !span.IsRecording() {
		t.Error("span should be recording")
	}

	// End the span
	provider.EndRequestSpan(span, 200, 500, 1500, nil)

	// Context should have span
	if telemetry.SpanFromContext(ctx) == nil {
		t.Error("context should contain span")
	}
}

func TestEndRequestSpan_WithError(t *testing.T) {
	cfg := telemetry.Config{
		Enabled:     true,
		Exporter:    "stdout",
		ServiceName: "elida-test",
	}

	provider, err := telemetry.NewProvider(cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer func() { _ = provider.Shutdown(context.Background()) }()

	ctx := context.Background()
	_, span := provider.StartRequestSpan(ctx, "test-session", "POST", "/api/generate", false)

	testErr := context.DeadlineExceeded
	provider.EndRequestSpan(span, 504, 100, 0, testErr)

	// Should not panic with error
}

// ============================================================
// RecordSessionCreated Tests
// ============================================================

func TestRecordSessionCreated(t *testing.T) {
	cfg := telemetry.Config{
		Enabled:     true,
		Exporter:    "stdout",
		ServiceName: "elida-test",
	}

	provider, err := telemetry.NewProvider(cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer func() { _ = provider.Shutdown(context.Background()) }()

	ctx := context.Background()
	ctx, span := provider.StartRequestSpan(ctx, "new-session", "POST", "/test", false)

	// Should not panic
	provider.RecordSessionCreated(ctx, "new-session", "anthropic", "10.0.0.5:12345")

	span.End()
}

// ============================================================
// RecordSessionEnded Tests
// ============================================================

func TestRecordSessionEnded(t *testing.T) {
	cfg := telemetry.Config{
		Enabled:     true,
		Exporter:    "stdout",
		ServiceName: "elida-test",
	}

	provider, err := telemetry.NewProvider(cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer func() { _ = provider.Shutdown(context.Background()) }()

	ctx := context.Background()

	// Should not panic
	provider.RecordSessionEnded(ctx, "ended-session", "completed", "openai", "192.168.1.1:8080", 60000, 15, 4096, 16384)
}

// ============================================================
// RecordSessionKilled Tests
// ============================================================

func TestRecordSessionKilled(t *testing.T) {
	cfg := telemetry.Config{
		Enabled:     true,
		Exporter:    "stdout",
		ServiceName: "elida-test",
	}

	provider, err := telemetry.NewProvider(cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer func() { _ = provider.Shutdown(context.Background()) }()

	ctx := context.Background()
	ctx, span := provider.StartRequestSpan(ctx, "kill-session", "POST", "/test", false)

	// Should not panic
	provider.RecordSessionKilled(ctx, "kill-session")

	span.End()
}

// ============================================================
// Config Tests
// ============================================================

func TestDefaultConfig(t *testing.T) {
	cfg := telemetry.DefaultConfig()

	if cfg.Enabled {
		t.Error("default config should have Enabled = false")
	}
	if cfg.Exporter != "none" {
		t.Errorf("default exporter should be 'none', got %s", cfg.Exporter)
	}
	if cfg.ServiceName != "elida" {
		t.Errorf("default service name should be 'elida', got %s", cfg.ServiceName)
	}
}

func TestConfigFromEnv_NoEnvSet(t *testing.T) {
	// This test relies on env vars not being set
	// In a real test, you'd mock the environment
	cfg := telemetry.ConfigFromEnv()

	// Should return default values when no env vars set
	if cfg.ServiceName != "elida" {
		t.Errorf("expected default service name 'elida', got %s", cfg.ServiceName)
	}
}

// ============================================================
// Shutdown Tests
// ============================================================

func TestProvider_Shutdown(t *testing.T) {
	cfg := telemetry.Config{
		Enabled:     true,
		Exporter:    "stdout",
		ServiceName: "elida-test",
	}

	provider, err := telemetry.NewProvider(cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	// Export something first
	provider.ExportSessionRecord(context.Background(), telemetry.SessionRecord{
		SessionID: "shutdown-test",
		State:     "completed",
	})

	// Shutdown should work without error
	err = provider.Shutdown(context.Background())
	if err != nil {
		t.Errorf("shutdown error: %v", err)
	}
}

func TestProvider_ShutdownWhenDisabled(t *testing.T) {
	cfg := telemetry.Config{
		Enabled: false,
	}

	provider, err := telemetry.NewProvider(cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	// Shutdown on disabled provider should not error
	err = provider.Shutdown(context.Background())
	if err != nil {
		t.Errorf("shutdown on disabled provider should not error: %v", err)
	}
}

// ============================================================
// SpanFromContext Tests
// ============================================================

func TestSpanFromContext_Empty(t *testing.T) {
	ctx := context.Background()
	span := telemetry.SpanFromContext(ctx)

	// Should return a noop span, not nil
	if span == nil {
		t.Error("SpanFromContext should return a span even for empty context")
	}
}

func TestSpanFromContext_WithSpan(t *testing.T) {
	cfg := telemetry.Config{
		Enabled:     true,
		Exporter:    "stdout",
		ServiceName: "elida-test",
	}

	provider, err := telemetry.NewProvider(cfg)
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer func() { _ = provider.Shutdown(context.Background()) }()

	ctx := context.Background()
	ctx, expectedSpan := provider.StartRequestSpan(ctx, "test", "GET", "/", false)

	retrievedSpan := telemetry.SpanFromContext(ctx)
	if retrievedSpan != expectedSpan {
		t.Error("SpanFromContext should return the span from context")
	}

	expectedSpan.End()
}

// ============================================================
// ContextWithTimeout Tests
// ============================================================

func TestContextWithTimeout(t *testing.T) {
	ctx, cancel := telemetry.ContextWithTimeout(100)
	defer cancel()

	if ctx == nil {
		t.Error("context should not be nil")
	}

	// Verify context has deadline
	_, ok := ctx.Deadline()
	if !ok {
		t.Error("context should have a deadline")
	}
}

// ============================================================
// Attribute Constants Tests
// ============================================================

func TestAttributeConstants(t *testing.T) {
	// Verify attribute constants are defined
	attrs := map[string]string{
		"AttrSessionID":         telemetry.AttrSessionID,
		"AttrSessionState":      telemetry.AttrSessionState,
		"AttrBackend":           telemetry.AttrBackend,
		"AttrClientAddr":        telemetry.AttrClientAddr,
		"AttrBytesIn":           telemetry.AttrBytesIn,
		"AttrBytesOut":          telemetry.AttrBytesOut,
		"AttrRequestCount":      telemetry.AttrRequestCount,
		"AttrDurationMs":        telemetry.AttrDurationMs,
		"AttrViolationCount":    telemetry.AttrViolationCount,
		"AttrViolationRules":    telemetry.AttrViolationRules,
		"AttrViolationSeverity": telemetry.AttrViolationSeverity,
		"AttrViolationActions":  telemetry.AttrViolationActions,
		"AttrCaptureCount":      telemetry.AttrCaptureCount,
	}

	for name, value := range attrs {
		if value == "" {
			t.Errorf("attribute constant %s should not be empty", name)
		}
		// Check prefix convention
		if name != "AttrRequestMethod" && name != "AttrRequestPath" && name != "AttrResponseCode" && name != "AttrStreaming" {
			if len(value) > 0 && value[:5] != "elida" {
				t.Logf("note: %s = %s (consider 'elida.' prefix for consistency)", name, value)
			}
		}
	}
}
