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

// The endpoint path selects the envelope. The canonical native route is a
// valid SDK target (Slice 03), even though this repository's server keeps it
// unavailable until Slice 04; any other path is rejected.
func TestEndpointPathSelectsProtocol(t *testing.T) {
	for _, tc := range []struct {
		path   string
		ok     bool
		native bool
	}{
		{"/api/v1/batches", true, true},
		{"/api/experimental/skeleton/batches", true, false},
		{"/api/v2/batches", false, false},
		{"/api/v1/batches/", false, false},
		{"/", false, false},
	} {
		h, err := New("http://127.0.0.1:8080"+tc.path, "test-token", nil)
		if (err == nil) != tc.ok {
			t.Fatalf("New(%q): err=%v, want ok=%v", tc.path, err, tc.ok)
		}
		if err != nil {
			continue
		}
		if h.state.native != tc.native {
			t.Fatalf("New(%q): native=%v want %v", tc.path, h.state.native, tc.native)
		}
		_ = h.Close(context.Background())
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
