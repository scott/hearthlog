package emitter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

// nativeServer is a synthetic NATIVE-V1 receiver used only by SDK tests. It
// records every request body and replays the original receipt for a repeated
// batch_id, like the Slice 04 contract.
type nativeServer struct {
	t        testing.TB
	srv      *httptest.Server
	mu       sync.Mutex
	bodies   [][]byte
	requests int
	receipts map[string][]byte
	events   map[string]int // event_id -> times committed
	inflight int
	maxIn    int
	next     int
	// dropBodies stops retaining request bodies (long outage profiles), so the
	// harness's own memory does not pollute the SDK's RSS measurement.
	dropBodies bool
	// respond, if set, may write its own response; return true when handled.
	respond func(w http.ResponseWriter, r *http.Request, body []byte, attempt int) bool
}

func newNativeServer(t testing.TB) *nativeServer {
	ns := &nativeServer{t: t, receipts: map[string][]byte{}, events: map[string]int{}}
	ns.srv = httptest.NewServer(http.HandlerFunc(ns.handle))
	t.Cleanup(ns.srv.Close)
	return ns
}

func (ns *nativeServer) endpoint() string { return ns.srv.URL + NativePath }

func (ns *nativeServer) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBatchBytes+1))
	if err != nil {
		return
	}
	ns.mu.Lock()
	ns.inflight++
	ns.maxIn = max(ns.maxIn, ns.inflight)
	ns.requests++
	attempt := ns.requests
	if !ns.dropBodies {
		ns.bodies = append(ns.bodies, body)
	}
	ns.mu.Unlock()
	defer func() {
		ns.mu.Lock()
		ns.inflight--
		ns.mu.Unlock()
	}()
	if r.Header.Get("Authorization") != "Bearer "+testToken || r.Header.Get("Content-Type") != "application/json" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if len(body) > MaxBatchBytes {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	if ns.respond != nil && ns.respond(w, r, body, attempt) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(ns.commit(body))
}

// commit validates like the server and returns the (possibly replayed) receipt.
func (ns *nativeServer) commit(body []byte) []byte {
	b := decodeNative(ns.t, body)
	ns.mu.Lock()
	defer ns.mu.Unlock()
	if rc, ok := ns.receipts[b.BatchID]; ok {
		return rc
	}
	for _, e := range b.Events {
		ns.events[e.EventID]++
	}
	first := ns.next + 1
	ns.next += len(b.Events)
	rc, _ := json.Marshal(map[string]any{
		"version": 1, "batch_id": b.BatchID, "accepted": len(b.Events),
		"first_ingest_id": fmt.Sprint(first), "last_ingest_id": fmt.Sprint(ns.next),
		"received_at": "2026-01-02T03:04:06Z", "replay_until": "2026-01-04T03:04:06Z",
	})
	ns.receipts[b.BatchID] = rc
	return rc
}

func (ns *nativeServer) snapshot() (bodies [][]byte, committed map[string]int, maxIn int) {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	committed = map[string]int{}
	for k, v := range ns.events {
		committed[k] = v
	}
	return append([][]byte(nil), ns.bodies...), committed, ns.maxIn
}

const testToken = "synthetic-write-token-0001"

// decodeNative checks a wire body against the native envelope shape and the
// shared server-side event validation, then returns it.
func decodeNative(t testing.TB, body []byte) model.Batch {
	t.Helper()
	if err := model.ValidateJSON(body); err != nil {
		t.Errorf("wire body is not strict JSON: %v", err)
	}
	var b model.Batch
	if err := json.Unmarshal(body, &b); err != nil {
		t.Errorf("wire body: %v", err)
		return b
	}
	if b.Version != 1 || len(b.Events) == 0 || len(b.Events) > MaxBatchEvents || len(body) > MaxBatchBytes {
		t.Errorf("bad native envelope: version=%d events=%d bytes=%d", b.Version, len(b.Events), len(body))
	}
	// Reuse the server's per-event validator through a one-event skeleton batch.
	for _, e := range b.Events {
		one, _ := json.Marshal(model.Batch{Version: model.SkeletonVersion, BatchID: b.BatchID, Events: []model.Event{e}})
		if _, err := model.ValidateBatch(one, time.Now()); err != nil {
			t.Errorf("event fails server validation: %v", err)
		}
	}
	return b
}

func newTestHandler(t *testing.T, cfg Config, clk clock, tune tuning) *Handler {
	t.Helper()
	if cfg.Token == "" {
		cfg.Token = testToken
	}
	if clk == nil {
		clk = realClock{}
	}
	if tune == (tuning{}) {
		tune = defaultTuning
	}
	h, err := newHandler(cfg, clk, tune)
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.Close(ctx)
	})
	return h
}

func checkAccounting(t *testing.T, st Stats) {
	t.Helper()
	if st.Handled != st.AckedEvents+st.TotalDropped()+uint64(st.QueuedEvents+st.InFlightEvents) {
		t.Fatalf("accounting: handled=%d acked=%d dropped=%d queued=%d inflight=%d",
			st.Handled, st.AckedEvents, st.TotalDropped(), st.QueuedEvents, st.InFlightEvents)
	}
}

// fakeClock advances only when the SDK sleeps, so retry timing is exact.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
	jitter float64
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), jitter: 1}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.sleeps = append(c.sleeps, d)
	c.mu.Unlock()
	return nil
}

func (c *fakeClock) Jitter() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.jitter
}

func (c *fakeClock) slept() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

// waitGoroutines fails if the goroutine count does not return to base.
func waitGoroutines(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > base {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			n := runtime.Stack(buf, true)
			t.Fatalf("goroutine leak: %d > %d\n%s", runtime.NumGoroutine(), base, buf[:n])
		}
		time.Sleep(10 * time.Millisecond)
	}
}
