package session

import (
	"strings"
	"testing"
)

func TestDeriveIDFromBodyMultiplePaths(t *testing.T) {
	const rest, rpc = "message.contextId", "params.message.contextId"
	tests := []struct {
		name       string
		body       string
		openaiUser bool
		paths      []string
		want       string
	}{
		{"a2a rest", `{"message":{"contextId":"ctx-1","parts":[]}}`, false, []string{rest, rpc}, "user-ctx-1"},
		{"a2a json-rpc", `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"contextId":"ctx-1"}}}`, false, []string{rest, rpc}, "user-ctx-1"},
		{"both present, first configured wins", `{"message":{"contextId":"a"},"params":{"message":{"contextId":"b"}}}`, false, []string{rest, rpc}, "user-a"},
		{"both present, order reversed", `{"message":{"contextId":"a"},"params":{"message":{"contextId":"b"}}}`, false, []string{rpc, rest}, "user-b"},
		{"empty leaf falls through", `{"message":{"contextId":""},"params":{"message":{"contextId":"b"}}}`, false, []string{rest, rpc}, "user-b"},
		{"non-string leaf falls through", `{"message":{"contextId":7},"params":{"message":{"contextId":"b"}}}`, false, []string{rest, rpc}, "user-b"},
		{"no path matches, user field used", `{"user":"u1","message":{}}`, true, []string{rest, rpc}, "user-u1"},
		{"no path matches, user disabled", `{"user":"u1","message":{}}`, false, []string{rest, rpc}, ""},
		{"empty entries skipped", `{"message":{"contextId":"ctx-1"}}`, false, []string{"", rest}, "user-ctx-1"},
		{"only empty entries, user disabled", `{"message":{"contextId":"ctx-1"}}`, false, []string{"", ""}, ""},
		{"json-rpc batch array", `[{"params":{"message":{"contextId":"b"}}}]`, true, []string{rest, rpc}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveIDFromBody([]byte(tt.body), tt.openaiUser, tt.paths...); got != tt.want {
				t.Errorf("DeriveIDFromBody() = %q, want %q", got, tt.want)
			}
		})
	}

	unsafe := DeriveIDFromBody([]byte(`{"params":{"message":{"contextId":"a b/c"}}}`), false, rest, rpc)
	if len(unsafe) != len("user-")+16 || strings.Contains(unsafe, "a b") {
		t.Errorf("unsafe value should be hashed, got %q", unsafe)
	}
}
