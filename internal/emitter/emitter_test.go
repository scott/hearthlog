package emitter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func TestConcurrentGroupedEmit(t *testing.T) {
	const canary = "canary-secret-xyz-12345"
	var mu sync.Mutex
	var bodies []json.RawMessage
	var reqCount int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-write-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 256*1024))
		if _, err := model.ValidateBatch(body, time.Now()); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		reqCount++
		bodies = append(bodies, body)
		mu.Unlock()
		var b model.Batch
		_ = json.Unmarshal(body, &b)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version":  model.SkeletonVersion,
			"protocol": model.SkeletonProtocol,
			"batch_id": b.BatchID,
			"accepted": len(b.Events),
		})
	}))
	defer srv.Close()

	h, err := New(srv.URL+"/api/experimental/skeleton/batches", "test-write-token", []string{canary})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.Close(ctx)
	}()

	grouped := []slog.Attr{
		slog.String("task", "demo"),
		slog.Int("attempt", 1),
		slog.String("password", "should-be-dropped"),
		slog.String("note", "contains "+canary+" inside"),
	}

	parent := h.WithGroup("outer")
	sibling := slog.New(parent.WithAttrs(grouped))

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			sibling.Info(fmt.Sprintf("event %d", n),
				slog.Int("idx", n),
				slog.Bool("ok", true),
			)
		}(i)
	}
	wg.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if reqCount != 20 {
		t.Fatalf("expected exactly 20 requests, got %d", reqCount)
	}
	for _, b := range bodies {
		if len(b) > 0 && containsCanary(b, canary) {
			t.Fatal("canary found in wire body")
		}
	}
}

func TestSiblingDoesNotMutateParent(t *testing.T) {
	h, err := New("http://127.0.0.1:1/api/experimental/skeleton/batches", "tok", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.Close(ctx)
	}()
	parentH := h.WithGroup("outer").(*Handler)
	_ = parentH.WithAttrs([]slog.Attr{slog.String("k", "v")})
	if len(parentH.base) != 0 {
		t.Fatal("parent mutated by discarded WithAttrs")
	}
}

func TestConcurrentHandleNoPanic(t *testing.T) {
	h, err := New("http://127.0.0.1:1/api/experimental/skeleton/batches", "tok", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.Close(ctx)
	}()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			slog.New(h).Info(fmt.Sprintf("msg %d", n), slog.Int("i", n))
		}(i)
	}
	wg.Wait()
}

func containsCanary(data []byte, canary string) bool {
	return len(data) > 0 && string(data) != "" && contains(data, canary)
}

func contains(haystack []byte, needle string) bool {
	n := len(needle)
	if n == 0 || len(haystack) < n {
		return false
	}
	for i := 0; i+n <= len(haystack); i++ {
		if string(haystack[i:i+n]) == needle {
			return true
		}
	}
	return false
}
