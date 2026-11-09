package server

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
	"example.invalid/hearthlog/internal/storage/sqlite"
)

func seedResultBytes(t *testing.T, store *sqlite.Store, count int, message string) model.Query {
	t.Helper()

	now := time.Now().UTC().Truncate(time.Microsecond)
	timeStr := now.Format(time.RFC3339Nano)

	for i := 0; i < count; i++ {
		batchID := fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
		eventID := fmt.Sprintf("00000000-0000-4000-8000-%012d", i+101)

		b := model.Batch{
			Version: 0,
			BatchID: batchID,
			Events: []model.Event{
				{
					EventID:        eventID,
					EventTime:      timeStr,
					SeverityNumber: 9,
					SeverityText:   "INFO",
					Message:        message,
					Attributes:     []byte(`{"kind":"bounded"}`),
				},
			},
		}

		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("marshal batch %d: %v", i, err)
		}

		validated, err := model.ValidateBatch(raw, now)
		if err != nil {
			t.Fatalf("validate batch %d: %v", i, err)
		}

		receipt, err := store.Commit(t.Context(), model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, validated)
		if err != nil {
			t.Fatalf("commit batch %d: %v", i, err)
		}
		if receipt.Accepted != 1 {
			t.Fatalf("batch %d accepted %d, want 1", i, receipt.Accepted)
		}
	}

	return model.Query{
		Sources:     []string{"src_demo_api"},
		From:        now.Add(-time.Minute).Format(time.RFC3339Nano),
		To:          now.Add(time.Minute).Format(time.RFC3339Nano),
		MinSeverity: 1,
		Terms:       []string{"boundedmarker"},
		Limit:       200,
	}
}
