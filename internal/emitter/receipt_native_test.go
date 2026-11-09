package emitter

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNativeReceiptDecodeTypes(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(map[string]any)
		wantErr bool
	}{
		{"ok", func(map[string]any) {}, false},
		{"first_number", func(m map[string]any) { m["first_ingest_id"] = 1 }, true},
		{"last_number", func(m map[string]any) { m["last_ingest_id"] = 2 }, true},
		{"replay_number", func(m map[string]any) { m["replay_until"] = 1700000000 }, true},
		{"first_null", func(m map[string]any) { m["first_ingest_id"] = nil }, true},
		{"missing_replay", func(m map[string]any) { delete(m, "replay_until") }, true},
		{"missing_first", func(m map[string]any) { delete(m, "first_ingest_id") }, true},
		{"missing_last", func(m map[string]any) { delete(m, "last_ingest_id") }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
				var b struct {
					BatchID string            `json:"batch_id"`
					Events  []json.RawMessage `json:"events"`
				}
				_ = json.Unmarshal(body, &b)
				rec := map[string]any{
					"version": 1, "batch_id": b.BatchID, "accepted": len(b.Events),
					"first_ingest_id": "1", "last_ingest_id": "1",
					"received_at": "2026-01-02T03:04:06Z", "replay_until": "2026-01-04T03:04:06Z",
				}
				tc.mutate(rec)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(rec)
			}))
			defer srv.Close()
			h, err := New(srv.URL+NativePath, "synthetic-token", nil)
			if err != nil {
				t.Fatal(err)
			}
			slog.New(h).Info("synthetic receipt type")
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			flushErr := h.Flush(ctx)
			_ = h.Close(ctx)
			if hits != 1 {
				t.Fatalf("requests %d", hits)
			}
			if tc.wantErr {
				if flushErr == nil {
					t.Fatal("expected receipt rejection")
				}
				if h.Stats().AckedEvents != 0 {
					t.Fatalf("acked %d", h.Stats().AckedEvents)
				}
				if h.Stats().Dropped["invalid_receipt"]["INFO"] != 1 {
					t.Fatalf("dropped %+v", h.Stats().Dropped)
				}
				return
			}
			if flushErr != nil {
				t.Fatal(flushErr)
			}
			if h.Stats().AckedEvents != 1 {
				t.Fatalf("acked %d", h.Stats().AckedEvents)
			}
		})
	}
}
