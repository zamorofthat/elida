package instruction

import (
	"testing"
)

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
