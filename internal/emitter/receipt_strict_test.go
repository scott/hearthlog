package emitter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func TestReceiptStrictJSON(t *testing.T) {
	cases := []struct {
		name    string
		prefix  string
		tail    string
		size    int
		wantErr bool
	}{
		{"dup_version_null", `{"version":null,"version":0,`, "", 0, true},
		{"dup_version_0", `{"version":0,"version":null,`, "", 0, true},
		{"dup_version_0b", `{"version":0,"version":0,`, "", 0, true},
		{"dup_version_escaped", `{"version":0,"vers\u0069on":0,`, "", 0, true},
		{"dup_accepted", `{"version":0,"accepted":1,"accepted":1,`, "", 0, true},
		{"valid_explicit", `{"version":0,"first_ingest_id":"1","last_ingest_id":"1","received_at":"2026-01-02T03:04:06Z",`, "", 0, false},
		{"version_overflow", `{"version":9223372036854775808,`, "", 0, true},
		{"invalid_utf8", `{"version":0,"first_ingest_id":"\xff",`, "", 0, true},
		{"version_neg_overflow", `{"version":-9223372036854775809,`, "", 0, true},
		{"version_null_uppercase", `{"version":null,"Version":0,`, "", 0, true},
		{"uppercase_version_only", `{"Version":0,`, "", 0, true},
		{"unpaired_surrogate", `{"version":0,"first_ingest_id":"\ud800",`, "", 0, true},
		{"unknown_field", `{"version":0,"unknown":"x",`, "", 0, true},
		{"trailing_root", `{"version":0,`, `{}`, 0, true},
		{"valid_4096", `{"version":0,`, "", 4096, false},
		{"valid_4097_ws", `{"version":0,`, "", 4097, true},
		{"garbage_beyond_4096", `{"version":0,`, `x`, 4096, true},
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
				resp := fmt.Sprintf(`%s"protocol":"%s","batch_id":"%s","accepted":1}`, tc.prefix, model.SkeletonProtocol, gotBatch.BatchID)
				if tc.name == "invalid_utf8" {
					resp = strings.Replace(resp, `\xff`, string([]byte{0xff}), 1)
				}
				if tc.size > 0 {
					resp += strings.Repeat(" ", tc.size-len(resp))
				}
				resp += tc.tail
				if tc.size > 0 && len(resp) != tc.size+len(tc.tail) {
					t.Errorf("wire length = %d, want %d", len(resp), tc.size+len(tc.tail))
				}
				if tc.name == "invalid_utf8" && !strings.Contains(resp, string([]byte{0xff})) {
					t.Errorf("wire missing 0xff byte")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(resp))
			}))
			defer srv.Close()

			h, err := New(srv.URL+"/api/experimental/skeleton/batches", "test-token", nil)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			logger := slog.New(h)
			logger.Info("synthetic receipt probe", "case", tc.name)

			flushCtx, flushCancel := context.WithTimeout(context.Background(), time.Second)
			defer flushCancel()
			flushErr := h.Flush(flushCtx)

			closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
			defer closeCancel()
			closeErr := h.Close(closeCtx)

			if tc.wantErr {
				if flushErr == nil {
					t.Errorf("Flush: expected error, got nil")
				}
				if closeErr == nil {
					t.Errorf("Close: expected error, got nil")
				}
				if flushCtx.Err() != nil {
					t.Errorf("Flush: context expired: %v", flushCtx.Err())
				}
				if closeCtx.Err() != nil {
					t.Errorf("Close: context expired: %v", closeCtx.Err())
				}
				if errors.Is(flushErr, context.DeadlineExceeded) || errors.Is(flushErr, context.Canceled) {
					t.Errorf("Flush: context error, want validation error")
				}
				if errors.Is(closeErr, context.DeadlineExceeded) || errors.Is(closeErr, context.Canceled) {
					t.Errorf("Close: context error, want validation error")
				}
				if got := h.Stats().SentBatches; got != 0 {
					t.Errorf("sent = %d, want 0", got)
				}
			} else {
				if flushErr != nil {
					t.Errorf("Flush: unexpected error: %v", flushErr)
				}
				if closeErr != nil {
					t.Errorf("Close: unexpected error: %v", closeErr)
				}
				if got := h.Stats().SentBatches; got != 1 {
					t.Errorf("sent = %d, want 1", got)
				}
			}

			if got := hits.Load(); got != 1 {
				t.Errorf("request count = %d, want exactly 1", got)
			}
		})
	}
}
