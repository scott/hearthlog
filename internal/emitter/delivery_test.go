package emitter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func flushWithin(t *testing.T, h *Handler, d time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return h.Flush(ctx)
}

func TestBatchingEventAndByteBounds(t *testing.T) {
	t.Run("events", func(t *testing.T) {
		ns := newNativeServer(t)
		h := newTestHandler(t, Config{Endpoint: ns.endpoint()}, nil, tuning{})
		l := slog.New(h)
		for i := 0; i < 1000; i++ {
			l.Info("synthetic batch", "i", i)
		}
		if err := flushWithin(t, h, 10*time.Second); err != nil {
			t.Fatal(err)
		}
		bodies, committed, maxIn := ns.snapshot()
		total, full := 0, 0
		for _, b := range bodies {
			n := len(decodeNative(t, b).Events)
			total += n
			if n == MaxBatchEvents {
				full++
			}
		}
		if total != 1000 || len(committed) != 1000 || full == 0 || maxIn > MaxInFlight {
			t.Fatalf("total=%d committed=%d full=%d batches=%d maxIn=%d", total, len(committed), full, len(bodies), maxIn)
		}
		checkAccounting(t, h.Stats())
	})
	t.Run("bytes", func(t *testing.T) {
		ns := newNativeServer(t)
		h := newTestHandler(t, Config{Endpoint: ns.endpoint()}, nil, tuning{})
		big := strings.Repeat("z", 7000)
		for i := 0; i < 60; i++ {
			slog.New(h).Info("big", "a", big, "b", big, "c", big, "d", big)
		}
		if err := flushWithin(t, h, 10*time.Second); err != nil {
			t.Fatal(err)
		}
		bodies, committed, _ := ns.snapshot()
		largest := 0
		for _, b := range bodies {
			largest = max(largest, len(b))
		}
		if len(committed) != 60 || largest > MaxBatchBytes || largest < MaxBatchBytes-2*MaxEventBytes {
			t.Fatalf("committed=%d largest=%d batches=%d", len(committed), largest, len(bodies))
		}
	})
}

func TestBatchIntervalAndInFlightLimit(t *testing.T) {
	ns := newNativeServer(t)
	var mu sync.Mutex
	var firstAt time.Time
	ns.respond = func(w http.ResponseWriter, r *http.Request, body []byte, attempt int) bool {
		mu.Lock()
		if firstAt.IsZero() {
			firstAt = time.Now()
		}
		mu.Unlock()
		time.Sleep(50 * time.Millisecond) // slow receiver
		return false
	}
	h := newTestHandler(t, Config{Endpoint: ns.endpoint()}, nil, tuning{})
	start := time.Now()
	slog.New(h).Info("single event waits for the batch interval")
	deadline := time.Now().Add(3 * time.Second)
	for h.Stats().AckedEvents != 1 {
		if time.Now().After(deadline) {
			t.Fatal("single event never sent without Flush")
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	waited := firstAt.Sub(start)
	mu.Unlock()
	if waited < BatchInterval-20*time.Millisecond || waited > BatchInterval+500*time.Millisecond {
		t.Fatalf("first send after %v, want about %v", waited, BatchInterval)
	}
	l := slog.New(h)
	for i := 0; i < 2000; i++ {
		l.Info("burst", "i", i)
	}
	if err := flushWithin(t, h, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	_, committed, maxIn := ns.snapshot()
	if len(committed) != 2001 || maxIn != MaxInFlight {
		t.Fatalf("committed=%d maxInFlight=%d (want exactly %d)", len(committed), maxIn, MaxInFlight)
	}
}

// TestSlowServerTimeoutRetriesSameBatch: a request exceeding its deadline is
// ambiguous, so the identical bytes (same batch_id) are resent.
func TestSlowServerTimeoutRetriesSameBatch(t *testing.T) {
	ns := newNativeServer(t)
	ns.respond = func(w http.ResponseWriter, r *http.Request, body []byte, attempt int) bool {
		if attempt == 1 {
			<-r.Context().Done() // never answers in time
			return true
		}
		return false
	}
	h := newTestHandler(t, Config{Endpoint: ns.endpoint()}, nil,
		tuning{batchInterval: BatchInterval, reqTimeout: 200 * time.Millisecond, retryBudget: RetryBudget})
	for i := 0; i < 3; i++ {
		slog.New(h).Info("slow", "i", i)
	}
	if err := flushWithin(t, h, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	bodies, committed, _ := ns.snapshot()
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("want 2 identical attempts, got %d", len(bodies))
	}
	st := h.Stats()
	if st.Retries != 1 || st.AckedEvents != 3 || len(committed) != 3 {
		t.Fatalf("stats %+v committed=%d", st, len(committed))
	}
}

// TestAmbiguousCommitReplaysIdenticalBatch: the server commits but the
// response is lost; the retry carries the same batch identity and bytes, so a
// native receiver returns the original receipt and nothing is duplicated.
func TestAmbiguousCommitReplaysIdenticalBatch(t *testing.T) {
	ns := newNativeServer(t)
	ns.respond = func(w http.ResponseWriter, r *http.Request, body []byte, attempt int) bool {
		if attempt == 1 {
			ns.commit(body)
			conn, _, err := http.NewResponseController(w).Hijack()
			if err == nil {
				conn.Close() // response lost after commit
			}
			return true
		}
		return false
	}
	clk := newFakeClock()
	h := newTestHandler(t, Config{Endpoint: ns.endpoint()}, clk, tuning{})
	for i := 0; i < 5; i++ {
		slog.New(h).Info("ambiguous", "i", i)
	}
	if err := flushWithin(t, h, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	bodies, committed, _ := ns.snapshot()
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("attempts=%d identical=%v", len(bodies), len(bodies) == 2 && bytes.Equal(bodies[0], bodies[1]))
	}
	for id, n := range committed {
		if n != 1 {
			t.Fatalf("event %s committed %d times", id, n)
		}
	}
	if st := h.Stats(); st.AckedEvents != 5 || st.Retries != 1 || len(committed) != 5 {
		t.Fatalf("stats %+v", st)
	}
	if s := clk.slept(); len(s) != 1 || s[0] < MinBackoff || s[0] > MaxBackoff {
		t.Fatalf("backoff %v", s)
	}
}

// TestRetryBackoffAndBudgetFakeClock: persistent 503 retries the identical
// batch with exponential jittered delays (250ms..5s) and gives up within 60s.
func TestRetryBackoffAndBudgetFakeClock(t *testing.T) {
	for _, jitter := range []float64{0, 0.5, 0.999} {
		t.Run(fmt.Sprint(jitter), func(t *testing.T) {
			ns := newNativeServer(t)
			statuses := []int{503, 500, 502, 504}
			ns.respond = func(w http.ResponseWriter, r *http.Request, body []byte, attempt int) bool {
				w.WriteHeader(statuses[attempt%len(statuses)])
				return true
			}
			clk := newFakeClock()
			clk.jitter = jitter
			var diags []Diagnostic
			var dmu sync.Mutex
			h := newTestHandler(t, Config{Endpoint: ns.endpoint(), OnDiagnostic: func(d Diagnostic) {
				dmu.Lock()
				diags = append(diags, d)
				dmu.Unlock()
			}}, clk, tuning{})
			slog.New(h).Error("outage")
			err := flushWithin(t, h, 10*time.Second)
			if !errors.Is(err, ErrUndelivered) {
				t.Fatalf("Flush = %v", err)
			}
			sleeps := clk.slept()
			var total time.Duration
			prevBase := time.Duration(0)
			for i, d := range sleeps {
				if d < MinBackoff || d > MaxBackoff {
					t.Fatalf("sleep %d = %v outside [250ms,5s]", i, d)
				}
				base := min(MinBackoff<<i, MaxBackoff)
				if d < base/2 || d > base || base < prevBase {
					t.Fatalf("sleep %d = %v not in jitter window of %v", i, d, base)
				}
				prevBase = base
				total += d
			}
			if total > RetryBudget || total+MaxBackoff < RetryBudget-MaxBackoff {
				t.Fatalf("total backoff %v vs budget %v (%d sleeps)", total, RetryBudget, len(sleeps))
			}
			bodies, _, _ := ns.snapshot()
			for _, b := range bodies {
				if !bytes.Equal(b, bodies[0]) {
					t.Fatal("batch changed between retries")
				}
			}
			st := h.Stats()
			if len(bodies) != len(sleeps)+1 || st.Retries != uint64(len(sleeps)) || st.Dropped["retry_exhausted"]["ERROR"] != 1 {
				t.Fatalf("attempts=%d sleeps=%d stats=%+v", len(bodies), len(sleeps), st)
			}
			waitFor(t, func() bool { dmu.Lock(); defer dmu.Unlock(); return len(diags) > 0 })
			dmu.Lock()
			defer dmu.Unlock()
			if len(diags) != 1 || diags[0].Reason != ReasonRetryExhausted || diags[0].Events != 1 {
				t.Fatalf("diags %+v", diags)
			}
			checkAccounting(t, st)
		})
	}
}

func TestRetryAfterBoundedByBudget(t *testing.T) {
	cases := []struct {
		name       string
		header     func(now time.Time) string
		wantSleeps []time.Duration
		wantAcked  uint64
		wantAtt    int
	}{
		{"seconds", func(time.Time) string { return "7" }, []time.Duration{7 * time.Second}, 1, 2},
		{"http_date", func(now time.Time) string { return now.Add(3 * time.Second).Format(http.TimeFormat) }, []time.Duration{3 * time.Second}, 1, 2},
		{"beyond_budget", func(time.Time) string { return "120" }, nil, 0, 1},
		{"invalid_falls_back", func(time.Time) string { return "soon" }, []time.Duration{MinBackoff}, 1, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ns := newNativeServer(t)
			clk := newFakeClock()
			ns.respond = func(w http.ResponseWriter, r *http.Request, body []byte, attempt int) bool {
				if attempt == 1 {
					w.Header().Set("Retry-After", tc.header(clk.Now()))
					w.WriteHeader(http.StatusTooManyRequests)
					return true
				}
				return false
			}
			h := newTestHandler(t, Config{Endpoint: ns.endpoint()}, clk, tuning{})
			slog.New(h).Info("rate limited")
			_ = flushWithin(t, h, 5*time.Second)
			bodies, _, _ := ns.snapshot()
			st := h.Stats()
			got := clk.slept()
			if st.AckedEvents != tc.wantAcked || len(bodies) != tc.wantAtt || fmt.Sprint(got) != fmt.Sprint(tc.wantSleeps) {
				t.Fatalf("acked=%d attempts=%d sleeps=%v", st.AckedEvents, len(bodies), got)
			}
			if tc.wantAcked == 0 && st.Dropped["retry_exhausted"]["INFO"] != 1 {
				t.Fatalf("dropped %v", st.Dropped)
			}
		})
	}
}

func TestTerminalStatusesAreNotRetried(t *testing.T) {
	for _, code := range []int{400, 404, 409, 413, 415, 422, 501, 307} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			ns := newNativeServer(t)
			ns.respond = func(w http.ResponseWriter, r *http.Request, body []byte, attempt int) bool {
				if code == 307 {
					w.Header().Set("Location", "http://127.0.0.1:1"+NativePath)
				}
				w.WriteHeader(code)
				return true
			}
			diagCh := make(chan Diagnostic, 1)
			h := newTestHandler(t, Config{Endpoint: ns.endpoint(), OnDiagnostic: func(d Diagnostic) { diagCh <- d }}, newFakeClock(), tuning{})
			slog.New(h).Warn("terminal")
			if err := flushWithin(t, h, 5*time.Second); !errors.Is(err, ErrUndelivered) {
				t.Fatalf("Flush = %v", err)
			}
			diag := <-diagCh
			bodies, _, _ := ns.snapshot()
			st := h.Stats()
			if len(bodies) != 1 || st.Retries != 0 || st.Dropped["rejected"]["WARN"] != 1 || diag.Status != code {
				t.Fatalf("attempts=%d stats=%+v diag=%+v", len(bodies), st, diag)
			}
		})
	}
}

// TestUnauthorizedStopsSender: 401/403 (revoked or wrong token) stops the
// sender; queued and later events are counted as unauthorized, not retried.
func TestUnauthorizedStopsSender(t *testing.T) {
	for _, code := range []int{401, 403} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			ns := newNativeServer(t)
			ns.respond = func(w http.ResponseWriter, r *http.Request, body []byte, attempt int) bool {
				w.WriteHeader(code)
				return true
			}
			h := newTestHandler(t, Config{Endpoint: ns.endpoint()}, newFakeClock(), tuning{})
			l := slog.New(h)
			for i := 0; i < 450; i++ {
				l.Info("revoked", "i", i)
			}
			_ = flushWithin(t, h, 5*time.Second)
			l.Error("after revoke")
			bodies, _, _ := ns.snapshot()
			st := h.Stats()
			if len(bodies) > MaxInFlight || st.Retries != 0 || st.AckedEvents != 0 ||
				st.Dropped["unauthorized"]["INFO"] != 450 || st.Dropped["unauthorized"]["ERROR"] != 1 {
				t.Fatalf("attempts=%d stats=%+v", len(bodies), st)
			}
			checkAccounting(t, st)
		})
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
