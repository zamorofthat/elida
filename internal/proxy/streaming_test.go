package proxy

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsStreamingRequest(t *testing.T) {
	p := &Proxy{}

	tests := []struct {
		name   string
		body   string
		accept string
		want   bool
	}{
		{"no space", `{"stream":true}`, "", true},
		{"one space", `{"stream": true}`, "", true},
		{"tab separated", `{"stream":	true}`, "", true},
		{"extra spaces", `{"stream"  :  true}`, "", true},
		{"stream false", `{"stream":false}`, "", false},
		{"missing field", `{"model":"gpt-4"}`, "", false},
		{"invalid JSON", `not json at all`, "", false},
		{"empty body", ``, "", false},
		{"SSE accept header", ``, "text/event-stream", true},
		{"SSE with body false", `{"stream":false}`, "text/event-stream", true},
		{"nested stream field", `{"config":{"stream":true}}`, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}
			got := p.isStreamingRequest(req, []byte(tt.body))
			if got != tt.want {
				t.Errorf("isStreamingRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestJoinChunks(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   string
	}{
		{"empty", nil, ""},
		{"single", []string{"hello"}, "hello"},
		{"multiple", []string{"hel", "lo ", "world"}, "hello world"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := joinChunks(tt.chunks)
			if got != tt.want {
				t.Errorf("joinChunks() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLogResponse_Truncation(t *testing.T) {
	p := &Proxy{}

	// Should not panic on short body
	p.logResponse("test-sess", []byte("short"))

	// Should not panic on body exceeding logTruncateLen
	longBody := []byte(strings.Repeat("x", logTruncateLen+500))
	p.logResponse("test-sess", longBody)
}

func TestLogStreamingResponse(t *testing.T) {
	p := &Proxy{}

	// SSE path
	p.logStreamingResponse("test-sess", []string{"data: hello\n\n"}, true)

	// NDJSON path
	p.logStreamingResponse("test-sess", []string{`{"response":"hi"}` + "\n"}, false)

	// Empty chunks
	p.logStreamingResponse("test-sess", nil, false)
}
