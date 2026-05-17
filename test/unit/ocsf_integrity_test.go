package unit

import (
	"encoding/json"
	"testing"

	"elida/internal/telemetry"
)

func TestOCSFPolicyDetectionIncludesSDRRoot(t *testing.T) {
	record := telemetry.SessionRecord{
		SessionID:                  "session-sdr-root",
		Backend:                    "anthropic",
		SDRRootHash:                "abc123",
		SDRAlgorithm:               "sha256-merkle-v1",
		SDRCanonicalizationVersion: "elida-sdr-canonical-v1",
		SDREventCount:              3,
	}
	violation := telemetry.Violation{
		RuleName:    "dangerous_tool",
		Description: "test violation",
		Severity:    "critical",
		Action:      "block",
	}

	finding := telemetry.BuildPolicyDetection(record.SessionID, violation, record)
	if finding.Unmapped.SDRRootHash != record.SDRRootHash {
		t.Fatalf("expected SDR root hash %s, got %s", record.SDRRootHash, finding.Unmapped.SDRRootHash)
	}

	data, err := json.Marshal(finding)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatalf("expected valid JSON: %s", data)
	}
}
