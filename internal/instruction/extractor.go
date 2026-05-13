package instruction

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

// Shape detection signal regexes — compiled once at package init.
var (
	directiveHeadingRe = regexp.MustCompile(`(?mi)^#{1,3}\s+(rules?|style\s+guide|conventions?|do\s+not|instructions?|constraints?|requirements?)`)
	imperativeRe       = regexp.MustCompile(`(?i)\b(you\s+must|you\s+should|always|never|do\s+not|don't|ensure\s+that|make\s+sure)\b`)
	projectContextRe   = regexp.MustCompile(`(?i)\b(this\s+project|this\s+repo|this\s+codebase|our\s+codebase|the\s+project)\b`)
	codeConventionRe   = regexp.MustCompile(`(?i)\b(code\s+style|naming\s+convention|file\s+structure|import\s+order|lint|format|gofmt|eslint|prettier)\b`)
	filePathRefRe      = regexp.MustCompile(`(?m)(internal/|src/|cmd/|test/|\.go|\.ts|\.py|\.js|Makefile|package\.json)`)
)

type shapeSignal struct {
	re     *regexp.Regexp
	weight float64
}

var shapeSignals = []shapeSignal{
	{directiveHeadingRe, 0.25},
	{imperativeRe, 0.20},
	{projectContextRe, 0.20},
	{codeConventionRe, 0.20},
	{filePathRefRe, 0.15},
}

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
		if ft, score := classifyByShape(content); score >= shapeThreshold {
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

func classifyByShape(content string) (FileType, float64) {
	if len(content) < 50 {
		return FileTypeUnknown, 0.0
	}

	var score float64
	for _, sig := range shapeSignals {
		matches := sig.re.FindAllString(content, -1)
		if len(matches) > 0 {
			count := float64(len(matches))
			boost := sig.weight
			if count > 1 {
				boost += sig.weight * 0.2 * minFloat(count-1, 4)
			}
			score += boost
		}
	}

	if score > 1.0 {
		score = 1.0
	}

	return FileTypeUnknown, score
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
