package emitter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime"
	"sync"
	"testing"
	"time"
)

// TestCanceledRequestContextStillLogs: a canceled caller context must not
// suppress a diagnostic event; the sender uses its own lifecycle context.
func TestCanceledRequestContextStillLogs(t *testing.T) {
	ns := newNativeServer(t)
	h := newTestHandler(t, Config{Endpoint: ns.endpoint()}, nil, tuning{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	slog.New(h).ErrorContext(ctx, "request canceled", "err_kind", "context_canceled")
	if err := flushWithin(t, h, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if st := h.Stats(); st.AckedEvents != 1 {
		t.Fatalf("stats %+v", st)
	}
}

// TestConcurrentFlushBarrier: each Flush returns only after every event
// accepted before it was acknowledged, while other goroutines keep logging.
func TestConcurrentFlushBarrier(t *testing.T) {
	ns := newNativeServer(t)
	ns.respond = func(w http.ResponseWriter, r *http.Request, body []byte, attempt int) bool {
		time.Sleep(5 * time.Millisecond)
		return false
	}
	h := newTestHandler(t, Config{Endpoint: ns.endpoint()}, nil, tuning{})
	l := slog.New(h)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for round := 0; round < 5; round++ {
				for i := 0; i < 20; i++ {
					l.Info("barrier", "g", g, "round", round, "i", i)
				}
				h.state.mu.Lock()
				target := h.state.nextSeq - 1
				h.state.mu.Unlock()
				if err := flushWithin(t, h, 10*time.Second); err != nil {
					t.Error(err)
					return
				}
				h.state.mu.Lock()
				low := h.state.lowestUnresolvedLocked()
				h.state.mu.Unlock()
				if low <= target {
					t.Errorf("Flush returned with seq %d unresolved (target %d)", low, target)
				}
			}
		}(g)
	}
	wg.Wait()
	st := h.Stats()
	if st.AckedEvents != 8*5*20 {
		t.Fatalf("stats %+v", st)
	}
	checkAccounting(t, st)
}

// TestCloseDrainsIsIdempotentAndLeakFree: concurrent Close calls all return
// the same nil result after a full drain, later Handle calls are counted as
// closed, and no SDK goroutine survives.
func TestCloseDrainsIsIdempotentAndLeakFree(t *testing.T) {
	ns := newNativeServer(t)
	base := runtime.NumGoroutine() // includes the test server's accept loop
	h, err := NewWithConfig(Config{Endpoint: ns.endpoint(), Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	l := slog.New(h)
	for i := 0; i < 500; i++ {
		l.Info("drain", "i", i)
	}
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			errs[i] = h.Close(ctx)
		}(i)
	}
	// Handle racing with Close: either delivered or counted as closed.
	for i := 0; i < 50; i++ {
		l.Info("racing close")
	}
	wg.Wait()
	for _, e := range errs {
		if e != errs[0] {
			t.Fatalf("Close results differ: %v vs %v", e, errs[0])
		}
	}
	st := h.Stats()
	if st.ShutdownUnsent != 0 || st.QueuedEvents != 0 || st.InFlightBatches != 0 {
		t.Fatalf("after drain %+v", st)
	}
	closed := st.Dropped["closed"]["INFO"]
	if st.AckedEvents+closed != 550 || errs[0] == nil && closed != 0 {
		t.Fatalf("acked=%d closed=%d err=%v", st.AckedEvents, closed, errs[0])
	}
	l.Info("after close")
	if h.Stats().Dropped["closed"]["INFO"] != closed+1 {
		t.Fatal("post-Close Handle not counted")
	}
	if err := h.Flush(context.Background()); err == nil && closed+1 > 0 {
		t.Fatal("Flush after drops should report ErrUndelivered")
	}
	checkAccounting(t, h.Stats())
	ns.srv.CloseClientConnections()
	waitGoroutines(t, base)
}

// TestCloseDeadlineReportsUnsent: with an endpoint that never answers, Close
// honors its deadline, cancels in-flight requests and reports exactly how many
// events were abandoned, then leaves no goroutines behind.
func TestCloseDeadlineReportsUnsent(t *testing.T) {
	ns := newNativeServer(t)
	base := runtime.NumGoroutine() // includes the test server's accept loop
	ns.respond = func(w http.ResponseWriter, r *http.Request, _ []byte, _ int) bool {
		<-r.Context().Done()
		return true
	}
	h, err := NewWithConfig(Config{Endpoint: ns.endpoint(), Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	l := slog.New(h)
	for i := 0; i < 900; i++ {
		l.Warn("stuck", "i", i)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = h.Close(ctx)
	elapsed := time.Since(start)
	var de *DeliveryError
	if !errors.As(err, &de) || !errors.Is(err, context.DeadlineExceeded) || de.Unsent != 900 {
		t.Fatalf("Close = %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Close took %v", elapsed)
	}
	st := h.Stats()
	if st.ShutdownUnsent != 900 || st.Dropped["shutdown_unsent"]["WARN"] != 900 {
		t.Fatalf("stats %+v", st)
	}
	checkAccounting(t, st)
	if again := h.Close(context.Background()); again != err {
		t.Fatalf("second Close %v", again)
	}
	ns.srv.CloseClientConnections()
	waitGoroutines(t, base)
}

func TestFlushDeadlineReportsUnsent(t *testing.T) {
	ns := newNativeServer(t)
	release := make(chan struct{})
	ns.respond = func(w http.ResponseWriter, r *http.Request, _ []byte, _ int) bool {
		select {
		case <-release:
		case <-r.Context().Done():
			return true
		}
		return false
	}
	h := newTestHandler(t, Config{Endpoint: ns.endpoint()}, nil, tuning{})
	for i := 0; i < 10; i++ {
		slog.New(h).Info("pending", "i", i)
	}
	err := flushWithin(t, h, 100*time.Millisecond)
	var de *DeliveryError
	if !errors.As(err, &de) || de.Unsent != 10 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Flush = %v", err)
	}
	close(release)
	if err := flushWithin(t, h, 5*time.Second); err != nil {
		t.Fatalf("second Flush = %v", err)
	}
}
