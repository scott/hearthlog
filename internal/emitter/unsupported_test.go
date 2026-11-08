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

func TestUnsupportedEndpointRejectsCanonicalV1(t *testing.T) {
	_, err := New("http://127.0.0.1:8080/api/v1/batches", "test-token", nil)
	if err == nil {
		t.Fatal("New: expected error for canonical v1 path, got nil")
	}
}

func TestUnsupportedEndpointReturns501NoRetry(t *testing.T) {
	var hits atomic.Int32
	var captured []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		captured, _ = io.ReadAll(io.LimitReader(r.Body, 64*1024))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"version":0,"protocol":"experimental-skeleton","error":{"code":"replay_not_supported","message":"replay not supported","retryable":false}}`))
	}))
	defer srv.Close()

	h, err := New(srv.URL+"/api/experimental/skeleton/batches", "test-token", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logger := slog.New(h)
	logger.Info("synthetic one event", "task", "demo")

	flushCtx, flushCancel := context.WithTimeout(context.Background(), time.Second)
	defer flushCancel()
	if err := h.Flush(flushCtx); err == nil {
		t.Fatal("Flush: expected error on 501, got nil")
	}

	closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer closeCancel()
	if err := h.Close(closeCtx); err == nil {
		t.Fatal("Close: expected error due to failure, got nil")
	}

	if got := hits.Load(); got != 1 {
		t.Fatalf("request count = %d, want exactly 1 (no retry)", got)
	}

	if len(captured) == 0 {
		t.Fatal("no request body captured")
	}

	var batch model.Batch
	if err := json.Unmarshal(captured, &batch); err != nil {
		t.Fatalf("unmarshal captured batch: %v", err)
	}

	if batch.Version != model.SkeletonVersion {
		t.Errorf("batch version = %d, want %d", batch.Version, model.SkeletonVersion)
	}
	if batch.BatchID == "" {
		t.Error("batch ID is empty")
	}
	if len(batch.Events) != 1 {
		t.Fatalf("event count = %d, want 1", len(batch.Events))
	}
	ev := batch.Events[0]
	if ev.EventID == "" {
		t.Error("event ID is empty")
	}
}
