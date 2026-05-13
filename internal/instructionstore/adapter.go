package instructionstore

import (
	"time"

	"elida/internal/instruction"
	"elida/internal/storage"
)

// SQLiteAdapter adapts storage.SQLiteStore to the instruction.Store interface.
type SQLiteAdapter struct {
	store *storage.SQLiteStore
}

// NewSQLiteAdapter wraps a SQLiteStore to satisfy the instruction.Store interface.
func NewSQLiteAdapter(store *storage.SQLiteStore) *SQLiteAdapter {
	return &SQLiteAdapter{store: store}
}

func (a *SQLiteAdapter) GetInstructionFile(hash string) (*instruction.Record, error) {
	return a.store.GetInstructionFile(hash)
}

func (a *SQLiteAdapter) SaveInstructionFile(record instruction.Record) error {
	return a.store.SaveInstructionFile(record)
}

func (a *SQLiteAdapter) IncrementInstructionFileSessionCount(hash string, lastSeen time.Time) error {
	return a.store.IncrementInstructionFileSessionCount(hash, lastSeen)
}

func (a *SQLiteAdapter) SaveEvent(evt instruction.Event) error {
	return a.store.SaveInstructionEvent(storage.InstructionEvent{
		Timestamp: evt.Timestamp,
		EventType: evt.EventType,
		SessionID: evt.SessionID,
		Severity:  evt.Severity,
		Data:      evt.Data,
	})
}
