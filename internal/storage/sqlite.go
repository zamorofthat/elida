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
type SessionRecord struct {
	ID           string            `json:"id"`
	State        string            `json:"state"`
	StartTime    time.Time         `json:"start_time"`
	EndTime      time.Time         `json:"end_time"`
	DurationMs   int64             `json:"duration_ms"`
	RequestCount int               `json:"request_count"`
	BytesIn      int64             `json:"bytes_in"`
	BytesOut     int64             `json:"bytes_out"`
	Backend      string            `json:"backend"`
	ClientAddr   string            `json:"client_addr"`
	Metadata     map[string]string `json:"metadata,omitempty"`
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
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_sessions_start_time ON sessions(start_time);
	CREATE INDEX IF NOT EXISTS idx_sessions_end_time ON sessions(end_time);
	CREATE INDEX IF NOT EXISTS idx_sessions_state ON sessions(state);
	CREATE INDEX IF NOT EXISTS idx_sessions_backend ON sessions(backend);
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

	_, err = s.db.Exec(`
		INSERT OR REPLACE INTO sessions
		(id, state, start_time, end_time, duration_ms, request_count, bytes_in, bytes_out, backend, client_addr, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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
	)
	if err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}

	slog.Debug("session saved to history", "session_id", record.ID, "state", record.State)
	return nil
}

// GetSession retrieves a session by ID
func (s *SQLiteStore) GetSession(id string) (*SessionRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, state, start_time, end_time, duration_ms, request_count, bytes_in, bytes_out, backend, client_addr, metadata
		FROM sessions WHERE id = ?`, id)

	var record SessionRecord
	var metadataStr string
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
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	if metadataStr != "" {
		json.Unmarshal([]byte(metadataStr), &record.Metadata)
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
		SELECT id, state, start_time, end_time, duration_ms, request_count, bytes_in, bytes_out, backend, client_addr, metadata
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
		var metadataStr string
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
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan session: %w", err)
		}

		if metadataStr != "" {
			json.Unmarshal([]byte(metadataStr), &record.Metadata)
		}

		records = append(records, record)
	}

	return records, nil
}

// Stats represents aggregate statistics
type Stats struct {
	TotalSessions   int64   `json:"total_sessions"`
	TotalRequests   int64   `json:"total_requests"`
	TotalBytesIn    int64   `json:"total_bytes_in"`
	TotalBytesOut   int64   `json:"total_bytes_out"`
	AvgDurationMs   float64 `json:"avg_duration_ms"`
	AvgRequestCount float64 `json:"avg_request_count"`
	SessionsByState map[string]int64 `json:"sessions_by_state"`
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

	query := fmt.Sprintf(`
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
