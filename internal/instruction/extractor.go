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

// pathMarkerRegex matches "Contents of <path>" lines injected by AI coding
// tools. Group 1 is the path; group 2 is the optional parenthetical label,
// such as "project instructions, checked into the codebase".
var pathMarkerRegex = regexp.MustCompile(`(?m)^Contents of ([^\s(]+)(?:\s*\(([^)]*)\))?:`)

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

// fileTypeForPath returns the tracked instruction file type a path names.
func fileTypeForPath(path string) (FileType, bool) {
	for _, mapping := range pathToFileType {
		if strings.HasSuffix(path, mapping.suffix) {
			return mapping.fileType, true
		}
	}
	return FileTypeUnknown, false
}

// hashContent returns the SHA-256 hex digest of the content.
func hashContent(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h)
}

// Extract returns the first instruction file in content, or nil. See
// ExtractAll.
func Extract(content string, shapeDetection bool, shapeThreshold float64) *InstructionFile {
	if files := ExtractAll(content, shapeDetection, shapeThreshold); len(files) > 0 {
		return files[0]
	}
	return nil
}

// ExtractAll returns every instruction file in one trusted-tag block, in
// document order, or nil when there are none.
//
// A block with no path marker is one candidate, identified by shape detection
// when enabled (medium confidence, no path).
//
// A block with markers is split into one segment per marker; text before the
// first marker is ignored. A segment is tracked, at high confidence, when its
// path names an instruction file or its label says "instructions" (how Claude
// Code labels a file imported by CLAUDE.md). A segment that is not tracked is
// still returned, at medium confidence, when the block also holds a tracked
// marker: a marker-shaped line written into an instruction file must not make
// the text below it invisible. A block whose markers are all untracked is a
// document and yields nothing; shape detection is not run on it.
func ExtractAll(content string, shapeDetection bool, shapeThreshold float64) []*InstructionFile {
	markers := pathMarkerRegex.FindAllStringSubmatchIndex(content, -1)
	if len(markers) == 0 {
		if shapeDetection && classifyByShape(content) >= shapeThreshold {
			return []*InstructionFile{{
				Type:       FileTypeUnknown,
				Content:    content,
				Hash:       hashContent(content),
				Confidence: ConfidenceMedium,
			}}
		}
		return nil
	}

	var files []*InstructionFile
	anyTracked := false
	for i, m := range markers {
		path := content[m[2]:m[3]]
		label := ""
		if m[4] >= 0 {
			label = content[m[4]:m[5]]
		}
		fileType, known := fileTypeForPath(path)
		confidence := ConfidenceMedium
		if known || strings.Contains(strings.ToLower(label), "instructions") {
			confidence = ConfidenceHigh
			// Counted before the empty-body check: a forged marker on the
			// first line of a tracked file leaves that file's segment empty.
			anyTracked = true
		}

		end := len(content)
		if i+1 < len(markers) {
			end = markers[i+1][0]
		}
		// Only leading line breaks are trimmed, so a single-marker block
		// hashes as it did before blocks were split.
		body := strings.TrimLeft(content[m[1]:end], "\r\n")
		if strings.TrimSpace(body) == "" {
			continue
		}
		files = append(files, &InstructionFile{
			Type:       fileType,
			Content:    body,
			Hash:       hashContent(body),
			Confidence: confidence,
			SourcePath: path,
		})
	}
	if !anyTracked {
		return nil
	}
	return files
}

// classifyByShape scores content against instruction file signals.
// Returns a confidence score (0.0-1.0). Shape detection cannot identify
// a specific file type — callers should use FileTypeUnknown.
func classifyByShape(content string) float64 {
	if len(content) < 50 {
		return 0.0
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

	return score
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
