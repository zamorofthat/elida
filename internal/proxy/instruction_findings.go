package proxy

import (
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"elida/internal/instruction"
	"elida/internal/policy"
)

// categoryInstructionIntegrity is the event category of an instruction
// rule hit scored from a file's content.
const categoryInstructionIntegrity = "instruction_integrity"

// instructionToolUses returns the tool calls among calls (the request's
// ExtractToolCalls result), as possible causes of an instruction file change.
// A tool_use block is a call; any other entry is a call only when it carries
// an ID or arguments. An entry with neither is a tool definition (an OpenAI
// "tools" entry), which names a tool and calls nothing.
func instructionToolUses(calls []ToolCallInfo) []instruction.ToolUse {
	var uses []instruction.ToolUse
	for _, c := range calls {
		if c.Type != "tool_use" && c.ID == "" && c.Arguments == "" {
			continue
		}
		uses = append(uses, instruction.ToolUse{ID: c.ID, Name: c.Name, Arguments: c.Arguments})
	}
	return uses
}

// recordInstructionFindings scores one file's static hits and change finding.
// It reports whether it recorded anything.
//
// Every violation is flag-action, unattributed to a message, and described
// only by rule names, the file's path, type and short hash, line counts, the
// cause tool and hidden-content code-point labels and line numbers: never by
// matched text or anything else from the file's content.
func (p *Proxy) recordInstructionFindings(sessionID string, file *instruction.InstructionFile, obs instruction.Observation) bool {
	recorded := false
	for _, sv := range obs.Static {
		p.policy.RecordInstructionViolation(sessionID, policy.Violation{
			RuleName:      sv.RuleName,
			Description:   staticDescription(file, sv),
			Severity:      policy.Severity(staticSeverity(file, sv)),
			Action:        "flag",
			SourceRole:    "system",
			MessageIndex:  -1,
			EventCategory: categoryInstructionIntegrity,
		})
		recorded = true
	}

	if obs.Suppressed {
		slog.Info("instruction file change not scored: session not identified",
			"session_id", sessionID,
			"source_path", obs.Path,
			"change_type", string(obs.Change),
		)
	}
	if obs.Severity != "" {
		rule := instruction.RuleFileChanged
		if obs.Change == instruction.ChangeSinceLastSeen {
			rule = instruction.RuleFileChangedSinceLastSeen
		}
		p.policy.RecordInstructionViolation(sessionID, policy.Violation{
			RuleName:      rule,
			Description:   changeDescription(rule, file, obs),
			Severity:      policy.Severity(obs.Severity),
			Action:        "flag",
			SourceRole:    "provenance",
			MessageIndex:  -1,
			EventCategory: instruction.CategoryProvenance,
		})
		recorded = true
	}
	return recorded
}

// staticSeverity is the severity a static hit is scored at: critical for a
// block-action rule, info for a non-hidden rule in a file identified only by
// shape, and otherwise the rule's own. The shape cap wins over block: such a
// file is a guess, and a non-hidden rule in it never refuses a request either.
func staticSeverity(file *instruction.InstructionFile, v instruction.Violation) string {
	switch {
	case file.Confidence == instruction.ConfidenceMedium && !instruction.IsHiddenContentRule(v.RuleName):
		return "info"
	case v.Action == "block":
		return "critical"
	default:
		return v.Severity
	}
}

// fileLabel names a file by path (or as unnamed), type and short hash. A
// path longer than instruction.MaxSourcePathLen is not tracked, and is
// labelled as unnamed too.
func fileLabel(file *instruction.InstructionFile) string {
	name := file.SourcePath
	if name == "" || len(name) > instruction.MaxSourcePathLen {
		name = "unnamed instruction block"
	}
	hash := file.Hash
	if len(hash) > 12 {
		hash = hash[:12]
	}
	return fmt.Sprintf("%s (%s, %s)", name, file.Type.String(), hash)
}

func staticDescription(file *instruction.InstructionFile, v instruction.Violation) string {
	d := v.RuleName + " in " + fileLabel(file)
	if instruction.IsHiddenContentRule(v.RuleName) && v.Hidden != nil {
		labels := make([]string, 0, len(v.Hidden.CodePoints))
		for cp := range v.Hidden.CodePoints {
			labels = append(labels, cp)
		}
		sort.Strings(labels)
		lines := make([]string, len(v.Hidden.Lines))
		for i, n := range v.Hidden.Lines {
			lines[i] = strconv.Itoa(n)
		}
		d += "; code points " + strings.Join(labels, ",") + " on lines " + strings.Join(lines, ",")
	}
	return d
}

func changeDescription(rule string, file *instruction.InstructionFile, obs instruction.Observation) string {
	d := fmt.Sprintf("%s: %s; +%d/-%d lines", rule, fileLabel(file), obs.LinesAdded, obs.LinesRemoved)
	if len(obs.AddedRuleHits) > 0 {
		d += "; added lines hit " + strings.Join(obs.AddedRuleHits, ",")
	}
	if obs.CauseTool != "" {
		d += "; cause tool " + obs.CauseTool
	}
	return d
}
