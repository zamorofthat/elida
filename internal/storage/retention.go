package storage

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// RetentionBatchSize is the most rows one retention statement deletes. The
// store has a single connection, so one statement over a large backlog (the
// first sweep of a database that was never pruned) would stall every session
// save behind it; between batches other writers get the connection.
const RetentionBatchSize = 500

// DefaultRetentionInterval is how often a running process sweeps.
const DefaultRetentionInterval = time.Hour

// RetentionPolicy says how long stored records are kept. A period of zero or
// less keeps that class of record forever.
type RetentionPolicy struct {
	// HistoryDays covers session records, voice session records and TTS
	// request records.
	HistoryDays int
	// EventDays covers the event log, and with it the integrity roots
	// computed over the events it removes.
	EventDays int
}

// Enabled reports whether the policy removes anything at all.
func (p RetentionPolicy) Enabled() bool {
	return p.HistoryDays > 0 || p.EventDays > 0
}

// RetentionResult counts the rows one sweep removed.
type RetentionResult struct {
	Sessions       int64
	VoiceSessions  int64
	TTSRequests    int64
	Events         int64
	IntegrityRoots int64
}

// Total is the number of rows removed across all tables.
func (r RetentionResult) Total() int64 {
	return r.Sessions + r.VoiceSessions + r.TTSRequests + r.Events + r.IntegrityRoots
}

// SweepRetention deletes every record older than the policy allows and
// reports what it removed. On an error (including a canceled context) the
// result holds what was removed before it.
//
// instruction_files, instruction_path_versions and fingerprint_baselines are
// not history and are never touched here.
func (s *SQLiteStore) SweepRetention(ctx context.Context, policy RetentionPolicy) (RetentionResult, error) {
	var result RetentionResult
	now := time.Now()

	if policy.HistoryDays > 0 {
		cutoff := now.AddDate(0, 0, -policy.HistoryDays)
		var err error
		if result.Sessions, err = s.deleteInBatches(ctx,
			`DELETE FROM sessions WHERE id IN (
				SELECT id FROM sessions WHERE end_time < ? LIMIT ?)`, cutoff); err != nil {
			return result, fmt.Errorf("failed to sweep sessions: %w", err)
		}
		// A voice session that never ended (a crash leftover) ages from its start.
		if result.VoiceSessions, err = s.deleteInBatches(ctx,
			`DELETE FROM voice_sessions WHERE id IN (
				SELECT id FROM voice_sessions WHERE COALESCE(end_time, start_time) < ? LIMIT ?)`, cutoff); err != nil {
			return result, fmt.Errorf("failed to sweep voice sessions: %w", err)
		}
		if result.TTSRequests, err = s.deleteInBatches(ctx,
			`DELETE FROM tts_requests WHERE id IN (
				SELECT id FROM tts_requests WHERE timestamp < ? LIMIT ?)`, cutoff); err != nil {
			return result, fmt.Errorf("failed to sweep TTS requests: %w", err)
		}
	}

	if policy.EventDays > 0 {
		cutoff := now.AddDate(0, 0, -policy.EventDays)
		var err error
		if result.Events, result.IntegrityRoots, err = s.sweepEvents(ctx, cutoff); err != nil {
			return result, fmt.Errorf("failed to sweep events: %w", err)
		}
	}

	return result, nil
}

// deleteInBatches runs a delete whose two parameters are the cutoff and a row
// limit until it has nothing left to remove.
func (s *SQLiteStore) deleteInBatches(ctx context.Context, query string, cutoff time.Time) (int64, error) {
	var total int64
	for {
		res, err := s.db.ExecContext(ctx, query, cutoff, RetentionBatchSize)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < RetentionBatchSize {
			return total, nil
		}
	}
}

// expiredEventBatch selects the next batch of events to remove. The order is
// the timestamp index's own, so no batch sorts the backlog, and it makes the
// two statements of one transaction pick the same rows.
const expiredEventBatch = `SELECT id FROM events WHERE timestamp < ? ORDER BY timestamp, id LIMIT ?`

// sweepEvents removes events older than the cutoff. An integrity root covers
// every event of its session (events and roots are both keyed by session ID),
// so a root is removed in the same transaction as the first of its events:
// a root left behind could no longer be proven, and each proof request would
// report it as corrupt data. The session's next finalization computes a new
// root over the events that remain.
func (s *SQLiteStore) sweepEvents(ctx context.Context, cutoff time.Time) (events, roots int64, err error) {
	for {
		batchEvents, batchRoots, batchErr := s.sweepEventBatch(ctx, cutoff)
		events += batchEvents
		roots += batchRoots
		if batchErr != nil {
			return events, roots, batchErr
		}
		if batchEvents < RetentionBatchSize {
			return events, roots, nil
		}
	}
}

func (s *SQLiteStore) sweepEventBatch(ctx context.Context, cutoff time.Time) (events, roots int64, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`DELETE FROM sdr_integrity WHERE session_id IN (
			SELECT session_id FROM events WHERE id IN (`+expiredEventBatch+`))`,
		cutoff, RetentionBatchSize)
	if err != nil {
		return 0, 0, err
	}
	if roots, err = res.RowsAffected(); err != nil {
		return 0, 0, err
	}

	res, err = tx.ExecContext(ctx,
		`DELETE FROM events WHERE id IN (`+expiredEventBatch+`)`,
		cutoff, RetentionBatchSize)
	if err != nil {
		return 0, 0, err
	}
	if events, err = res.RowsAffected(); err != nil {
		return 0, 0, err
	}

	if err = tx.Commit(); err != nil {
		return 0, 0, err
	}
	return events, roots, nil
}

// RunRetention enforces the policy until ctx ends: one sweep right away, so a
// process that restarts more often than the interval still prunes, then one
// per interval. It returns at once when the policy keeps everything.
func (s *SQLiteStore) RunRetention(ctx context.Context, policy RetentionPolicy, interval time.Duration) {
	if !policy.Enabled() {
		return
	}
	if interval <= 0 {
		interval = DefaultRetentionInterval
	}

	s.sweepAndLog(ctx, policy)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepAndLog(ctx, policy)
		}
	}
}

func (s *SQLiteStore) sweepAndLog(ctx context.Context, policy RetentionPolicy) {
	result, err := s.SweepRetention(ctx, policy)
	attrs := []any{
		"sessions", result.Sessions,
		"voice_sessions", result.VoiceSessions,
		"tts_requests", result.TTSRequests,
		"events", result.Events,
		"integrity_roots", result.IntegrityRoots,
		"history_days", policy.HistoryDays,
		"event_days", policy.EventDays,
	}
	switch {
	case err != nil && ctx.Err() != nil:
		// Shutdown interrupted the sweep; the next start resumes it.
	case err != nil:
		slog.Error("retention sweep failed; it will run again at the next interval", append(attrs, "error", err)...)
	case result.Total() > 0:
		slog.Info("retention sweep removed expired records", attrs...)
	}
}
