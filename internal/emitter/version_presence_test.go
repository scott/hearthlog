package emitter

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func TestVersionPresence(t *testing.T) {
	cases := []struct {
		name    string
		version json.RawMessage
		wantErr bool
	}{
		{"missing", nil, true},
		{"null", json.RawMessage("null"), true},
		{"string", json.RawMessage(`"0"`), true},
		{"true", json.RawMessage("true"), true},
		{"array", json.RawMessage("[]"), true},
		{"object", json.RawMessage("{}"), true},
		{"float0", json.RawMessage("0.0"), true},
		{"float05", json.RawMessage("0.5"), true},
		{"one", json.RawMessage("1"), true},
		{"neg1", json.RawMessage("-1"), true},
		{"exp1", json.RawMessage("1e0"), true},
		{"valid0", json.RawMessage("0"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			var gotBatch model.Batch

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
				if err := json.Unmarshal(body, &gotBatch); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				resp := map[string]any{
					"protocol": model.SkeletonProtocol,
					"batch_id": gotBatch.BatchID,
					"accepted": 1,
				}
				if tc.version != nil {
					resp["version"] = tc.version
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer srv.Close()

			h, err := New(srv.URL+"/api/experimental/skeleton/batches", "test-token", nil)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			logger := slog.New(h)
			logger.Info("synthetic version probe", "case", tc.name)

			flushCtx, flushCancel := context.WithTimeout(context.Background(), time.Second)
			defer flushCancel()
			flushErr := h.Flush(flushCtx)
			if flushCtx.Err() != nil {
				t.Errorf("Flush: context deadline exceeded: %v", flushCtx.Err())
			}

			closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
			defer closeCancel()
			closeErr := h.Close(closeCtx)
			if closeCtx.Err() != nil {
				t.Errorf("Close: context deadline exceeded: %v", closeCtx.Err())
			}

			if tc.wantErr {
				if flushErr == nil {
					t.Errorf("Flush: expected error for %s, got nil", tc.name)
				}
				if closeErr == nil {
					t.Errorf("Close: expected error for %s, got nil", tc.name)
				}
				if got := h.state.sent.Load(); got != 0 {
					t.Errorf("sent = %d, want 0 for %s", got, tc.name)
				}
			} else {
				if flushErr != nil {
					t.Errorf("Flush: unexpected error for %s: %v", tc.name, flushErr)
				}
				if closeErr != nil {
					t.Errorf("Close: unexpected error for %s: %v", tc.name, closeErr)
				}
				if got := h.state.sent.Load(); got != 1 {
					t.Errorf("sent = %d, want 1 for %s", got, tc.name)
				}
			}

			if got := hits.Load(); got != 1 {
				t.Errorf("request count = %d, want exactly 1 (no retry) for %s", got, tc.name)
			}
		})
	}
}
