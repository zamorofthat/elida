package instruction

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

// pathMarkerRegex matches "Contents of <path>" lines injected by AI coding
// tools, with an optional parenthetical label such as "project instructions,
// checked into the codebase". It has two alternatives; markerParts reads
// whichever matched.
//
// The first reads a path that may hold spaces and parentheses ("/Users/j/My
// Projects/app (copy)/CLAUDE.md"). To tell such a path from a sentence it
// must start like a path, hold no further colon, and the marker must end its
// line. The second is every other marker: a path with no space in it,
// wherever on the line the colon falls.
var pathMarkerRegex = regexp.MustCompile(`(?m)^Contents of (?:` +
	`((?:/|~|[A-Za-z]:[\\/])[^:\n]*?)(?:[ \t]*\(((?:[^()\n]|\([^()\n]*\))*)\))?:[ \t]*\r?$` +
	`|` +
	`([^\s(]+)(?:\s*\(([^)]*)\))?:` +
	`)`)

// markerParts returns the path and label one pathMarkerRegex match captured.
func markerParts(content string, m []int) (path, label string) {
	pathAt, labelAt := 2, 4
	if m[pathAt] < 0 {
		pathAt, labelAt = 6, 8
	}
	path = content[m[pathAt]:m[pathAt+1]]
	if m[labelAt] >= 0 {
		label = content[m[labelAt]:m[labelAt+1]]
	}
	return path, label
}

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
// first marker is ignored. A segment is tracked when its path names an
// instruction file or its label says "instructions" (how Claude Code labels a
// file imported by CLAUDE.md). A block with at least one tracked segment is
// an instruction block, and every segment in it is returned at high
// confidence under its own path, tracked or not. Whoever can write a tracked
// file can write a marker-shaped line into it, so a marker must never make
// the text below it invisible or less enforced than the file it sits in. A
// block whose markers are all untracked is a document and yields nothing;
// shape detection is not run on it.
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
		path, label := markerParts(content, m)
		fileType, known := fileTypeForPath(path)
		if known || strings.Contains(strings.ToLower(label), "instructions") {
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
			Confidence: ConfidenceHigh,
			SourcePath: path,
		})
	}
	if !anyTracked {
		return nil
	}
	return files
}
