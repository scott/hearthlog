package sqlite

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func freshStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	dbPath := filepath.Join(state, "test.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	})
	return store, dbPath
}

func negativeBatch(t *testing.T, n int, message string) model.Batch {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	events := make([]model.Event, 0, n)
	for i := 0; i < n; i++ {
		batchID := fmt.Sprintf("%08x-0000-4000-8000-%012d", i, i)
		eventID := fmt.Sprintf("%08x-0000-4000-8000-%012d", i+1000, i)
		attrs := json.RawMessage(`{"int":1,"real":1.5,"bool":true}`)
		events = append(events, model.Event{
			EventID:        eventID,
			EventTime:      now.Format(time.RFC3339Nano),
			SeverityNumber: 9,
			SeverityText:   "INFO",
			Message:        message,
			Attributes:     attrs,
		})
		_ = batchID
	}
	batch := model.Batch{Version: 0, BatchID: fmt.Sprintf("%08x-0000-4000-8000-%012d", 0, 0), Events: events}
	raw, err := json.Marshal(batch)
	if err != nil {
		t.Fatalf("marshal batch: %v", err)
	}
	validated, err := model.ValidateBatch(raw, now)
	if err != nil {
		t.Fatalf("validate batch: %v", err)
	}
	return validated
}

func negativeQuery(term string) model.Query {
	now := time.Now().UTC()
	return model.Query{
		Sources:     []string{"src_demo_api"},
		From:        now.Add(-time.Minute).Format(time.RFC3339),
		To:          now.Add(time.Minute).Format(time.RFC3339),
		MinSeverity: 1,
		Terms:       []string{term},
		Limit:       200,
	}
}
