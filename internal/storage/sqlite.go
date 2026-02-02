package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	_ "modernc.org/sqlite"
)

// SessionRecord represents a historical session record
// CapturedRequest stores request/response content for session records
type CapturedRequest struct {
	Timestamp    time.Time `json:"timestamp"`
	Method       string    `json:"method"`
	Path         string    `json:"path"`
	RequestBody  string    `json:"request_body,omitempty"`
	ResponseBody string    `json:"response_body,omitempty"`
	StatusCode   int       `json:"status_code"`
}

// Violation records a policy violation for session records
type Violation struct {
	RuleName    string `json:"rule_name"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
	MatchedText string `json:"matched_text,omitempty"`
	Action      string `json:"action"`
}

// TranscriptEntry represents a single utterance in a voice session
type TranscriptEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Speaker   string    `json:"speaker"` // "user" or "assistant"
	Text      string    `json:"text"`
	IsFinal   bool      `json:"is_final"`
	Source    string    `json:"source"` // "stt", "tts", "text"
}

// VoiceSessionRecord represents a historical voice session record (CDR)
type VoiceSessionRecord struct {
	ID              string            `json:"id"`
	ParentSessionID string            `json:"parent_session_id"`
	State           string            `json:"state"`
	StartTime       time.Time         `json:"start_time"`
	AnswerTime      *time.Time        `json:"answer_time,omitempty"`
	EndTime         *time.Time        `json:"end_time,omitempty"`
	DurationMs      int64             `json:"duration_ms"`
	AudioDurationMs int64             `json:"audio_duration_ms"`
	TurnCount       int               `json:"turn_count"`
	Model           string            `json:"model,omitempty"`
	Voice           string            `json:"voice,omitempty"`
	Language        string            `json:"language,omitempty"`
	Protocol        string            `json:"protocol,omitempty"`
	AudioBytesIn    int64             `json:"audio_bytes_in"`
	AudioBytesOut   int64             `json:"audio_bytes_out"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	Transcript      []TranscriptEntry `json:"transcript,omitempty"`
}

// TTSRequest represents a text-to-speech API call
type TTSRequest struct {
	ID            string    `json:"id"`
	SessionID     string    `json:"session_id"`
	Timestamp     time.Time `json:"timestamp"`
	Provider      string    `json:"provider"`       // "openai", "deepgram", "elevenlabs"
	Model         string    `json:"model"`          // e.g., "tts-1", "aura-asteria-en"
	Voice         string    `json:"voice"`          // e.g., "alloy", "nova"
	Text          string    `json:"text"`           // Text being synthesized
	TextLength    int       `json:"text_length"`    // Character count
	ResponseBytes int64     `json:"response_bytes"` // Audio response size
	DurationMs    int64     `json:"duration_ms"`    // Request duration
	StatusCode    int       `json:"status_code"`
}

type SessionRecord struct {
	ID              string            `json:"id"`
	State           string            `json:"state"`
	StartTime       time.Time         `json:"start_time"`
	EndTime         time.Time         `json:"end_time"`
	DurationMs      int64             `json:"duration_ms"`
	RequestCount    int               `json:"request_count"`
	BytesIn         int64             `json:"bytes_in"`
	BytesOut        int64             `json:"bytes_out"`
	Backend         string            `json:"backend"`
	ClientAddr      string            `json:"client_addr"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	CapturedContent []CapturedRequest `json:"captured_content,omitempty"`
	Violations      []Violation       `json:"violations,omitempty"`
}

// SQLiteStore provides persistent storage for session history
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore creates a new SQLite-backed storage
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Enable WAL mode for better concurrent performance
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	store := &SQLiteStore{db: db}

	if err := store.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	slog.Info("SQLite storage initialized", "path", dbPath)
	return store, nil
}

// migrate creates the necessary tables
func (s *SQLiteStore) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY,
		state TEXT NOT NULL,
		start_time DATETIME NOT NULL,
		end_time DATETIME NOT NULL,
		duration_ms INTEGER NOT NULL,
		request_count INTEGER NOT NULL DEFAULT 0,
		bytes_in INTEGER NOT NULL DEFAULT 0,
		bytes_out INTEGER NOT NULL DEFAULT 0,
		backend TEXT NOT NULL,
		client_addr TEXT NOT NULL,
		metadata TEXT,
		captured_content TEXT,
		violations TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_sessions_start_time ON sessions(start_time);
	CREATE INDEX IF NOT EXISTS idx_sessions_end_time ON sessions(end_time);
	CREATE INDEX IF NOT EXISTS idx_sessions_state ON sessions(state);
	CREATE INDEX IF NOT EXISTS idx_sessions_backend ON sessions(backend);

	-- Voice session CDR table
	CREATE TABLE IF NOT EXISTS voice_sessions (
		id TEXT PRIMARY KEY,
		parent_session_id TEXT NOT NULL,
		state TEXT NOT NULL,
		start_time DATETIME NOT NULL,
		answer_time DATETIME,
		end_time DATETIME,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		audio_duration_ms INTEGER NOT NULL DEFAULT 0,
		turn_count INTEGER NOT NULL DEFAULT 0,
		model TEXT,
		voice TEXT,
		language TEXT,
		protocol TEXT,
		audio_bytes_in INTEGER NOT NULL DEFAULT 0,
		audio_bytes_out INTEGER NOT NULL DEFAULT 0,
		metadata TEXT,
		transcript TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_voice_sessions_parent ON voice_sessions(parent_session_id);
	CREATE INDEX IF NOT EXISTS idx_voice_sessions_start_time ON voice_sessions(start_time);
	CREATE INDEX IF NOT EXISTS idx_voice_sessions_state ON voice_sessions(state);

	-- TTS (Text-to-Speech) request table
	CREATE TABLE IF NOT EXISTS tts_requests (
		id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		timestamp DATETIME NOT NULL,
		provider TEXT NOT NULL,
		model TEXT,
		voice TEXT,
		text TEXT,
		text_length INTEGER NOT NULL DEFAULT 0,
		response_bytes INTEGER NOT NULL DEFAULT 0,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		status_code INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_tts_session ON tts_requests(session_id);
	CREATE INDEX IF NOT EXISTS idx_tts_timestamp ON tts_requests(timestamp);
	CREATE INDEX IF NOT EXISTS idx_tts_provider ON tts_requests(provider);
	`

	_, err := s.db.Exec(schema)
	return err
}

// SaveSession saves a completed session record
func (s *SQLiteStore) SaveSession(record SessionRecord) error {
	metadata, err := json.Marshal(record.Metadata)
	if err != nil {
		metadata = []byte("{}")
	}

	capturedContent, err := json.Marshal(record.CapturedContent)
	if err != nil {
		capturedContent = []byte("[]")
	}

	violations, err := json.Marshal(record.Violations)
	if err != nil {
		violations = []byte("[]")
	}

	_, err = s.db.Exec(`
		INSERT OR REPLACE INTO sessions
		(id, state, start_time, end_time, duration_ms, request_count, bytes_in, bytes_out, backend, client_addr, metadata, captured_content, violations)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID,
		record.State,
		record.StartTime,
		record.EndTime,
		record.DurationMs,
		record.RequestCount,
		record.BytesIn,
		record.BytesOut,
		record.Backend,
		record.ClientAddr,
		string(metadata),
		string(capturedContent),
		string(violations),
	)
	if err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}

	slog.Debug("session saved to history",
		"session_id", record.ID,
		"state", record.State,
		"captures", len(record.CapturedContent),
		"violations", len(record.Violations),
	)
	return nil
}

// GetSession retrieves a session by ID
func (s *SQLiteStore) GetSession(id string) (*SessionRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, state, start_time, end_time, duration_ms, request_count, bytes_in, bytes_out, backend, client_addr, metadata, captured_content, violations
		FROM sessions WHERE id = ?`, id)

	var record SessionRecord
	var metadataStr, capturedStr, violationsStr sql.NullString
	err := row.Scan(
		&record.ID,
		&record.State,
		&record.StartTime,
		&record.EndTime,
		&record.DurationMs,
		&record.RequestCount,
		&record.BytesIn,
		&record.BytesOut,
		&record.Backend,
		&record.ClientAddr,
		&metadataStr,
		&capturedStr,
		&violationsStr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	if metadataStr.Valid && metadataStr.String != "" {
		_ = json.Unmarshal([]byte(metadataStr.String), &record.Metadata)
	}
	if capturedStr.Valid && capturedStr.String != "" {
		_ = json.Unmarshal([]byte(capturedStr.String), &record.CapturedContent)
	}
	if violationsStr.Valid && violationsStr.String != "" {
		_ = json.Unmarshal([]byte(violationsStr.String), &record.Violations)
	}

	return &record, nil
}

// ListSessionsOptions contains options for listing sessions
type ListSessionsOptions struct {
	Limit   int
	Offset  int
	State   string // Filter by state
	Backend string // Filter by backend
	Since   *time.Time
	Until   *time.Time
}

// ListSessions retrieves sessions with filtering and pagination
func (s *SQLiteStore) ListSessions(opts ListSessionsOptions) ([]SessionRecord, error) {
	query := `
		SELECT id, state, start_time, end_time, duration_ms, request_count, bytes_in, bytes_out, backend, client_addr, metadata, captured_content, violations
		FROM sessions WHERE 1=1`

	args := []interface{}{}

	if opts.State != "" {
		query += " AND state = ?"
		args = append(args, opts.State)
	}
	if opts.Backend != "" {
		query += " AND backend = ?"
		args = append(args, opts.Backend)
	}
	if opts.Since != nil {
		query += " AND start_time >= ?"
		args = append(args, *opts.Since)
	}
	if opts.Until != nil {
		query += " AND start_time <= ?"
		args = append(args, *opts.Until)
	}

	query += " ORDER BY start_time DESC"

	if opts.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, opts.Limit)
	}
	if opts.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, opts.Offset)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	defer rows.Close()

	var records []SessionRecord
	for rows.Next() {
		var record SessionRecord
		var metadataStr, capturedStr, violationsStr sql.NullString
		err := rows.Scan(
			&record.ID,
			&record.State,
			&record.StartTime,
			&record.EndTime,
			&record.DurationMs,
			&record.RequestCount,
			&record.BytesIn,
			&record.BytesOut,
			&record.Backend,
			&record.ClientAddr,
			&metadataStr,
			&capturedStr,
			&violationsStr,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan session: %w", err)
		}

		if metadataStr.Valid && metadataStr.String != "" {
			_ = json.Unmarshal([]byte(metadataStr.String), &record.Metadata)
		}
		if capturedStr.Valid && capturedStr.String != "" {
			_ = json.Unmarshal([]byte(capturedStr.String), &record.CapturedContent)
		}
		if violationsStr.Valid && violationsStr.String != "" {
			_ = json.Unmarshal([]byte(violationsStr.String), &record.Violations)
		}

		records = append(records, record)
	}

	return records, nil
}

// Stats represents aggregate statistics
type Stats struct {
	TotalSessions     int64            `json:"total_sessions"`
	TotalRequests     int64            `json:"total_requests"`
	TotalBytesIn      int64            `json:"total_bytes_in"`
	TotalBytesOut     int64            `json:"total_bytes_out"`
	AvgDurationMs     float64          `json:"avg_duration_ms"`
	AvgRequestCount   float64          `json:"avg_request_count"`
	SessionsByState   map[string]int64 `json:"sessions_by_state"`
	SessionsByBackend map[string]int64 `json:"sessions_by_backend"`
}

// GetStats retrieves aggregate statistics
func (s *SQLiteStore) GetStats(since *time.Time) (*Stats, error) {
	stats := &Stats{
		SessionsByState:   make(map[string]int64),
		SessionsByBackend: make(map[string]int64),
	}

	// Build base WHERE clause
	whereClause := "WHERE 1=1"
	args := []interface{}{}
	if since != nil {
		whereClause += " AND start_time >= ?"
		args = append(args, *since)
	}

	// Get aggregate stats
	row := s.db.QueryRow(fmt.Sprintf(`
		SELECT
			COUNT(*),
			COALESCE(SUM(request_count), 0),
			COALESCE(SUM(bytes_in), 0),
			COALESCE(SUM(bytes_out), 0),
			COALESCE(AVG(duration_ms), 0),
			COALESCE(AVG(request_count), 0)
		FROM sessions %s`, whereClause), args...)

	err := row.Scan(
		&stats.TotalSessions,
		&stats.TotalRequests,
		&stats.TotalBytesIn,
		&stats.TotalBytesOut,
		&stats.AvgDurationMs,
		&stats.AvgRequestCount,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get aggregate stats: %w", err)
	}

	// Get sessions by state
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT state, COUNT(*) FROM sessions %s GROUP BY state`, whereClause), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get state stats: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var state string
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			return nil, err
		}
		stats.SessionsByState[state] = count
	}

	// Get sessions by backend
	rows, err = s.db.Query(fmt.Sprintf(`
		SELECT backend, COUNT(*) FROM sessions %s GROUP BY backend`, whereClause), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get backend stats: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var backend string
		var count int64
		if err := rows.Scan(&backend, &count); err != nil {
			return nil, err
		}
		stats.SessionsByBackend[backend] = count
	}

	return stats, nil
}

// TimeSeriesPoint represents a point in a time series
type TimeSeriesPoint struct {
	Timestamp    time.Time `json:"timestamp"`
	SessionCount int64     `json:"session_count"`
	RequestCount int64     `json:"request_count"`
	BytesIn      int64     `json:"bytes_in"`
	BytesOut     int64     `json:"bytes_out"`
}

// GetTimeSeries retrieves time series data for the dashboard
func (s *SQLiteStore) GetTimeSeries(since time.Time, interval string) ([]TimeSeriesPoint, error) {
	// SQLite date truncation based on interval
	// Use datetime() to normalize the timestamp format first
	var dateTrunc string
	switch interval {
	case "hour":
		dateTrunc = "strftime('%Y-%m-%d %H:00:00', datetime(start_time))"
	case "day":
		dateTrunc = "strftime('%Y-%m-%d', datetime(start_time))"
	case "minute":
		dateTrunc = "strftime('%Y-%m-%d %H:%M:00', datetime(start_time))"
	default:
		dateTrunc = "strftime('%Y-%m-%d %H:00:00', datetime(start_time))" // default to hourly
	}

	// #nosec G201 -- dateTrunc is safe, only set from hardcoded switch cases above, never user input
	query := fmt.Sprintf(` // nosemgrep: string-formatted-query
		SELECT
			COALESCE(%s, 'unknown') as bucket,
			COUNT(*) as session_count,
			COALESCE(SUM(request_count), 0) as request_count,
			COALESCE(SUM(bytes_in), 0) as bytes_in,
			COALESCE(SUM(bytes_out), 0) as bytes_out
		FROM sessions
		WHERE start_time >= ?
		GROUP BY bucket
		HAVING bucket != 'unknown'
		ORDER BY bucket ASC`, dateTrunc)

	rows, err := s.db.Query(query, since)
	if err != nil {
		return nil, fmt.Errorf("failed to get time series: %w", err)
	}
	defer rows.Close()

	var points []TimeSeriesPoint
	for rows.Next() {
		var point TimeSeriesPoint
		var bucket string
		if err := rows.Scan(&bucket, &point.SessionCount, &point.RequestCount, &point.BytesIn, &point.BytesOut); err != nil {
			return nil, err
		}
		point.Timestamp, _ = time.Parse("2006-01-02 15:04:05", bucket)
		if point.Timestamp.IsZero() {
			point.Timestamp, _ = time.Parse("2006-01-02", bucket)
		}
		points = append(points, point)
	}

	return points, nil
}

// Cleanup removes old session records
func (s *SQLiteStore) Cleanup(retentionDays int) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	result, err := s.db.Exec("DELETE FROM sessions WHERE end_time < ?", cutoff)
	if err != nil {
		return 0, fmt.Errorf("failed to cleanup old sessions: %w", err)
	}

	deleted, _ := result.RowsAffected()
	if deleted > 0 {
		slog.Info("cleaned up old sessions", "deleted", deleted, "retention_days", retentionDays)
	}
	return deleted, nil
}

// Close closes the database connection
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// SaveVoiceSession saves a voice session record (CDR with transcript)
func (s *SQLiteStore) SaveVoiceSession(record VoiceSessionRecord) error {
	metadata, err := json.Marshal(record.Metadata)
	if err != nil {
		metadata = []byte("{}")
	}

	transcript, err := json.Marshal(record.Transcript)
	if err != nil {
		transcript = []byte("[]")
	}

	_, err = s.db.Exec(`
		INSERT OR REPLACE INTO voice_sessions
		(id, parent_session_id, state, start_time, answer_time, end_time, duration_ms, audio_duration_ms, turn_count, model, voice, language, protocol, audio_bytes_in, audio_bytes_out, metadata, transcript)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID,
		record.ParentSessionID,
		record.State,
		record.StartTime,
		record.AnswerTime,
		record.EndTime,
		record.DurationMs,
		record.AudioDurationMs,
		record.TurnCount,
		record.Model,
		record.Voice,
		record.Language,
		record.Protocol,
		record.AudioBytesIn,
		record.AudioBytesOut,
		string(metadata),
		string(transcript),
	)
	if err != nil {
		return fmt.Errorf("failed to save voice session: %w", err)
	}

	slog.Debug("voice session saved to history",
		"voice_session_id", record.ID,
		"parent_session_id", record.ParentSessionID,
		"state", record.State,
		"transcript_entries", len(record.Transcript),
	)
	return nil
}

// GetVoiceSession retrieves a voice session by ID
func (s *SQLiteStore) GetVoiceSession(id string) (*VoiceSessionRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, parent_session_id, state, start_time, answer_time, end_time, duration_ms, audio_duration_ms, turn_count, model, voice, language, protocol, audio_bytes_in, audio_bytes_out, metadata, transcript
		FROM voice_sessions WHERE id = ?`, id)

	var record VoiceSessionRecord
	var answerTime, endTime sql.NullTime
	var model, voice, language, protocol sql.NullString
	var metadataStr, transcriptStr sql.NullString

	err := row.Scan(
		&record.ID,
		&record.ParentSessionID,
		&record.State,
		&record.StartTime,
		&answerTime,
		&endTime,
		&record.DurationMs,
		&record.AudioDurationMs,
		&record.TurnCount,
		&model,
		&voice,
		&language,
		&protocol,
		&record.AudioBytesIn,
		&record.AudioBytesOut,
		&metadataStr,
		&transcriptStr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get voice session: %w", err)
	}

	if answerTime.Valid {
		record.AnswerTime = &answerTime.Time
	}
	if endTime.Valid {
		record.EndTime = &endTime.Time
	}
	if model.Valid {
		record.Model = model.String
	}
	if voice.Valid {
		record.Voice = voice.String
	}
	if language.Valid {
		record.Language = language.String
	}
	if protocol.Valid {
		record.Protocol = protocol.String
	}
	if metadataStr.Valid && metadataStr.String != "" {
		json.Unmarshal([]byte(metadataStr.String), &record.Metadata)
	}
	if transcriptStr.Valid && transcriptStr.String != "" {
		json.Unmarshal([]byte(transcriptStr.String), &record.Transcript)
	}

	return &record, nil
}

// ListVoiceSessionsOptions contains options for listing voice sessions
type ListVoiceSessionsOptions struct {
	Limit           int
	Offset          int
	ParentSessionID string // Filter by parent HTTP session
	State           string
	Since           *time.Time
	Until           *time.Time
}

// ListVoiceSessions retrieves voice sessions with filtering and pagination
func (s *SQLiteStore) ListVoiceSessions(opts ListVoiceSessionsOptions) ([]VoiceSessionRecord, error) {
	query := `
		SELECT id, parent_session_id, state, start_time, answer_time, end_time, duration_ms, audio_duration_ms, turn_count, model, voice, language, protocol, audio_bytes_in, audio_bytes_out, metadata, transcript
		FROM voice_sessions WHERE 1=1`

	args := []interface{}{}

	if opts.ParentSessionID != "" {
		query += " AND parent_session_id = ?"
		args = append(args, opts.ParentSessionID)
	}
	if opts.State != "" {
		query += " AND state = ?"
		args = append(args, opts.State)
	}
	if opts.Since != nil {
		query += " AND start_time >= ?"
		args = append(args, *opts.Since)
	}
	if opts.Until != nil {
		query += " AND start_time <= ?"
		args = append(args, *opts.Until)
	}

	query += " ORDER BY start_time DESC"

	if opts.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, opts.Limit)
	}
	if opts.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, opts.Offset)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list voice sessions: %w", err)
	}
	defer rows.Close()

	var records []VoiceSessionRecord
	for rows.Next() {
		var record VoiceSessionRecord
		var answerTime, endTime sql.NullTime
		var model, voice, language, protocol sql.NullString
		var metadataStr, transcriptStr sql.NullString

		err := rows.Scan(
			&record.ID,
			&record.ParentSessionID,
			&record.State,
			&record.StartTime,
			&answerTime,
			&endTime,
			&record.DurationMs,
			&record.AudioDurationMs,
			&record.TurnCount,
			&model,
			&voice,
			&language,
			&protocol,
			&record.AudioBytesIn,
			&record.AudioBytesOut,
			&metadataStr,
			&transcriptStr,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan voice session: %w", err)
		}

		if answerTime.Valid {
			record.AnswerTime = &answerTime.Time
		}
		if endTime.Valid {
			record.EndTime = &endTime.Time
		}
		if model.Valid {
			record.Model = model.String
		}
		if voice.Valid {
			record.Voice = voice.String
		}
		if language.Valid {
			record.Language = language.String
		}
		if protocol.Valid {
			record.Protocol = protocol.String
		}
		if metadataStr.Valid && metadataStr.String != "" {
			json.Unmarshal([]byte(metadataStr.String), &record.Metadata)
		}
		if transcriptStr.Valid && transcriptStr.String != "" {
			json.Unmarshal([]byte(transcriptStr.String), &record.Transcript)
		}

		records = append(records, record)
	}

	return records, nil
}

// GetVoiceSessionsByParent retrieves all voice sessions for a parent HTTP session
func (s *SQLiteStore) GetVoiceSessionsByParent(parentSessionID string) ([]VoiceSessionRecord, error) {
	return s.ListVoiceSessions(ListVoiceSessionsOptions{
		ParentSessionID: parentSessionID,
	})
}

// VoiceStats represents aggregate statistics for voice sessions
type VoiceStats struct {
	TotalSessions      int64            `json:"total_sessions"`
	TotalAudioMs       int64            `json:"total_audio_ms"`
	TotalTurns         int64            `json:"total_turns"`
	AvgDurationMs      float64          `json:"avg_duration_ms"`
	AvgTurnsPerSession float64          `json:"avg_turns_per_session"`
	SessionsByState    map[string]int64 `json:"sessions_by_state"`
	SessionsByModel    map[string]int64 `json:"sessions_by_model"`
}

// GetVoiceStats retrieves aggregate statistics for voice sessions
func (s *SQLiteStore) GetVoiceStats(since *time.Time) (*VoiceStats, error) {
	stats := &VoiceStats{
		SessionsByState: make(map[string]int64),
		SessionsByModel: make(map[string]int64),
	}

	whereClause := "WHERE 1=1"
	args := []interface{}{}
	if since != nil {
		whereClause += " AND start_time >= ?"
		args = append(args, *since)
	}

	row := s.db.QueryRow(fmt.Sprintf(`
		SELECT
			COUNT(*),
			COALESCE(SUM(audio_duration_ms), 0),
			COALESCE(SUM(turn_count), 0),
			COALESCE(AVG(duration_ms), 0),
			COALESCE(AVG(turn_count), 0)
		FROM voice_sessions %s`, whereClause), args...)

	err := row.Scan(
		&stats.TotalSessions,
		&stats.TotalAudioMs,
		&stats.TotalTurns,
		&stats.AvgDurationMs,
		&stats.AvgTurnsPerSession,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get voice stats: %w", err)
	}

	// Sessions by state
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT state, COUNT(*) FROM voice_sessions %s GROUP BY state`, whereClause), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get voice state stats: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var state string
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			return nil, err
		}
		stats.SessionsByState[state] = count
	}

	// Sessions by model
	rows, err = s.db.Query(fmt.Sprintf(`
		SELECT COALESCE(model, 'unknown'), COUNT(*) FROM voice_sessions %s GROUP BY model`, whereClause), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get voice model stats: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var model string
		var count int64
		if err := rows.Scan(&model, &count); err != nil {
			return nil, err
		}
		stats.SessionsByModel[model] = count
	}

	return stats, nil
}

// SaveTTSRequest saves a TTS request record
func (s *SQLiteStore) SaveTTSRequest(req TTSRequest) error {
	_, err := s.db.Exec(`
		INSERT INTO tts_requests
		(id, session_id, timestamp, provider, model, voice, text, text_length, response_bytes, duration_ms, status_code)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ID,
		req.SessionID,
		req.Timestamp,
		req.Provider,
		req.Model,
		req.Voice,
		req.Text,
		req.TextLength,
		req.ResponseBytes,
		req.DurationMs,
		req.StatusCode,
	)
	if err != nil {
		return fmt.Errorf("failed to save TTS request: %w", err)
	}

	slog.Debug("TTS request saved",
		"id", req.ID,
		"session_id", req.SessionID,
		"provider", req.Provider,
		"text_length", req.TextLength,
	)
	return nil
}

// ListTTSRequestsOptions contains options for listing TTS requests
type ListTTSRequestsOptions struct {
	Limit     int
	Offset    int
	SessionID string
	Provider  string
	Since     *time.Time
	Until     *time.Time
}

// ListTTSRequests retrieves TTS requests with filtering
func (s *SQLiteStore) ListTTSRequests(opts ListTTSRequestsOptions) ([]TTSRequest, error) {
	query := `
		SELECT id, session_id, timestamp, provider, model, voice, text, text_length, response_bytes, duration_ms, status_code
		FROM tts_requests WHERE 1=1`

	args := []interface{}{}

	if opts.SessionID != "" {
		query += " AND session_id = ?"
		args = append(args, opts.SessionID)
	}
	if opts.Provider != "" {
		query += " AND provider = ?"
		args = append(args, opts.Provider)
	}
	if opts.Since != nil {
		query += " AND timestamp >= ?"
		args = append(args, *opts.Since)
	}
	if opts.Until != nil {
		query += " AND timestamp <= ?"
		args = append(args, *opts.Until)
	}

	query += " ORDER BY timestamp DESC"

	if opts.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, opts.Limit)
	}
	if opts.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, opts.Offset)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list TTS requests: %w", err)
	}
	defer rows.Close()

	var requests []TTSRequest
	for rows.Next() {
		var req TTSRequest
		var model, voice, text sql.NullString

		err := rows.Scan(
			&req.ID,
			&req.SessionID,
			&req.Timestamp,
			&req.Provider,
			&model,
			&voice,
			&text,
			&req.TextLength,
			&req.ResponseBytes,
			&req.DurationMs,
			&req.StatusCode,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan TTS request: %w", err)
		}

		if model.Valid {
			req.Model = model.String
		}
		if voice.Valid {
			req.Voice = voice.String
		}
		if text.Valid {
			req.Text = text.String
		}

		requests = append(requests, req)
	}

	return requests, nil
}

// GetTTSRequestsBySession retrieves all TTS requests for a session
func (s *SQLiteStore) GetTTSRequestsBySession(sessionID string) ([]TTSRequest, error) {
	return s.ListTTSRequests(ListTTSRequestsOptions{
		SessionID: sessionID,
	})
}

// TTSStats represents aggregate statistics for TTS requests
type TTSStats struct {
	TotalRequests      int64            `json:"total_requests"`
	TotalCharacters    int64            `json:"total_characters"`
	TotalResponseBytes int64            `json:"total_response_bytes"`
	AvgTextLength      float64          `json:"avg_text_length"`
	RequestsByProvider map[string]int64 `json:"requests_by_provider"`
	RequestsByVoice    map[string]int64 `json:"requests_by_voice"`
}

// GetTTSStats retrieves aggregate TTS statistics
func (s *SQLiteStore) GetTTSStats(since *time.Time) (*TTSStats, error) {
	stats := &TTSStats{
		RequestsByProvider: make(map[string]int64),
		RequestsByVoice:    make(map[string]int64),
	}

	whereClause := "WHERE 1=1"
	args := []interface{}{}
	if since != nil {
		whereClause += " AND timestamp >= ?"
		args = append(args, *since)
	}

	row := s.db.QueryRow(fmt.Sprintf(`
		SELECT
			COUNT(*),
			COALESCE(SUM(text_length), 0),
			COALESCE(SUM(response_bytes), 0),
			COALESCE(AVG(text_length), 0)
		FROM tts_requests %s`, whereClause), args...)

	err := row.Scan(
		&stats.TotalRequests,
		&stats.TotalCharacters,
		&stats.TotalResponseBytes,
		&stats.AvgTextLength,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get TTS stats: %w", err)
	}

	// Requests by provider
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT provider, COUNT(*) FROM tts_requests %s GROUP BY provider`, whereClause), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get TTS provider stats: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var provider string
		var count int64
		if err := rows.Scan(&provider, &count); err != nil {
			return nil, err
		}
		stats.RequestsByProvider[provider] = count
	}

	// Requests by voice
	rows, err = s.db.Query(fmt.Sprintf(`
		SELECT COALESCE(voice, 'unknown'), COUNT(*) FROM tts_requests %s GROUP BY voice`, whereClause), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get TTS voice stats: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var voice string
		var count int64
		if err := rows.Scan(&voice, &count); err != nil {
			return nil, err
		}
		stats.RequestsByVoice[voice] = count
	}

	return stats, nil
}
