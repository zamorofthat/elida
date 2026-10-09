package storage

import (
	"database/sql"
	"fmt"
	"time"

	"elida/internal/session"
)

// Caps on history carried from earlier sessions that shared a session ID.
// They bound only the carried part: a record's own entries are always
// written in full, and carried entries fill what is left up to the cap.
const (
	// MaxCarriedViolations matches the policy engine's per-session cap on
	// remembered semantic decisions (maxSeenDecisionIDsPerSession, 1024).
	MaxCarriedViolations = 1024
	// MaxCarriedCaptures matches the default storage.max_captured_per_session
	// (100), the per-session cap the capture buffer applies.
	MaxCarriedCaptures = 100
	// MaxCarriedShadow is session.MaxSemanticShadow, the per-session cap on
	// shadow decisions.
	MaxCarriedShadow = session.MaxSemanticShadow
)

// priorHistory is what a session row carries from earlier sessions with the
// same ID. It is stored in the prior_history column so that a later save of
// the same session (same start_time) still knows which entries came before
// it.
type priorHistory struct {
	Violations      []Violation       `json:"violations,omitempty"`
	CapturedContent []CapturedRequest `json:"captured_content,omitempty"`
	SemanticShadow  []SemanticShadow  `json:"semantic_shadow,omitempty"`
}

// loadPriorHistory returns the history the session being saved must carry.
//   - No stored row: none.
//   - A stored row from the same session (equal start_time): the prior
//     history that row already carried.
//   - A stored row from an earlier session: everything that row holds,
//     which already includes its own prior history.
func loadPriorHistory(tx *sql.Tx, id string, start time.Time) (priorHistory, error) {
	var storedStart time.Time
	var captured, violations, shadow, prior sql.NullString
	err := tx.QueryRow(`SELECT start_time, captured_content, violations, semantic_shadow, prior_history FROM sessions WHERE id = ?`, id).
		Scan(&storedStart, &captured, &violations, &shadow, &prior)
	if err == sql.ErrNoRows {
		return priorHistory{}, nil
	}
	if err != nil {
		return priorHistory{}, fmt.Errorf("read stored session: %w", err)
	}
	var p priorHistory
	if storedStart.Equal(start) {
		unmarshalJSON(prior, &p, "prior_history", id)
		return p, nil
	}
	unmarshalJSON(violations, &p.Violations, "violations", id)
	unmarshalJSON(captured, &p.CapturedContent, "captured_content", id)
	unmarshalJSON(shadow, &p.SemanticShadow, "semantic_shadow", id)
	return p, nil
}

// violationKey identifies a violation occurrence: its EventID when present,
// otherwise the fields that describe it (older rows carry no EventID).
func violationKey(v Violation) string {
	if v.EventID != "" {
		return "id\x00" + v.EventID
	}
	return fmt.Sprintf("v\x00%s\x00%s\x00%s\x00%s\x00%t\x00%s",
		v.RuleName, v.EventCategory, v.Severity, v.SourceRole, v.EvidenceOnly, v.Description)
}

func captureKey(c CapturedRequest) string {
	return fmt.Sprintf("%d\x00%s\x00%s", c.Timestamp.UnixNano(), c.Method, c.Path)
}

// mergeHistory merges the prior history into record and returns the merged
// record together with the part of prior that was kept, which is what the
// row stores as its prior_history (so it stays bounded too).
//
//   - Violations: prior entries first (older), then the record's own. A
//     prior entry is dropped when the record holds the same EventID (or, for
//     entries without one, the same rule, category, severity, role, evidence
//     flag and description). At most MaxCarriedViolations in total unless
//     the record alone has more; the newest prior entries are kept.
//   - Captured content: prior first, then the record's own; deduplicated by
//     timestamp, method and path; capped by MaxCarriedCaptures likewise.
//   - Semantic shadow (newest first): the record's own, then prior;
//     deduplicated by DecisionID; capped by MaxCarriedShadow likewise, the
//     newest prior entries kept.
func mergeHistory(record SessionRecord, prior priorHistory) (SessionRecord, priorHistory) {
	var kept priorHistory

	own := make(map[string]bool, len(record.Violations))
	for _, v := range record.Violations {
		own[violationKey(v)] = true
	}
	for _, v := range prior.Violations {
		if !own[violationKey(v)] {
			kept.Violations = append(kept.Violations, v)
		}
	}
	kept.Violations = keepTail(kept.Violations, MaxCarriedViolations-len(record.Violations))

	ownC := make(map[string]bool, len(record.CapturedContent))
	for _, c := range record.CapturedContent {
		ownC[captureKey(c)] = true
	}
	for _, c := range prior.CapturedContent {
		if !ownC[captureKey(c)] {
			kept.CapturedContent = append(kept.CapturedContent, c)
		}
	}
	kept.CapturedContent = keepTail(kept.CapturedContent, MaxCarriedCaptures-len(record.CapturedContent))

	ownS := make(map[string]bool, len(record.SemanticShadow))
	for _, s := range record.SemanticShadow {
		ownS[s.DecisionID] = true
	}
	for _, s := range prior.SemanticShadow {
		if s.DecisionID == "" || !ownS[s.DecisionID] {
			kept.SemanticShadow = append(kept.SemanticShadow, s)
		}
	}
	kept.SemanticShadow = keepHead(kept.SemanticShadow, MaxCarriedShadow-len(record.SemanticShadow))

	if len(kept.Violations) > 0 {
		record.Violations = append(append([]Violation(nil), kept.Violations...), record.Violations...)
	}
	if len(kept.CapturedContent) > 0 {
		record.CapturedContent = append(append([]CapturedRequest(nil), kept.CapturedContent...), record.CapturedContent...)
	}
	if len(kept.SemanticShadow) > 0 {
		record.SemanticShadow = append(append([]SemanticShadow(nil), record.SemanticShadow...), kept.SemanticShadow...)
	}
	return record, kept
}

// keepTail returns the last n elements of s (none when n <= 0).
func keepTail[T any](s []T, n int) []T {
	if n <= 0 {
		return nil
	}
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// keepHead returns the first n elements of s (none when n <= 0).
func keepHead[T any](s []T, n int) []T {
	if n <= 0 {
		return nil
	}
	if len(s) > n {
		return s[:n]
	}
	return s
}
