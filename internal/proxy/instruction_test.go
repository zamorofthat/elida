package proxy

import (
	"regexp"
	"testing"
)

func TestExtractTrustedTagContent(t *testing.T) {
	// Compile extraction regex the same way New() does
	tag := "system-reminder"
	pattern := regexp.MustCompile(`(?s)<` + tag + `>(.*?)</` + tag + `>`)
	regexes := []*regexp.Regexp{pattern}

	tests := []struct {
		name    string
		content string
		want    int // number of extracted blocks
	}{
		{
			name:    "single tag",
			content: "<system-reminder>\nContents of /project/CLAUDE.md:\n\n# Rules\n</system-reminder>\n\nHello",
			want:    1,
		},
		{
			name:    "multiple tags",
			content: "<system-reminder>Block 1</system-reminder>\nText\n<system-reminder>Block 2</system-reminder>",
			want:    2,
		},
		{
			name:    "no tags",
			content: "Just a regular message with no tags.",
			want:    0,
		},
		{
			name:    "empty tag",
			content: "<system-reminder></system-reminder>",
			want:    0, // empty content is filtered
		},
		{
			name:    "nested content with newlines",
			content: "<system-reminder>\nContents of /project/CLAUDE.md (project instructions):\n\n# My Project\n\n## Code Style\n\nUse gofmt.\n</system-reminder>",
			want:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractTrustedTagContent(tt.content, regexes)
			if len(result) != tt.want {
				t.Errorf("got %d blocks, want %d", len(result), tt.want)
			}
		})
	}
}

func TestExtractTrustedTagContentValues(t *testing.T) {
	tag := "system-reminder"
	pattern := regexp.MustCompile(`(?s)<` + tag + `>(.*?)</` + tag + `>`)
	regexes := []*regexp.Regexp{pattern}

	content := "<system-reminder>\nContents of /project/CLAUDE.md:\n\n# Rules\nDo good.\n</system-reminder>"
	result := extractTrustedTagContent(content, regexes)
	if len(result) != 1 {
		t.Fatalf("expected 1 block, got %d", len(result))
	}
	if result[0] != "\nContents of /project/CLAUDE.md:\n\n# Rules\nDo good.\n" {
		t.Errorf("unexpected content: %q", result[0])
	}
}
