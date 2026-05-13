package instruction

import "time"

// FileType identifies the kind of instruction file.
type FileType int

const (
	FileTypeUnknown FileType = iota
	FileTypeClaudeMD
	FileTypeCursorRules
	FileTypeCursorRulesDir
	FileTypeAgentsMD
	FileTypeWindsurfRules
)

func (ft FileType) String() string {
	switch ft {
	case FileTypeClaudeMD:
		return "claude_md"
	case FileTypeCursorRules:
		return "cursorrules"
	case FileTypeCursorRulesDir:
		return "cursor_rules"
	case FileTypeAgentsMD:
		return "agents_md"
	case FileTypeWindsurfRules:
		return "windsurfrules"
	default:
		return "unknown"
	}
}

// ParseFileType converts a string back to a FileType.
func ParseFileType(s string) FileType {
	switch s {
	case "claude_md":
		return FileTypeClaudeMD
	case "cursorrules":
		return FileTypeCursorRules
	case "cursor_rules":
		return FileTypeCursorRulesDir
	case "agents_md":
		return FileTypeAgentsMD
	case "windsurfrules":
		return FileTypeWindsurfRules
	default:
		return FileTypeUnknown
	}
}

// Confidence indicates how the file was identified.
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"   // Identified by path marker
	ConfidenceMedium Confidence = "medium" // Identified by shape analysis
)

// ScanStatus tracks the integrity status of an instruction file.
type ScanStatus string

const (
	ScanStatusClean   ScanStatus = "clean"
	ScanStatusFlagged ScanStatus = "flagged"
	ScanStatusPending ScanStatus = "pending"
)

// InstructionFile represents an extracted instruction file from a request.
type InstructionFile struct {
	Type       FileType   `json:"type"`
	Content    string     `json:"content"`
	Hash       string     `json:"hash"`
	Confidence Confidence `json:"confidence"`
	SourcePath string     `json:"source_path,omitempty"`
}

// Violation records a rule hit against instruction file content.
type Violation struct {
	RuleName    string `json:"rule_name"`
	Severity    string `json:"severity"`
	Action      string `json:"action"`
	MatchedText string `json:"matched_text"`
}

// ScanResult holds the outcome of scanning an instruction file.
type ScanResult struct {
	Violations  []Violation `json:"violations,omitempty"`
	ShouldBlock bool        `json:"should_block"`
}

// Record is the persistent representation stored in the DB.
type Record struct {
	Hash         string      `json:"hash"`
	FileType     string      `json:"file_type"`
	Confidence   string      `json:"confidence"`
	SourcePath   string      `json:"source_path,omitempty"`
	Content      string      `json:"content"`
	ScanStatus   string      `json:"scan_status"`
	ScanResults  []Violation `json:"scan_results,omitempty"`
	FirstSeen    time.Time   `json:"first_seen"`
	LastSeen     time.Time   `json:"last_seen"`
	SessionCount int         `json:"session_count"`
	PrevHash     string      `json:"prev_hash,omitempty"`
	Diff         string      `json:"diff,omitempty"`
}
