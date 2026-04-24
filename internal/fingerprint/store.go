package fingerprint

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// BaselineStore persists baselines across restarts.
type BaselineStore interface {
	Load(cfg BaselineConfig) (map[string]*Baseline, error)
	Save(baselines map[string]*Baseline) error
	Close() error
}

// SQLiteBaselineStore persists baselines to a SQLite database.
type SQLiteBaselineStore struct {
	db *sql.DB
}

// NewSQLiteBaselineStore creates a baseline store using an existing database handle.
// It creates the fingerprint_baselines table if it doesn't exist.
func NewSQLiteBaselineStore(db *sql.DB) (*SQLiteBaselineStore, error) {
	schema := `
	CREATE TABLE IF NOT EXISTS fingerprint_baselines (
		class TEXT PRIMARY KEY,
		count INTEGER NOT NULL DEFAULT 0,
		mean_json TEXT NOT NULL,
		covariance_json TEXT NOT NULL,
		quantiles_json TEXT NOT NULL,
		updated_at DATETIME NOT NULL
	);`

	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		return nil, fmt.Errorf("failed to create fingerprint_baselines table: %w", err)
	}

	return &SQLiteBaselineStore{db: db}, nil
}

// storedQuantiles holds the serialized P2 quantile estimators.
type storedQuantiles struct {
	Low  [NumFeatures]*P2Quantile `json:"low"`
	High [NumFeatures]*P2Quantile `json:"high"`
}

// Load reads all baselines from the database.
func (s *SQLiteBaselineStore) Load(cfg BaselineConfig) (map[string]*Baseline, error) {
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT class, count, mean_json, covariance_json, quantiles_json, updated_at
		FROM fingerprint_baselines`)
	if err != nil {
		return nil, fmt.Errorf("failed to query baselines: %w", err)
	}
	defer func() { _ = rows.Close() }()

	baselines := make(map[string]*Baseline)
	for rows.Next() {
		var (
			class                            string
			count                            int
			meanJSON, covJSON, quantilesJSON string
			updatedAt                        time.Time
		)
		if err := rows.Scan(&class, &count, &meanJSON, &covJSON, &quantilesJSON, &updatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan baseline row: %w", err)
		}

		b := NewBaseline(class, cfg)
		b.Count = count
		b.UpdatedAt = updatedAt

		if err := json.Unmarshal([]byte(meanJSON), &b.Mean); err != nil {
			slog.Warn("failed to unmarshal baseline mean", "class", class, "error", err)
			continue
		}
		if err := json.Unmarshal([]byte(covJSON), &b.Covariance); err != nil {
			slog.Warn("failed to unmarshal baseline covariance", "class", class, "error", err)
			continue
		}

		var sq storedQuantiles
		if err := json.Unmarshal([]byte(quantilesJSON), &sq); err != nil {
			slog.Warn("failed to unmarshal baseline quantiles", "class", class, "error", err)
			continue
		}
		for i := 0; i < NumFeatures; i++ {
			if sq.Low[i] != nil {
				b.Low[i] = sq.Low[i]
			}
			if sq.High[i] != nil {
				b.High[i] = sq.High[i]
			}
		}

		baselines[class] = b
	}

	slog.Info("loaded fingerprint baselines", "count", len(baselines))
	return baselines, nil
}

// Save persists all baselines to the database.
func (s *SQLiteBaselineStore) Save(baselines map[string]*Baseline) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO fingerprint_baselines
		(class, count, mean_json, covariance_json, quantiles_json, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, b := range baselines {
		b.mu.RLock()

		meanJSON, err := json.Marshal(b.Mean)
		if err != nil {
			b.mu.RUnlock()
			return fmt.Errorf("failed to marshal mean for class %s: %w", b.Class, err)
		}

		covJSON, err := json.Marshal(b.Covariance)
		if err != nil {
			b.mu.RUnlock()
			return fmt.Errorf("failed to marshal covariance for class %s: %w", b.Class, err)
		}

		sq := storedQuantiles{Low: b.Low, High: b.High}
		quantilesJSON, err := json.Marshal(sq)
		if err != nil {
			b.mu.RUnlock()
			return fmt.Errorf("failed to marshal quantiles for class %s: %w", b.Class, err)
		}

		_, err = stmt.ExecContext(ctx, b.Class, b.Count, string(meanJSON), string(covJSON), string(quantilesJSON), b.UpdatedAt)
		b.mu.RUnlock()
		if err != nil {
			return fmt.Errorf("failed to save baseline for class %s: %w", b.Class, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit baselines: %w", err)
	}

	slog.Debug("saved fingerprint baselines", "count", len(baselines))
	return nil
}

// Close is a no-op since the database handle is owned by the caller.
func (s *SQLiteBaselineStore) Close() error {
	return nil
}
