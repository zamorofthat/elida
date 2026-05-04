package mcp

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

const sparkChars = "▁▂▃▄▅▆▇█"

// FormatASCII renders tool results as token-efficient ASCII.
func FormatASCII(toolName string, result any) string {
	switch toolName {
	case "elida_get_stats":
		return formatStats(result)
	case "elida_list_sessions":
		return formatSessionList(result)
	case "elida_get_session":
		return formatSessionDetail(result)
	case "elida_get_outliers":
		return formatOutliers(result)
	case "elida_get_timeline":
		return formatTimeline(result)
	case "elida_get_violations":
		return formatViolations(result)
	default:
		b, _ := json.Marshal(result)
		return string(b)
	}
}

func formatStats(result any) string {
	m := toMap(result)
	if m == nil {
		return "{}"
	}
	return fmt.Sprintf("Sessions: %v active  %v completed  %v killed  %v timeout\nRequests: %v  In: %v  Out: %v",
		m["active"], m["completed"], m["killed"], m["timed_out"],
		m["total_requests"], m["total_bytes_in"], m["total_bytes_out"],
	)
}

func formatSessionList(result any) string {
	m := toMap(result)
	if m == nil {
		return "No sessions"
	}
	sessions, _ := m["sessions"].([]any)
	if len(sessions) == 0 {
		return "No sessions"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%v sessions:\n", m["total"])
	for _, s := range sessions {
		sm := toMapAny(s)
		if sm == nil {
			continue
		}
		b.WriteString(formatSessionLine(sm))
		b.WriteByte('\n')
	}
	return b.String()
}

func formatSessionDetail(result any) string {
	sm := toMapAny(result)
	if sm == nil {
		return "Session not found"
	}
	return formatSessionCard(sm)
}

func formatOutliers(result any) string {
	m := toMap(result)
	if m == nil {
		return "No outliers"
	}
	outliers, _ := m["outliers"].([]any)
	if len(outliers) == 0 {
		return "No outliers"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Top %d by %v:\n", len(outliers), m["metric"])
	for i, o := range outliers {
		sm := toMapAny(o)
		if sm == nil {
			continue
		}
		fmt.Fprintf(&b, "%d. ", i+1)
		b.WriteString(formatSessionLine(sm))
		b.WriteByte('\n')
	}
	return b.String()
}

func formatTimeline(result any) string {
	m := toMap(result)
	if m == nil {
		return "No timeline"
	}
	entries, _ := m["entries"].([]any)
	if len(entries) == 0 {
		return fmt.Sprintf("Session %v: no events", m["session_id"])
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Timeline for %v (%d events):\n", m["session_id"], len(entries))
	for _, e := range entries {
		em := toMapAny(e)
		if em == nil {
			continue
		}
		ts, _ := em["timestamp"].(string)
		if len(ts) > 19 {
			ts = ts[11:19] // HH:MM:SS
		}
		typ, _ := em["type"].(string)
		detail := em["detail"]

		switch typ {
		case "tool_call":
			dm := toMapAny(detail)
			fmt.Fprintf(&b, "  %s  [tool] %v\n", ts, dm["tool_name"])
		case "violation":
			dm := toMapAny(detail)
			fmt.Fprintf(&b, "  %s  [!%v] %v\n", ts, dm["severity"], dm["rule"])
		case "message":
			dm := toMapAny(detail)
			fmt.Fprintf(&b, "  %s  [msg] %v\n", ts, dm["role"])
		default:
			fmt.Fprintf(&b, "  %s  [%s]\n", ts, typ)
		}
	}
	return b.String()
}

func formatViolations(result any) string {
	m := toMap(result)
	if m == nil {
		return "No violations"
	}
	violations, _ := m["violations"].([]any)
	if len(violations) == 0 {
		return "No violations"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%v flagged sessions:\n", m["total"])
	for _, v := range violations {
		vm := toMapAny(v)
		if vm == nil {
			continue
		}
		risk := toFloat(vm["risk_score"])
		fmt.Fprintf(&b, "  %v  %s Risk:%.0f  [%v] %v violations\n",
			vm["session_id"], riskBar(risk), risk, vm["current_action"], vm["violation_count"])
	}
	return b.String()
}

// --- helpers ---

func formatSessionLine(sm map[string]any) string {
	id, _ := sm["id"].(string)
	if len(id) > 12 {
		id = id[:12]
	}
	risk := toFloat(sm["risk_score"])
	return fmt.Sprintf("%-12s  %s Risk:%3.0f  %v reqs  %v",
		id, riskBar(risk), risk, sm["request_count"], sm["state"])
}

func formatSessionCard(sm map[string]any) string {
	id, _ := sm["id"].(string)
	risk := toFloat(sm["risk_score"])
	action, _ := sm["current_action"].(string)
	if action == "" {
		action = "observe"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Session %s  Risk: %s %.0f/100  [%s]\n", id, riskBar(risk), risk, strings.ToUpper(action))
	fmt.Fprintf(&b, "  State:    %v  Duration: %v\n", sm["state"], sm["duration"])
	fmt.Fprintf(&b, "  Requests: %v  In: %v  Out: %v\n", sm["request_count"], sm["bytes_in"], sm["bytes_out"])
	if tokIn := toFloat(sm["tokens_in"]); tokIn > 0 {
		fmt.Fprintf(&b, "  Tokens:   in=%v out=%v\n", sm["tokens_in"], sm["tokens_out"])
	}

	// Tool pills
	if tc, ok := sm["tool_counts"].(map[string]any); ok && len(tc) > 0 {
		b.WriteString("  Tools:   ")
		type kv struct {
			k string
			v int
		}
		var tools []kv
		maxCount := 0
		for k, v := range tc {
			c := int(toFloat(v))
			tools = append(tools, kv{k, c})
			if c > maxCount {
				maxCount = c
			}
		}
		sort.Slice(tools, func(i, j int) bool { return tools[i].v > tools[j].v })
		for i, t := range tools {
			if i > 0 {
				b.WriteString(" ")
			}
			// Mark outlier if >3x median
			marker := ""
			if len(tools) > 2 && t.v > tools[len(tools)/2].v*3 {
				marker = " <- outlier"
			}
			fmt.Fprintf(&b, "%s(%d)%s", t.k, t.v, marker)
		}
		b.WriteByte('\n')
	}

	return b.String()
}

func riskBar(score float64) string {
	filled := int(score / 10)
	if filled > 10 {
		filled = 10
	}
	if filled < 0 {
		filled = 0
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", 10-filled)
}

func sparkline(values []float64) string {
	if len(values) == 0 {
		return ""
	}
	maxVal := 0.0
	for _, v := range values {
		if v > maxVal {
			maxVal = v
		}
	}
	if maxVal == 0 {
		return strings.Repeat(string([]rune(sparkChars)[0]), len(values))
	}

	runes := []rune(sparkChars)
	var b strings.Builder
	for _, v := range values {
		idx := int(math.Round(v / maxVal * float64(len(runes)-1)))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(runes) {
			idx = len(runes) - 1
		}
		b.WriteRune(runes[idx])
	}
	return b.String()
}

func toMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	// Try via JSON round-trip for struct types
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}

func toMapAny(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return toMap(v)
}

func toFloat(v any) float64 {
	switch f := v.(type) {
	case float64:
		return f
	case float32:
		return float64(f)
	case int:
		return float64(f)
	case int64:
		return float64(f)
	case json.Number:
		n, _ := f.Float64()
		return n
	default:
		return 0
	}
}
