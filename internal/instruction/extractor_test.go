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

func TestClassifyByShape(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantAbove float64 // score must be >= this; use -1 to expect low score
	}{
		{
			name:      "obvious instruction file",
			content:   "# Project Instructions\n\n## Code Style\n\n- Use slog for logging\n- Run make fmt before commits\n\n## Rules\n\n- You must always run tests before committing\n- Never modify the database schema without approval\n- This project uses Go modules\n",
			wantAbove: 0.7,
		},
		{
			name:      "regular user message",
			content:   "Can you help me fix this bug? The function returns nil when it should return an error.",
			wantAbove: -1,
		},
		{
			name:      "assistant response",
			content:   "I'll help you fix that. Here's the updated code:\n\n```go\nfunc doThing() error {\n    return fmt.Errorf(\"something went wrong\")\n}\n```\n\nThis should resolve the nil return issue.",
			wantAbove: -1,
		},
		{
			name:      "borderline - has some directive patterns",
			content:   "## Getting Started\n\nThis project is a web server. Run npm install to get started.\nAlways use TypeScript for new files.\n",
			wantAbove: 0.3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, score := classifyByShape(tt.content)
			if tt.wantAbove >= 0 && score < tt.wantAbove {
				t.Errorf("score = %.2f, want >= %.2f", score, tt.wantAbove)
			}
			if tt.wantAbove < 0 && score >= 0.7 {
				t.Errorf("score = %.2f, expected low score for non-instruction content", score)
			}
		})
	}
}

func TestExtract(t *testing.T) {
	tests := []struct {
		name           string
		content        string
		shapeDetection bool
		threshold      float64
		wantNil        bool
		wantType       FileType
		wantConfidence Confidence
	}{
		{
			name:           "path marker found",
			content:        "Contents of /project/CLAUDE.md:\n\n# Rules\nDo things.",
			shapeDetection: false,
			threshold:      0.7,
			wantNil:        false,
			wantType:       FileTypeClaudeMD,
			wantConfidence: ConfidenceHigh,
		},
		{
			name:           "no marker, shape detection off",
			content:        "# Rules\n\nYou must always test. This project uses Go.\n\n## Code Style\n\nNever skip linting. Always run gofmt.",
			shapeDetection: false,
			threshold:      0.7,
			wantNil:        true,
		},
		{
			name:           "no marker, shape detection on, above threshold",
			content:        "# Project Instructions\n\n## Rules\n\nYou must always test. This project uses Go.\n\n## Code Style\n\nNever skip linting. Always run gofmt. Do not use internal/ unless needed.",
			shapeDetection: true,
			threshold:      0.5,
			wantNil:        false,
			wantType:       FileTypeUnknown,
			wantConfidence: ConfidenceMedium,
		},
		{
			name:           "regular message, shape detection on",
			content:        "Can you fix this bug please?",
			shapeDetection: true,
			threshold:      0.7,
			wantNil:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Extract(tt.content, tt.shapeDetection, tt.threshold)
			if tt.wantNil {
				if result != nil {
					t.Errorf("expected nil, got %+v", result)
				}
				return
			}
			if result == nil {
				t.Fatal("expected non-nil result")
			}
			if result.Type != tt.wantType {
				t.Errorf("type = %v, want %v", result.Type, tt.wantType)
			}
			if result.Confidence != tt.wantConfidence {
				t.Errorf("confidence = %v, want %v", result.Confidence, tt.wantConfidence)
			}
			if result.Hash == "" {
				t.Error("expected non-empty hash")
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
