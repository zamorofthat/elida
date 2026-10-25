package unit

import (
	"sync"
	"testing"

	"elida/internal/policy"
)

// CaptureRequest and UpdateLastCaptureWithResponseAndStatus read
// captureContent, which ReloadConfig writes; under -race this fails if the
// read happens outside the engine lock. Bounded: a fixed number of
// iterations, no sleeps.
func TestPolicyCapture_ConcurrentWithReloadConfig(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	e.RecordSemanticViolation("sess-cap-reload", decisionViolation("semantic_injection", policy.SeverityWarning, false, 0.6, "dec_cap", 1, "cap"))
	cfg := e.GetConfig()

	const n = 200
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			c := cfg
			c.CaptureContent = i%2 == 0
			e.ReloadConfig(c)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			e.CaptureRequest("sess-cap-reload", policy.CapturedRequest{Method: "POST", Path: "/v1/chat", RequestBody: "req"})
			e.UpdateLastCaptureWithResponseAndStatus("sess-cap-reload", "resp", 200)
			e.UpdateLastCaptureWithResponse("sess-cap-reload", "resp")
		}
	}()
	wg.Wait()

	// Behavior is unchanged: capture on records, capture off does not.
	cfg.CaptureContent = true
	e.ReloadConfig(cfg)
	before := len(e.GetFlaggedSession("sess-cap-reload").CapturedContent)
	e.CaptureRequest("sess-cap-reload", policy.CapturedRequest{RequestBody: "on"})
	e.UpdateLastCaptureWithResponseAndStatus("sess-cap-reload", "on-resp", 201)
	fs := e.GetFlaggedSession("sess-cap-reload")
	if len(fs.CapturedContent) != before+1 {
		t.Fatalf("captures = %d, want %d with capture on", len(fs.CapturedContent), before+1)
	}
	if last := fs.CapturedContent[len(fs.CapturedContent)-1]; last.ResponseBody != "on-resp" || last.StatusCode != 201 {
		t.Fatalf("last capture = %+v, want the response and status recorded", last)
	}
	cfg.CaptureContent = false
	e.ReloadConfig(cfg)
	e.CaptureRequest("sess-cap-reload", policy.CapturedRequest{RequestBody: "off"})
	e.UpdateLastCaptureWithResponseAndStatus("sess-cap-reload", "off-resp", 500)
	fs = e.GetFlaggedSession("sess-cap-reload")
	if len(fs.CapturedContent) != before+1 || fs.CapturedContent[len(fs.CapturedContent)-1].ResponseBody != "on-resp" {
		t.Fatal("capture off must neither record nor update a capture")
	}
}
