package proxy

import (
	"net/http/httptest"
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
