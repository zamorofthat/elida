package instruction

import (
	"testing"
)

func TestExtractByPathMarker(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantType  FileType
		wantPath  string
		wantFound bool
	}{
		{
			name:      "claude code format",
			content:   "Contents of /Users/aaron/project/CLAUDE.md (project instructions, checked into the codebase):\n\n# My Project\n\n## Rules\nDo things right.",
			wantType:  FileTypeClaudeMD,
			wantPath:  "/Users/aaron/project/CLAUDE.md",
			wantFound: true,
		},
		{
			name:      "cursorrules",
			content:   "Contents of /home/user/repo/.cursorrules:\n\nBe helpful.",
			wantType:  FileTypeCursorRules,
			wantPath:  "/home/user/repo/.cursorrules",
			wantFound: true,
		},
		{
			name:      "cursor rules dir",
			content:   "Contents of /home/user/repo/.cursor/rules:\n\nFollow conventions.",
			wantType:  FileTypeCursorRulesDir,
			wantPath:  "/home/user/repo/.cursor/rules",
			wantFound: true,
		},
		{
			name:      "agents md",
			content:   "Contents of /project/AGENTS.md:\n\n# Agent Instructions",
			wantType:  FileTypeAgentsMD,
			wantPath:  "/project/AGENTS.md",
			wantFound: true,
		},
		{
			name:      "windsurfrules",
			content:   "Contents of /project/.windsurfrules:\n\nWindsurf rules here.",
			wantType:  FileTypeWindsurfRules,
			wantPath:  "/project/.windsurfrules",
			wantFound: true,
		},
		{
			name:      "no marker",
			content:   "Just a regular user message with no instruction file.",
			wantType:  FileTypeUnknown,
			wantPath:  "",
			wantFound: false,
		},
		{
			name:      "claude md nested path",
			content:   "Contents of /Users/aaron/projects/arc/.ai/CLAUDE.md:\n\n# Arc Instructions",
			wantType:  FileTypeClaudeMD,
			wantPath:  "/Users/aaron/projects/arc/.ai/CLAUDE.md",
			wantFound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft, path, found := extractByPathMarker(tt.content)
			if found != tt.wantFound {
				t.Errorf("found = %v, want %v", found, tt.wantFound)
			}
			if ft != tt.wantType {
				t.Errorf("type = %v, want %v", ft, tt.wantType)
			}
			if path != tt.wantPath {
				t.Errorf("path = %q, want %q", path, tt.wantPath)
			}
		})
	}
}

func TestFileTypeString(t *testing.T) {
	tests := []struct {
		ft   FileType
		want string
	}{
		{FileTypeClaudeMD, "claude_md"},
		{FileTypeCursorRules, "cursorrules"},
		{FileTypeCursorRulesDir, "cursor_rules"},
		{FileTypeAgentsMD, "agents_md"},
		{FileTypeWindsurfRules, "windsurfrules"},
		{FileTypeUnknown, "unknown"},
	}
	for _, tt := range tests {
		if got := tt.ft.String(); got != tt.want {
			t.Errorf("FileType(%d).String() = %q, want %q", tt.ft, got, tt.want)
		}
	}
}
