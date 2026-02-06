package unit

import (
	"sync"
	"testing"
	"time"

	"elida/internal/proxy"
)

func TestCaptureBuffer_Capture(t *testing.T) {
	cb := proxy.NewCaptureBuffer(100, 10)

	// Capture a request
	cb.Capture("session-1", proxy.CapturedRequest{
		Timestamp:   time.Now(),
		Method:      "POST",
		Path:        "/v1/chat/completions",
		RequestBody: "hello world",
	})

	if !cb.HasContent("session-1") {
		t.Error("expected session-1 to have content")
	}

	content := cb.GetContent("session-1")
	if len(content) != 1 {
		t.Errorf("expected 1 capture, got %d", len(content))
	}
	if content[0].RequestBody != "hello world" {
		t.Errorf("expected 'hello world', got %q", content[0].RequestBody)
	}
	if content[0].Method != "POST" {
		t.Errorf("expected 'POST', got %q", content[0].Method)
	}

	// After GetContent, session should be empty
	if cb.HasContent("session-1") {
		t.Error("expected session-1 to be empty after GetContent")
	}
}

func TestCaptureBuffer_UpdateLastResponse(t *testing.T) {
	cb := proxy.NewCaptureBuffer(1000, 10)

	// Capture a request
	cb.Capture("session-1", proxy.CapturedRequest{
		Timestamp:   time.Now(),
		Method:      "POST",
		Path:        "/test",
		RequestBody: "request body",
	})

	// Update with response
	cb.UpdateLastResponse("session-1", "response body", 200)

	content := cb.GetContent("session-1")
	if len(content) != 1 {
		t.Fatalf("expected 1 capture, got %d", len(content))
	}
	if content[0].ResponseBody != "response body" {
		t.Errorf("expected 'response body', got %q", content[0].ResponseBody)
	}
	if content[0].StatusCode != 200 {
		t.Errorf("expected status 200, got %d", content[0].StatusCode)
	}
}

func TestCaptureBuffer_Truncation(t *testing.T) {
	maxSize := 20
	cb := proxy.NewCaptureBuffer(maxSize, 10)

	// Capture a request with body larger than max
	longBody := "this is a very long request body that exceeds the limit"
	cb.Capture("session-1", proxy.CapturedRequest{
		Timestamp:   time.Now(),
		Method:      "POST",
		Path:        "/test",
		RequestBody: longBody,
	})

	content := cb.GetContent("session-1")
	if len(content) != 1 {
		t.Fatalf("expected 1 capture, got %d", len(content))
	}

	// Should be truncated
	if len(content[0].RequestBody) > maxSize+len("...[truncated]") {
		t.Errorf("expected truncated body, got length %d", len(content[0].RequestBody))
	}
	if content[0].RequestBody[len(content[0].RequestBody)-len("...[truncated]"):] != "...[truncated]" {
		t.Errorf("expected truncated suffix, got %q", content[0].RequestBody)
	}
}

func TestCaptureBuffer_MaxCapturedPerSession(t *testing.T) {
	maxPerSession := 3
	cb := proxy.NewCaptureBuffer(1000, maxPerSession)

	// Capture more than max
	for i := 0; i < 5; i++ {
		cb.Capture("session-1", proxy.CapturedRequest{
			Timestamp:   time.Now(),
			Method:      "POST",
			Path:        "/test",
			RequestBody: "request",
		})
	}

	content := cb.GetContent("session-1")
	if len(content) != maxPerSession {
		t.Errorf("expected %d captures (max), got %d", maxPerSession, len(content))
	}
}

func TestCaptureBuffer_PeekContent(t *testing.T) {
	cb := proxy.NewCaptureBuffer(1000, 10)

	cb.Capture("session-1", proxy.CapturedRequest{
		Timestamp:   time.Now(),
		Method:      "POST",
		Path:        "/test",
		RequestBody: "request",
	})

	// Peek should return content without removing
	peeked := cb.PeekContent("session-1")
	if len(peeked) != 1 {
		t.Errorf("expected 1 capture from peek, got %d", len(peeked))
	}

	// Content should still be there
	if !cb.HasContent("session-1") {
		t.Error("expected content to still exist after peek")
	}

	// GetContent should return same content
	content := cb.GetContent("session-1")
	if len(content) != 1 {
		t.Errorf("expected 1 capture from get, got %d", len(content))
	}
}

func TestCaptureBuffer_Remove(t *testing.T) {
	cb := proxy.NewCaptureBuffer(1000, 10)

	cb.Capture("session-1", proxy.CapturedRequest{
		Timestamp:   time.Now(),
		Method:      "POST",
		Path:        "/test",
		RequestBody: "request",
	})

	if !cb.HasContent("session-1") {
		t.Error("expected content before remove")
	}

	cb.Remove("session-1")

	if cb.HasContent("session-1") {
		t.Error("expected no content after remove")
	}
}

func TestCaptureBuffer_MultipleSessions(t *testing.T) {
	cb := proxy.NewCaptureBuffer(1000, 10)

	// Capture to different sessions
	cb.Capture("session-1", proxy.CapturedRequest{
		Timestamp:   time.Now(),
		Method:      "POST",
		Path:        "/test",
		RequestBody: "session 1 request",
	})

	cb.Capture("session-2", proxy.CapturedRequest{
		Timestamp:   time.Now(),
		Method:      "GET",
		Path:        "/other",
		RequestBody: "session 2 request",
	})

	// Both should have content
	if !cb.HasContent("session-1") {
		t.Error("expected session-1 to have content")
	}
	if !cb.HasContent("session-2") {
		t.Error("expected session-2 to have content")
	}

	// Get session-1 content
	content1 := cb.GetContent("session-1")
	if len(content1) != 1 {
		t.Errorf("expected 1 capture for session-1, got %d", len(content1))
	}
	if content1[0].RequestBody != "session 1 request" {
		t.Errorf("expected 'session 1 request', got %q", content1[0].RequestBody)
	}

	// Session-2 should still have content
	if !cb.HasContent("session-2") {
		t.Error("expected session-2 to still have content")
	}
}

func TestCaptureBuffer_UpdateLastResponse_NoContent(t *testing.T) {
	cb := proxy.NewCaptureBuffer(1000, 10)

	// Update response without capturing request first - should not panic
	cb.UpdateLastResponse("nonexistent", "response", 200)

	// Should not have content
	if cb.HasContent("nonexistent") {
		t.Error("expected no content for nonexistent session")
	}
}

func TestCaptureBuffer_ConcurrentAccess(t *testing.T) {
	cb := proxy.NewCaptureBuffer(1000, 100)

	var wg sync.WaitGroup
	sessions := 10
	requestsPerSession := 10

	// Concurrent captures
	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func(sessionNum int) {
			defer wg.Done()
			sessionID := "session-" + string(rune('0'+sessionNum))
			for j := 0; j < requestsPerSession; j++ {
				cb.Capture(sessionID, proxy.CapturedRequest{
					Timestamp:   time.Now(),
					Method:      "POST",
					Path:        "/test",
					RequestBody: "request",
				})
			}
		}(i)
	}

	wg.Wait()

	// Verify all sessions have content
	for i := 0; i < sessions; i++ {
		sessionID := "session-" + string(rune('0'+i))
		if !cb.HasContent(sessionID) {
			t.Errorf("expected session %s to have content", sessionID)
		}
		content := cb.GetContent(sessionID)
		if len(content) != requestsPerSession {
			t.Errorf("expected %d captures for %s, got %d", requestsPerSession, sessionID, len(content))
		}
	}
}

func TestCaptureBuffer_ResponseTruncation(t *testing.T) {
	maxSize := 20
	cb := proxy.NewCaptureBuffer(maxSize, 10)

	// Capture a request
	cb.Capture("session-1", proxy.CapturedRequest{
		Timestamp:   time.Now(),
		Method:      "POST",
		Path:        "/test",
		RequestBody: "short",
	})

	// Update with long response
	longResponse := "this is a very long response body that exceeds the limit"
	cb.UpdateLastResponse("session-1", longResponse, 200)

	content := cb.GetContent("session-1")
	if len(content) != 1 {
		t.Fatalf("expected 1 capture, got %d", len(content))
	}

	// Response should be truncated
	if len(content[0].ResponseBody) > maxSize+len("...[truncated]") {
		t.Errorf("expected truncated response, got length %d", len(content[0].ResponseBody))
	}
}

func TestCaptureBuffer_Defaults(t *testing.T) {
	// Test with zero/negative values - should use defaults
	cb := proxy.NewCaptureBuffer(0, 0)

	// Should still work with defaults (10KB, 100 entries)
	cb.Capture("session-1", proxy.CapturedRequest{
		Timestamp:   time.Now(),
		Method:      "POST",
		Path:        "/test",
		RequestBody: "request",
	})

	if !cb.HasContent("session-1") {
		t.Error("expected content with default settings")
	}
}

func TestCaptureBuffer_PeekContent_Empty(t *testing.T) {
	cb := proxy.NewCaptureBuffer(1000, 10)

	// Peek on non-existent session
	peeked := cb.PeekContent("nonexistent")
	if peeked != nil {
		t.Errorf("expected nil for non-existent session, got %v", peeked)
	}
}

func TestCaptureBuffer_MultipleRequestsAndResponses(t *testing.T) {
	cb := proxy.NewCaptureBuffer(1000, 10)

	// Capture first request
	cb.Capture("session-1", proxy.CapturedRequest{
		Timestamp:   time.Now(),
		Method:      "POST",
		Path:        "/first",
		RequestBody: "first request",
	})
	cb.UpdateLastResponse("session-1", "first response", 200)

	// Capture second request
	cb.Capture("session-1", proxy.CapturedRequest{
		Timestamp:   time.Now(),
		Method:      "POST",
		Path:        "/second",
		RequestBody: "second request",
	})
	cb.UpdateLastResponse("session-1", "second response", 201)

	content := cb.GetContent("session-1")
	if len(content) != 2 {
		t.Fatalf("expected 2 captures, got %d", len(content))
	}

	// Verify first request/response
	if content[0].Path != "/first" {
		t.Errorf("expected path '/first', got %q", content[0].Path)
	}
	if content[0].RequestBody != "first request" {
		t.Errorf("expected 'first request', got %q", content[0].RequestBody)
	}
	if content[0].ResponseBody != "first response" {
		t.Errorf("expected 'first response', got %q", content[0].ResponseBody)
	}
	if content[0].StatusCode != 200 {
		t.Errorf("expected status 200, got %d", content[0].StatusCode)
	}

	// Verify second request/response
	if content[1].Path != "/second" {
		t.Errorf("expected path '/second', got %q", content[1].Path)
	}
	if content[1].RequestBody != "second request" {
		t.Errorf("expected 'second request', got %q", content[1].RequestBody)
	}
	if content[1].ResponseBody != "second response" {
		t.Errorf("expected 'second response', got %q", content[1].ResponseBody)
	}
	if content[1].StatusCode != 201 {
		t.Errorf("expected status 201, got %d", content[1].StatusCode)
	}
}
