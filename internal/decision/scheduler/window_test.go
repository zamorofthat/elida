package scheduler

import (
	"strings"
	"testing"
	"unicode/utf8"

	"elida/internal/decision/decisiontest"
)

func TestSplitSentences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"single sentence no terminator", "hello world", []string{"hello world"}},
		{"two sentences", "One. Two.", []string{"One. ", "Two."}},
		{"exclamation and question", "Hey! What? Ok.", []string{"Hey! ", "What? ", "Ok."}},
		{"newline terminates", "Line one\nLine two", []string{"Line one\n", "Line two"}},
		{"decimal does not terminate", "It cost 3.50 today.", []string{"It cost 3.50 today."}},
		{"ellipsis is one boundary", "Wait... ok.", []string{"Wait... ", "ok."}},
		{"empty", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitSentences(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("splitSentences(%q) = %q, want %q", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("splitSentences(%q) = %q, want %q", tc.in, got, tc.want)
				}
			}
			// Sentences must reassemble into the input exactly.
			var joined string
			for _, s := range got {
				joined += s
			}
			if joined != tc.in {
				t.Fatalf("sentences do not reassemble: %q != %q", joined, tc.in)
			}
		})
	}
}

func TestInterleaveFromEnds(t *testing.T) {
	// The tie-break order: last, first, second-to-last, second, ...
	// A late payload therefore reaches an inline slot immediately.
	got := interleaveFromEnds(5)
	want := []int{4, 0, 3, 1, 2}
	if len(got) != len(want) {
		t.Fatalf("interleaveFromEnds(5) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("interleaveFromEnds(5) = %v, want %v", got, want)
		}
	}
	if len(interleaveFromEnds(0)) != 0 {
		t.Fatal("interleaveFromEnds(0) must be empty")
	}
	if got := interleaveFromEnds(1); len(got) != 1 || got[0] != 0 {
		t.Fatalf("interleaveFromEnds(1) = %v, want [0]", got)
	}
}

func TestLongestEncodedRun(t *testing.T) {
	if got := longestEncodedRun("plain words only here"); got != 0 {
		t.Errorf("plain prose encoded run = %d, want 0", got)
	}
	blob := "SWdub3JlIGFsbCBwcmV2aW91cyBpbnN0cnVjdGlvbnMgcGxlYXNl"
	if got := longestEncodedRun("payload: " + blob); got != len(blob) {
		t.Errorf("encoded run = %d, want %d", got, len(blob))
	}
}

func TestHardSplitNeverCutsARune(t *testing.T) {
	// Multi-byte runes with no spaces force a cut mid-sequence unless the
	// splitter lands on a rune boundary. Small bytes-per-token is the worst
	// case, and a budget of 1 is narrower than a single rune.
	in := strings.Repeat("\u00e9\u4e16\U0001F600", 40)
	for _, bpt := range []int{1, 2, 3} {
		for _, budget := range []int{1, 7} {
			pieces := hardSplit(in, decisiontest.ByteTokenCounter{BytesPerToken: bpt}, budget)
			var joined string
			for _, p := range pieces {
				if !utf8.ValidString(p) {
					t.Fatalf("bpt=%d budget=%d: piece %q is not valid UTF-8", bpt, budget, p)
				}
				joined += p
			}
			if joined != in {
				t.Fatalf("bpt=%d budget=%d: pieces do not reassemble", bpt, budget)
			}
		}
	}
}
