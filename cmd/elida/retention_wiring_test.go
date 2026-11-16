package main

import (
	"testing"

	"elida/internal/config"
	"elida/internal/storage"
)

// The sweep enforces what the config says: history by storage.retention_days,
// events by their own period, or by the history period when theirs is unset.
func TestRetentionPolicy_FromStorageConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	a := &app{cfg: cfg}

	if got, want := a.retentionPolicy(), (storage.RetentionPolicy{HistoryDays: 30, EventDays: 30}); got != want {
		t.Errorf("default policy = %+v, want %+v", got, want)
	}

	cfg.Storage.RetentionDays = 7
	cfg.Storage.Events.RetentionDays = 90
	if got, want := a.retentionPolicy(), (storage.RetentionPolicy{HistoryDays: 7, EventDays: 90}); got != want {
		t.Errorf("policy = %+v, want %+v", got, want)
	}
}
