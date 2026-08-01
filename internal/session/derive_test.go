package session

import "testing"

func TestDeriveIDFromBody(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		openaiUser bool
		bodyPath   string
		want       string
	}{
		{"openai user field", `{"model":"gemma","user":"conv-42"}`, true, "", "user-conv-42"},
		{"user disabled", `{"user":"conv-42"}`, false, "", ""},
		{"no user field", `{"model":"gemma"}`, true, "", ""},
		{"empty user", `{"user":""}`, true, "", ""},
		{"non-string user", `{"user":7}`, true, "", ""},
		{"not json", `hello`, true, "", ""},
		{"empty body", ``, true, "", ""},
		{"body path wins over user", `{"user":"u1","metadata":{"conversation_id":"c9"}}`, true, "metadata.conversation_id", "user-c9"},
		{"body path miss falls back to user", `{"user":"u1","metadata":{}}`, true, "metadata.conversation_id", "user-u1"},
		{"body path non-string leaf", `{"metadata":{"conversation_id":123}}`, true, "metadata.conversation_id", ""},
		{"body path through non-object", `{"metadata":"flat"}`, true, "metadata.conversation_id", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveIDFromBody([]byte(tt.body), tt.openaiUser, tt.bodyPath); got != tt.want {
				t.Errorf("DeriveIDFromBody() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Values that are long or contain unsafe characters are hashed, bounded, and stable.
func TestDeriveIDSanitization(t *testing.T) {
	long := `{"user":"` + string(make([]byte, 0)) + `this-value-is-definitely-longer-than-sixty-four-characters-so-it-must-be-hashed-here"}`
	got := DeriveIDFromBody([]byte(long), true, "")
	if len(got) != len("user-")+16 {
		t.Errorf("long value: got %q, want user-<16 hex chars>", got)
	}
	again := DeriveIDFromBody([]byte(long), true, "")
	if got != again {
		t.Errorf("hashing not stable: %q vs %q", got, again)
	}
	unsafe := DeriveIDFromBody([]byte(`{"user":"a b/c"}`), true, "")
	if len(unsafe) != len("user-")+16 {
		t.Errorf("unsafe chars: got %q, want hashed form", unsafe)
	}
}
