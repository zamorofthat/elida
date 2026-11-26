package proxy

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"elida/internal/policy"
	"elida/internal/session"
)

const changedWarning = "system prompt changed mid-session"

// captureLogs routes the default logger into a buffer for one test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func systemEntries(msgs []policy.MessageToScan) []policy.MessageToScan {
	var out []policy.MessageToScan
	for _, m := range msgs {
		if m.Role == "system" {
			out = append(out, m)
		}
	}
	return out
}

// claudeCodeBody is the shape Claude Code sends: a top-level system prompt
// and role "system" entries inside the message list, in the same request.
func claudeCodeBody(topLevel string, inList ...string) []byte {
	var b strings.Builder
	b.WriteString(`{"system":[{"type":"text","text":"` + topLevel + `"}],"messages":[`)
	for _, s := range inList {
		b.WriteString(`{"role":"system","content":[{"type":"text","text":"` + s + `"}]},`)
	}
	b.WriteString(`{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	return []byte(b.String())
}

func TestScannable_TopLevelAndInListSystemAreEachScannedOnce(t *testing.T) {
	logs := captureLogs(t)
	sess := session.NewSession("s", "b", "c")
	body := claudeCodeBody("top level prompt", "in-list notice")

	first := systemEntries(extractScannableMessages(body, sess, nil))
	if len(first) != 2 || first[0].Index != -1 || first[1].Index != 0 {
		t.Fatalf("first request: want the top-level prompt (index -1) and the in-list entry (index 0), got %#v", first)
	}
	for i := 2; i <= 4; i++ {
		if again := systemEntries(extractScannableMessages(body, sess, nil)); len(again) != 0 {
			t.Fatalf("request %d: an unchanged body must not be rescanned, got %#v", i, again)
		}
	}
	if n := strings.Count(logs.String(), changedWarning); n != 0 {
		t.Fatalf("an unchanged body logged %q %d times", changedWarning, n)
	}
}

func TestScannable_NewInListSystemEntryIsScannedOnceWithoutAChangeWarning(t *testing.T) {
	logs := captureLogs(t)
	sess := session.NewSession("s", "b", "c")
	extractScannableMessages(claudeCodeBody("top", "notice one"), sess, nil)

	grown := claudeCodeBody("top", "notice one", "notice two")
	got := systemEntries(extractScannableMessages(grown, sess, nil))
	if len(got) != 1 || got[0].Index != 1 || got[0].Content != "notice two " {
		t.Fatalf("want only the new entry at index 1, got %#v", got)
	}
	if again := systemEntries(extractScannableMessages(grown, sess, nil)); len(again) != 0 {
		t.Fatalf("the grown body must not be rescanned, got %#v", again)
	}
	if n := strings.Count(logs.String(), changedWarning); n != 0 {
		t.Fatalf("a new in-list entry is not a changed system prompt; logged %d times", n)
	}
}

func TestScannable_ChangedTopLevelPromptIsRescannedAndWarnsOnce(t *testing.T) {
	logs := captureLogs(t)
	sess := session.NewSession("s", "b", "c")
	extractScannableMessages(claudeCodeBody("prompt A", "notice"), sess, nil)

	changed := claudeCodeBody("prompt B", "notice")
	got := systemEntries(extractScannableMessages(changed, sess, nil))
	if len(got) != 1 || got[0].Index != -1 || got[0].Content != "prompt B " {
		t.Fatalf("want only the changed top-level prompt, got %#v", got)
	}
	extractScannableMessages(changed, sess, nil)
	if n := strings.Count(logs.String(), changedWarning); n != 1 {
		t.Fatalf("want one %q warning for one change, got %d", changedWarning, n)
	}
}

// One session carries more than one system prompt when a subagent shares its
// parent's session ID. Alternating between two known prompts is not a change.
func TestScannable_AlternatingKnownTopLevelPromptsAreNotRescanned(t *testing.T) {
	logs := captureLogs(t)
	sess := session.NewSession("s", "b", "c")
	mainThread := claudeCodeBody("main prompt")
	subagent := claudeCodeBody("subagent prompt")
	extractScannableMessages(mainThread, sess, nil)
	extractScannableMessages(subagent, sess, nil)

	for i := 0; i < 3; i++ {
		for name, body := range map[string][]byte{"main": mainThread, "subagent": subagent} {
			if got := systemEntries(extractScannableMessages(body, sess, nil)); len(got) != 0 {
				t.Fatalf("round %d, %s: a prompt already scanned in this session was rescanned: %#v", i, name, got)
			}
		}
	}
	if n := strings.Count(logs.String(), changedWarning); n != 1 {
		t.Fatalf("want one warning, for the first sight of the second prompt, got %d", n)
	}
}

// The OpenAI shape — no top-level system, one role "system" message — keeps
// the behavior it had: scanned once, rescanned and warned about on change.
func TestScannable_OpenAISystemMessageKeepsItsChangeWarning(t *testing.T) {
	logs := captureLogs(t)
	sess := session.NewSession("s", "b", "c")
	body := func(sys string) []byte {
		return []byte(`{"messages":[{"role":"system","content":"` + sys + `"},{"role":"user","content":"hi"}]}`)
	}
	if got := systemEntries(extractScannableMessages(body("be brief"), sess, nil)); len(got) != 1 || got[0].Index != 0 {
		t.Fatalf("first request: want the system message at index 0, got %#v", got)
	}
	if got := systemEntries(extractScannableMessages(body("be brief"), sess, nil)); len(got) != 0 {
		t.Fatalf("unchanged system message rescanned: %#v", got)
	}
	if n := strings.Count(logs.String(), changedWarning); n != 0 {
		t.Fatalf("unchanged system message warned %d times", n)
	}
	if got := systemEntries(extractScannableMessages(body("be verbose"), sess, nil)); len(got) != 1 {
		t.Fatalf("changed system message: want one rescan, got %#v", got)
	}
	if n := strings.Count(logs.String(), changedWarning); n != 1 {
		t.Fatalf("want one warning for the change, got %d", n)
	}
}
