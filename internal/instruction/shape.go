package instruction

import (
	"regexp"
	"strings"
)

// Shape detection answers one question about text that arrives with no path:
// is it directing an agent? It is a hand-tuned, English-only heuristic that
// decides what gets tracked at medium confidence. A low score never means the
// text is safe.

const (
	// shapeMinBytes is the content length below which nothing is scored.
	shapeMinBytes = 50
	// shapeMinProseWords is the floor below which densities are noise: one
	// "never" in a ten-word message is a 10-per-100 directive rate.
	shapeMinProseWords = 40
	// selfIDWindowBytes bounds self-identification to the top of the text,
	// where a file says what it is. A document that mentions CLAUDE.md
	// further down is talking about one, not being one.
	selfIDWindowBytes = 600
)

var (
	// replayPrefixRe is the line-number prefix a file-read tool adds.
	replayPrefixRe = regexp.MustCompile(`^\s*\d+(?:\t|→)`)
	fenceRe        = regexp.MustCompile("^\\s*(?:```|~~~)")
	listMarkerRe   = regexp.MustCompile("^(?:[-*+>]|\\d+[.)])\\s+(?:\\[[ xX]\\]\\s+)?")
	leadPunctRe    = regexp.MustCompile("^[*_`\"'(\\[]+")
	mdHeadingRe    = regexp.MustCompile(`^#{1,6}\s`)

	directiveRe = regexp.MustCompile(`(?i)\b(?:must|never|always|do\s+not|don't|should|shall|avoid|prefer|ensure|make\s+sure|only\s+when|unless|instead\s+of|rather\s+than)\b`)
	theUserRe   = regexp.MustCompile(`(?i)\bthe\s+user(?:'s)?\b`)
	agentRe     = regexp.MustCompile(`(?i)\b(?:assistant|agents?|subagents?|claude|codex|copilot|tool\s+calls?)\b`)
	// emphasisRe is case-sensitive: capitals are the signal.
	emphasisRe = regexp.MustCompile(`\b(?:IMPORTANT|MUST|NEVER|ALWAYS|CRITICAL|DO NOT|REQUIRED)\b`)

	selfIDRe = regexp.MustCompile(`(?i)\b(?:CLAUDE|AGENTS|GEMINI)\.md\b|\bguidance\s+(?:to|for)\s+(?:claude|codex|copilot|gemini|ai|coding|agents?)\b|\binstructions?\s+for\s+(?:ai|agents?|claude|codex|copilot|llms?|coding\s+agents?|assistants?)\b|\bagent\s+(?:guide|instructions|guidelines)\b|\brepository\s+guidelines\b|\bwhen\s+working\s+(?:with|in|on)\s+(?:code\s+in\s+)?this\s+(?:repo|repository|codebase|project)\b`)
)

// imperativeVerbs are the words that open a line addressed to its reader.
var imperativeVerbs = func() map[string]bool {
	verbs := make(map[string]bool)
	for _, verb := range strings.Fields(`use run add keep write read check follow prefer avoid never always do don't make ensure start treat put create update include verify test call return set ask report stop begin record pick give tell leave remove fix commit push build install execute invoke respond answer wait refer look search handle pass reuse mark name place retrieve see note consider try apply state say explain describe list show provide confirm save load open close send fetch find identify determine choose select review think remember be let limit match wrap format cite link quote default announce complete finish continue skip ignore reject refuse decline delete replace edit move copy rename define declare implement document mention summarize validate assume respect honor preserve maintain focus work go get take only`) {
		verbs[verb] = true
	}
	return verbs
}()

// shapeProse is the part of a text the address signals read: its lines with
// replay line numbers, fenced code and table rows removed.
type shapeProse struct {
	lines []string // trimmed, non-empty, headings included
	text  string   // lines joined with "\n"
	words int
}

func proseOf(content string) shapeProse {
	var prose shapeProse
	inFence := false
	for _, raw := range strings.Split(content, "\n") {
		raw = replayPrefixRe.ReplaceAllString(raw, "")
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if fenceRe.MatchString(raw) {
			inFence = !inFence
			continue
		}
		if inFence || strings.HasPrefix(line, "|") {
			continue
		}
		prose.lines = append(prose.lines, line)
	}
	prose.text = strings.Join(prose.lines, "\n")
	prose.words = len(strings.Fields(prose.text))
	return prose
}

// per100Words is how often re matches per hundred prose words.
func per100Words(re *regexp.Regexp) func(shapeProse) float64 {
	return func(prose shapeProse) float64 {
		return 100 * float64(len(re.FindAllStringIndex(prose.text, -1))) / float64(prose.words)
	}
}

// imperativeFraction is the share of lines that open with an imperative verb.
// Headings and lines of fewer than three words are not counted either way.
func imperativeFraction(prose shapeProse) float64 {
	eligible, imperative := 0, 0
	for _, line := range prose.lines {
		if mdHeadingRe.MatchString(line) {
			continue
		}
		line = leadPunctRe.ReplaceAllString(listMarkerRe.ReplaceAllString(line, ""), "")
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		eligible++
		if imperativeVerbs[strings.ToLower(strings.TrimRight(fields[0], ":,.*_`"))] {
			imperative++
		}
	}
	if eligible == 0 {
		return 0
	}
	return float64(imperative) / float64(eligible)
}

// addressSignal is one measurement of how a text addresses its reader. It
// contributes its full weight once the measurement reaches saturatesAt.
type addressSignal struct {
	weight      float64
	saturatesAt float64
	measure     func(shapeProse) float64
}

// addressSignals deliberately has no weight that reaches the default
// threshold alone, and no signal about what the text is about: project,
// tooling and file-path vocabulary describe documentation as well as they
// describe instructions.
var addressSignals = []addressSignal{
	{weight: 0.45, saturatesAt: 3, measure: per100Words(directiveRe)},
	{weight: 0.65, saturatesAt: 1, measure: per100Words(theUserRe)},
	{weight: 0.50, saturatesAt: 3, measure: per100Words(agentRe)},
	{weight: 0.45, saturatesAt: 0.5, measure: per100Words(emphasisRe)},
	{weight: 0.35, saturatesAt: 0.4, measure: imperativeFraction},
}

// addressScore is how strongly content reads as text directing an agent,
// from 0 to 1. Content with too little prose to judge scores 0.
func addressScore(content string) float64 {
	prose := proseOf(content)
	if prose.words < shapeMinProseWords {
		return 0
	}
	var score float64
	for _, sig := range addressSignals {
		score += sig.weight * min(sig.measure(prose)/sig.saturatesAt, 1)
	}
	return min(score, 1)
}

// selfIdentifies reports whether content names itself as agent instructions
// near its top.
func selfIdentifies(content string) bool {
	if len(content) > selfIDWindowBytes {
		content = content[:selfIDWindowBytes]
	}
	return selfIDRe.MatchString(content)
}

// classifyByShape scores content with no path marker, from 0 to 1. Text that
// names itself as agent instructions scores 1; anything else scores by how it
// addresses its reader. Shape detection cannot identify a file type, so
// callers use FileTypeUnknown.
func classifyByShape(content string) float64 {
	if len(content) < shapeMinBytes {
		return 0
	}
	if selfIdentifies(content) {
		return 1
	}
	return addressScore(content)
}
