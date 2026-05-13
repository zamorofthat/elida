package instruction

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

// pathMarkerRegex matches "Contents of <path>" lines injected by AI coding tools.
var pathMarkerRegex = regexp.MustCompile(`(?m)^Contents of ([^\s(]+)(?:\s*\([^)]*\))?:`)

// pathToFileType maps filename patterns to FileType.
var pathToFileType = []struct {
	suffix   string
	fileType FileType
}{
	{"/CLAUDE.md", FileTypeClaudeMD},
	{"/.cursorrules", FileTypeCursorRules},
	{"/.cursor/rules", FileTypeCursorRulesDir},
	{"/AGENTS.md", FileTypeAgentsMD},
	{"/.windsurfrules", FileTypeWindsurfRules},
}

// extractByPathMarker looks for "Contents of /path/to/CLAUDE.md" markers.
func extractByPathMarker(content string) (FileType, string, bool) {
	matches := pathMarkerRegex.FindStringSubmatch(content)
	if matches == nil {
		return FileTypeUnknown, "", false
	}

	path := matches[1]
	for _, mapping := range pathToFileType {
		if strings.HasSuffix(path, mapping.suffix) {
			return mapping.fileType, path, true
		}
	}

	return FileTypeUnknown, path, false
}

// extractInstructionContent extracts the content block after the path marker.
func extractInstructionContent(content string) string {
	loc := pathMarkerRegex.FindStringIndex(content)
	if loc == nil {
		return content
	}
	rest := content[loc[1]:]
	rest = strings.TrimLeft(rest, "\n")
	return rest
}

// hashContent returns the SHA-256 hex digest of the content.
func hashContent(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h)
}

// Extract attempts to identify and extract an instruction file from content.
// It first tries path markers (high confidence), then falls back to shape
// detection (medium confidence) if enabled.
func Extract(content string, shapeDetection bool, shapeThreshold float64) *InstructionFile {
	ft, path, found := extractByPathMarker(content)
	if found && ft != FileTypeUnknown {
		body := extractInstructionContent(content)
		return &InstructionFile{
			Type:       ft,
			Content:    body,
			Hash:       hashContent(body),
			Confidence: ConfidenceHigh,
			SourcePath: path,
		}
	}

	if shapeDetection {
		if ft, score := classifyByShape(content); ft != FileTypeUnknown && score >= shapeThreshold {
			return &InstructionFile{
				Type:       ft,
				Content:    content,
				Hash:       hashContent(content),
				Confidence: ConfidenceMedium,
				SourcePath: path,
			}
		}
	}

	return nil
}

// classifyByShape scores content against instruction file signals.
// Stub — full implementation in Task 3.
func classifyByShape(content string) (FileType, float64) {
	return FileTypeUnknown, 0.0
}
