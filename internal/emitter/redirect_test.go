package emitter

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRedirectNotFollowed(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	var apiHits atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiHits.Add(1)
		w.Header().Set("Location", target.URL+"/api/experimental/skeleton/batches")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer api.Close()

	h, err := New(api.URL+"/api/experimental/skeleton/batches", "tok", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "synthetic", 0)
	if err := h.Handle(context.Background(), rec); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.Flush(ctx); err == nil {
		t.Fatal("Flush: expected error, got nil")
	}

	if got := apiHits.Load(); got != 1 {
		t.Errorf("api hits = %d, want 1", got)
	}
	if got := targetHits.Load(); got != 0 {
		t.Errorf("target hits = %d, want 0", got)
	}

	closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer closeCancel()
	_ = h.Close(closeCtx)
}

func TestNewRejectsInvalidEndpoints(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
	}{
		{"nonloopback", "http://192.168.1.1:8080/api/experimental/skeleton/batches"},
		{"userinfo", "http://user:pass@127.0.0.1:8080/api/experimental/skeleton/batches"},
		{"query", "http://127.0.0.1:8080/api/experimental/skeleton/batches?x=1"},
		{"missingport", "http://127.0.0.1/api/experimental/skeleton/batches"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.endpoint, "tok", nil); err == nil {
				t.Errorf("New(%q): expected error, got nil", tc.endpoint)
			}
		})
	}
}
