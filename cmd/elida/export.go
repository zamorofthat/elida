// Package main's `export-sessions` subcommand (this file) implements
// synthspine seam ① export.
//
// CAVEAT — tool ORDER in this export is NOT reliable: the SQLite history
// store persists only session-end AGGREGATE tool counts per session (one
// "tool_called" event per distinct tool name, with a CallCount, taken from a
// Go map — see toolCallHistory below), not per-call ordered history. Cross-
// tool sequence in the output reflects Go map-iteration order, not real
// chronology, and repeated calls of the same tool collapse to duration_ms=0.
// The output is usable for tool VOCABULARY/FREQUENCY but NOT for
// transition-order fitting (e.g. a tool-Markov-chain fit), pending per-call
// tool-history persistence (tracked as a separate follow-on; out of scope
// here — see toolCallHistory's doc comment). This caveat is also surfaced at
// runtime (runExportSessions) and in --help.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"time"

	"elida/internal/config"
	"elida/internal/session"
	"elida/internal/storage"
)

// orderCaveat is the standing caveat about tool-sequence fidelity in this
// export (see the package doc comment above and toolCallHistory below for
// the root cause). Surfaced in three places: that doc comment, --help/usage
// text, and a runtime warning on every invocation.
const orderCaveat = "tool ORDER in this export is NOT reliable: the SQLite store keeps only " +
	"session-end AGGREGATE tool counts (nondeterministic order), not per-call ordered history. " +
	"Output is usable for tool VOCABULARY/FREQUENCY but NOT for transition-order fitting, " +
	"pending per-call tool-history persistence."

// AgentTurn is a synthspine seam ① projection record: one per tool call in a
// session's trajectory. See docs/superpowers/specs/2026-09-02-panel-member-c-toolchain-design.md §4.
type AgentTurn struct {
	Signal     string `json:"signal"`
	SessionID  string `json:"session_id"`
	Tool       string `json:"tool"`
	Turn       int    `json:"turn"`
	DurationMs int64  `json:"duration_ms"`
	Entity     string `json:"entity"`
}

// toAgentTurns projects a session's tool-call history into synthspine agent_turn records.
func toAgentTurns(sessionID, class string, history []session.ToolCallRecord) []AgentTurn {
	out := make([]AgentTurn, len(history))
	for i, rec := range history {
		var dt int64
		if i > 0 {
			dt = rec.Timestamp.Sub(history[i-1].Timestamp).Milliseconds()
		}
		out[i] = AgentTurn{
			Signal: "agent_turn", SessionID: sessionID, Tool: rec.ToolName,
			Turn: i, DurationMs: dt, Entity: class,
		}
	}
	return out
}

// endedStates are the terminal session states persisted to the sessions
// table. "active" is never persisted there (SaveSession is only called from
// the session-end callback / the forensic "flagged" checkpoint), and
// "flagged" is an in-flight forensic snapshot that may still be updated by a
// later end-of-session upsert — so it does not count as ended either.
// export-sessions emits complete trajectories only (design doc §8).
var endedStates = []string{"completed", "killed", "timeout"}

// runExportSessions implements the `elida export-sessions` subcommand: it
// walks the SQLite history store for ended sessions started at or after
// --since, projects each session's tool-call history into synthspine
// agent_turn records via toAgentTurns, and writes them as jsonl to --out.
func runExportSessions(args []string) error {
	fs := flag.NewFlagSet("export-sessions", flag.ExitOnError)
	configPath := fs.String("config", "configs/elida.yaml", "path to config file")
	sinceStr := fs.String("since", "", "only export sessions started at or after this RFC3339 timestamp (e.g. 2026-01-01T00:00:00Z)")
	outPath := fs.String("out", "", "output jsonl file path (required)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: elida export-sessions --out <path> [--since <RFC3339>] [--config <path>]\n\n")
		fmt.Fprintf(fs.Output(), "Walks the SQLite history store for ended sessions and writes each\n")
		fmt.Fprintf(fs.Output(), "session's tool-call trajectory as synthspine agent_turn jsonl records.\n\n")
		fmt.Fprintf(fs.Output(), "CAVEAT: %s\n\n", orderCaveat)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *outPath == "" {
		fs.Usage()
		return fmt.Errorf("--out is required")
	}

	fmt.Fprintf(os.Stderr, "export-sessions: WARNING: %s\n", orderCaveat)
	slog.Warn("export-sessions: tool order not reliable in this export", "reason", orderCaveat)

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("failed to load config %s: %w", *configPath, err)
	}
	if !cfg.Storage.Enabled {
		return fmt.Errorf("storage is not enabled in %s; export-sessions requires a SQLite history store", *configPath)
	}

	var since *time.Time
	if *sinceStr != "" {
		t, err := time.Parse(time.RFC3339, *sinceStr)
		if err != nil {
			return fmt.Errorf("invalid --since %q: %w", *sinceStr, err)
		}
		since = &t
	}

	store, err := storage.NewSQLiteStore(cfg.Storage.Path)
	if err != nil {
		return fmt.Errorf("failed to open history store %s: %w", cfg.Storage.Path, err)
	}
	defer func() { _ = store.Close() }()

	sessions, err := listEndedSessions(store, since)
	if err != nil {
		return fmt.Errorf("failed to list ended sessions: %w", err)
	}

	out, err := os.Create(*outPath)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", *outPath, err)
	}
	defer func() { _ = out.Close() }()

	enc := json.NewEncoder(out)
	turnCount := 0
	for _, rec := range sessions {
		history, herr := toolCallHistory(store, rec.ID)
		if herr != nil {
			return fmt.Errorf("failed to load tool-call history for session %s: %w", rec.ID, herr)
		}
		for _, turn := range toAgentTurns(rec.ID, rec.FingerprintClass, history) {
			if err := enc.Encode(turn); err != nil {
				return fmt.Errorf("failed to write turn for session %s: %w", rec.ID, err)
			}
			turnCount++
		}
	}

	fmt.Fprintf(os.Stderr, "export-sessions: wrote %d agent_turn record(s) from %d session(s) to %s\n", turnCount, len(sessions), *outPath)
	return nil
}

// listEndedSessions returns all sessions in a terminal state (endedStates),
// started at or after since, ordered by start time. It reuses
// SQLiteStore.ListSessions — the same query GET /control/history uses
// (internal/control/api.go handleHistory) — once per terminal state, since
// ListSessionsOptions.State is an exact-match filter and a session can only
// hold one persisted state at a time.
func listEndedSessions(store *storage.SQLiteStore, since *time.Time) ([]storage.SessionRecord, error) {
	var all []storage.SessionRecord
	for _, state := range endedStates {
		recs, err := store.ListSessions(storage.ListSessionsOptions{State: state, Since: since})
		if err != nil {
			return nil, err
		}
		all = append(all, recs...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].StartTime.Before(all[j].StartTime) })
	return all, nil
}

// toolCallHistory reconstructs a session's ordered tool-call history from the
// persisted event log. See orderCaveat / the package doc comment above:
// this is the root cause of that caveat.
//
// NOTE on fidelity: the SQLite history store does not persist individual,
// per-call ToolCallRecord entries for ended sessions — that detail exists
// only on the live in-memory session.Session (ToolCallHistory), which is
// discarded once a session ends and is evicted from session.Manager. The
// closest surviving signal is the "tool_called" event: recorded once per
// distinct tool name at session end with an AGGREGATE count, iterated from
// a Go map (storage.ToolCalledData{ToolName, CallCount}, from
// sess.GetToolCallCounts()), see cmd/elida/main.go's
// initSessionEndCallback (~line 497-504). Cross-tool order here is
// therefore Go map-iteration order, not true chronology. This expands each
// such event back into CallCount individual records at the event's
// persisted timestamp, ordered by event id (insertion order) — a stable
// but NOT reliable turn sequence: tool identity and per-tool frequency are
// exact, but cross-tool order is arbitrary and inter-call durations are not
// recoverable from persisted data
// (repeated calls of the same tool collapse to duration_ms=0).
func toolCallHistory(store *storage.SQLiteStore, sessionID string) ([]session.ToolCallRecord, error) {
	events, err := store.GetSessionEvents(sessionID)
	if err != nil {
		return nil, err
	}
	sort.Slice(events, func(i, j int) bool { return events[i].ID < events[j].ID })

	var history []session.ToolCallRecord
	for _, evt := range events {
		if evt.Type != storage.EventToolCalled {
			continue
		}
		var data storage.ToolCalledData
		if err := json.Unmarshal(evt.Data, &data); err != nil {
			continue
		}
		count := data.CallCount
		if count < 1 {
			count = 1
		}
		for i := 0; i < count; i++ {
			history = append(history, session.ToolCallRecord{
				Timestamp: evt.Timestamp,
				ToolName:  data.ToolName,
				ToolType:  data.ToolType,
				RequestID: data.RequestID,
			})
		}
	}
	return history, nil
}
