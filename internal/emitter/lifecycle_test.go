package emitter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func TestLifecycleFlushClose(t *testing.T) {
	var (
		reqCount    atomic.Int64
		entered     = make(chan struct{})
		release     = make(chan struct{})
		releaseOnce sync.Once
	)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		if reqCount.Load() == 1 {
			close(entered)
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		var b model.Batch
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		resp := map[string]any{
			"version":  model.SkeletonVersion,
			"protocol": model.SkeletonProtocol,
			"batch_id": b.BatchID,
			"accepted": len(b.Events),
		}
		json.NewEncoder(w).Encode(resp)
	})

	srv := httptest.NewServer(handler)
	defer func() {
		releaseOnce.Do(func() { close(release) })
		srv.Close()
	}()

	h, err := New(srv.URL+"/api/experimental/skeleton/batches", "tok", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// 1. Flush while in-flight request is blocked
	if err := h.Handle(context.Background(), slogRecord("first")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for first request")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := h.Flush(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Flush: want DeadlineExceeded, got %v", err)
	}

	// 2. Fill queue to capacity (128) + 1 to trigger drop
	for i := 0; i < 129; i++ {
		_ = h.Handle(context.Background(), slogRecord(fmt.Sprintf("fill-%d", i)))
	}
	h.state.mu.Lock()
	pending := h.state.pending
	qlen := len(h.state.queue)
	h.state.mu.Unlock()
	if pending > 129 {
		t.Fatalf("pending %d > 129", pending)
	}
	if qlen > 128 {
		t.Fatalf("queue len %d > 128", qlen)
	}
	if h.state.drops.Load() == 0 {
		t.Fatal("expected drops > 0")
	}

	// 3. Close while still blocked
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	if err := h.Close(ctx2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close: want DeadlineExceeded, got %v", err)
	}

	// 4. Release and close with bounded timeout
	releaseOnce.Do(func() { close(release) })
	ctx3, cancel3 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel3()
	_ = h.Close(ctx3)

	select {
	case <-h.state.done:
	default:
		t.Fatal("done channel not closed")
	}
}

func TestConcurrentHandleCloseNoPanic(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var b model.Batch
		json.NewDecoder(r.Body).Decode(&b)
		json.NewEncoder(w).Encode(map[string]any{"version": model.SkeletonVersion,
			"protocol": model.SkeletonProtocol, "batch_id": b.BatchID, "accepted": len(b.Events)})
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	h, err := New(srv.URL+"/api/experimental/skeleton/batches", "tok", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				_ = h.Handle(context.Background(), slogRecord("conc"))
			}
		}()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.Close(ctx) }()

	wg.Wait()

	select {
	case err := <-done:
		if err != nil {
			if h.state.drops.Load() == 0 {
				t.Fatalf("Close: unexpected error with no drops: %v", err)
			}
		}
	case <-ctx.Done():
		t.Fatal("Close timed out")
	}
	select {
	case <-h.state.done:
	default:
		t.Fatal("done channel not closed")
	}
}

func slogRecord(msg string) slog.Record {
	return slog.NewRecord(time.Now(), slog.LevelInfo, msg, 0)
}
