package session

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestObserveUserAgent_FirstValueWins(t *testing.T) {
	s := NewSession("s1", "b", "127.0.0.1")
	s.ObserveUserAgent("claude-cli/1.2")
	s.ObserveUserAgent("claude-cli/1.2")
	if s.Metadata["user_agent"] != "claude-cli/1.2" {
		t.Fatalf("user_agent = %q", s.Metadata["user_agent"])
	}
	if _, ok := s.Metadata["user_agent_changed"]; ok {
		t.Fatal("same value flagged as changed")
	}
}

func TestObserveUserAgent_ChangeKeepsFirstAndFlags(t *testing.T) {
	s := NewSession("s1", "b", "127.0.0.1")
	s.ObserveUserAgent("claude-cli/1.2")
	s.ObserveUserAgent("curl/8.0")
	if s.Metadata["user_agent"] != "claude-cli/1.2" {
		t.Fatalf("first value replaced: %q", s.Metadata["user_agent"])
	}
	if s.Metadata["user_agent_changed"] != "true" {
		t.Fatal("change not flagged")
	}
}

func TestObserveUserAgent_EmptyIgnored(t *testing.T) {
	s := NewSession("s1", "b", "127.0.0.1")
	s.ObserveUserAgent("")
	s.ObserveUserAgent("  \t\r\n ")
	if _, ok := s.Metadata["user_agent"]; ok {
		t.Fatal("empty header recorded")
	}
	s.ObserveUserAgent("a/1")
	s.ObserveUserAgent("")
	if _, ok := s.Metadata["user_agent_changed"]; ok {
		t.Fatal("a missing header counted as a change")
	}
}

func TestObserveUserAgent_SanitizedAndCapped(t *testing.T) {
	s := NewSession("s1", "b", "127.0.0.1")
	s.ObserveUserAgent("evil\x00/1\x1b[31m" + strings.Repeat("é", 500))
	got := s.Metadata["user_agent"]
	if len(got) > MaxUserAgentLen {
		t.Fatalf("len %d exceeds cap", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncation split a rune")
	}
	for _, r := range got {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("control character %q kept", r)
		}
	}
}
