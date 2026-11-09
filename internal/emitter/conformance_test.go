package emitter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/slogtest"
	"time"

	"example.invalid/hearthlog/internal/model"
)

// queuedEvents returns the events currently held in the queue (test only).
func queuedEvents(t *testing.T, h *Handler) []model.Event {
	t.Helper()
	s := h.state
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.Event
	for _, q := range s.q[s.qHead:] {
		var e model.Event
		if err := json.Unmarshal(q.data, &e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

// TestSlogtestConformance runs the standard library handler conformance
// suite against snapshots taken by Handle. The SDK always assigns an
// event_time (the native contract requires one), so the single slogtest case
// "zero time is ignored" is the documented, expected deviation.
func TestSlogtestConformance(t *testing.T) {
	h := newTestHandler(t, Config{Endpoint: "http://127.0.0.1:1" + NativePath, Level: slog.LevelDebug}, nil,
		tuning{batchInterval: time.Hour, reqTimeout: time.Second, retryBudget: time.Second})
	// Hold everything in the queue: one huge batch interval and no flush.
	results := func() []map[string]any {
		var out []map[string]any
		for _, e := range queuedEvents(t, h) {
			m := map[string]any{}
			dec := json.NewDecoder(bytes.NewReader(e.Attributes))
			dec.UseNumber()
			if err := dec.Decode(&m); err != nil {
				t.Fatal(err)
			}
			normalizeNumbers(m)
			m[slog.TimeKey] = e.EventTime
			m[slog.LevelKey] = e.SeverityText
			m[slog.MessageKey] = e.Message
			out = append(out, m)
		}
		return out
	}
	err := slogtest.TestHandler(h, results)
	var unexpected []string
	for _, e := range unwrapAll(err) {
		if strings.Contains(e.Error(), "unexpected key \"time\"") {
			continue // zero-time deviation, see doc comment
		}
		unexpected = append(unexpected, e.Error())
	}
	if len(unexpected) > 0 {
		t.Fatalf("slogtest failures:\n%s", strings.Join(unexpected, "\n"))
	}
}

func unwrapAll(err error) []error {
	if err == nil {
		return nil
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		var out []error
		for _, e := range j.Unwrap() {
			out = append(out, unwrapAll(e)...)
		}
		return out
	}
	return []error{err}
}

func normalizeNumbers(m map[string]any) {
	for k, v := range m {
		switch x := v.(type) {
		case json.Number:
			if i, err := x.Int64(); err == nil {
				m[k] = i
			} else if f, err := x.Float64(); err == nil {
				m[k] = f
			}
		case map[string]any:
			normalizeNumbers(x)
		}
	}
}

// TestConcurrentHandlerMethodsRace exercises Enabled/Handle/WithAttrs/
// WithGroup from many goroutines on shared parents under -race, and checks
// that derived handlers never observe each other's attributes.
func TestConcurrentHandlerMethodsRace(t *testing.T) {
	ns := newNativeServer(t)
	h := newTestHandler(t, Config{Endpoint: ns.endpoint()}, nil, tuning{})
	parent := h.WithGroup("svc").WithAttrs([]slog.Attr{slog.String("component", "demo-worker")})
	const workers, per = 16, 25
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			child := parent.WithAttrs([]slog.Attr{slog.Int("worker", w)}).WithGroup(fmt.Sprintf("g%d", w%3))
			for i := 0; i < per; i++ {
				if !child.Enabled(context.Background(), slog.LevelInfo) || child.Enabled(context.Background(), slog.LevelDebug) {
					t.Error("Enabled mismatch")
				}
				slog.New(child).Info("synthetic concurrent", "i", i)
			}
		}(w)
	}
	wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	st := h.Stats()
	if st.AckedEvents != workers*per {
		t.Fatalf("acked %d want %d (%+v)", st.AckedEvents, workers*per, st)
	}
	checkAccounting(t, st)
	bodies, _, _ := ns.snapshot()
	seen := map[int]int{}
	for _, body := range bodies {
		for _, e := range decodeNative(t, body).Events {
			var a struct {
				Svc map[string]json.RawMessage `json:"svc"`
			}
			if err := json.Unmarshal(e.Attributes, &a); err != nil {
				t.Fatal(err)
			}
			var w int
			if err := json.Unmarshal(a.Svc["worker"], &w); err != nil {
				t.Fatalf("worker attr: %s", e.Attributes)
			}
			if len(a.Svc) != 3 { // component, worker, one gN group
				t.Fatalf("sibling attributes leaked: %s", e.Attributes)
			}
			if _, ok := a.Svc[fmt.Sprintf("g%d", w%3)]; !ok {
				t.Fatalf("wrong group for worker %d: %s", w, e.Attributes)
			}
			seen[w]++
		}
	}
	for w := 0; w < workers; w++ {
		if seen[w] != per {
			t.Fatalf("worker %d: %d events", w, seen[w])
		}
	}
}

func TestLevelFiltering(t *testing.T) {
	var lv slog.LevelVar
	lv.Set(slog.LevelWarn)
	h := newTestHandler(t, Config{Endpoint: "http://127.0.0.1:1" + NativePath, Level: &lv}, nil, tuning{})
	if h.Enabled(context.Background(), slog.LevelInfo) || !h.Enabled(context.Background(), slog.LevelWarn) {
		t.Fatal("level not honored")
	}
	lv.Set(slog.LevelDebug)
	if !h.WithGroup("g").Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("dynamic level not honored by derived handler")
	}
}

// TestLimitsDropNewestWithAccounting covers the per-event bounds and the
// queue's event and byte limits, each counted by reason and severity.
func TestLimitsDropNewestWithAccounting(t *testing.T) {
	// A receiver that never answers keeps batches in flight, so the queue fills.
	stall := newNativeServer(t)
	stall.respond = func(w http.ResponseWriter, r *http.Request, _ []byte, _ int) bool {
		<-r.Context().Done()
		return true
	}
	endpoint := stall.endpoint()
	hold := tuning{batchInterval: time.Hour, reqTimeout: time.Second, retryBudget: time.Second}

	t.Run("nesting", func(t *testing.T) {
		h := newTestHandler(t, Config{Endpoint: endpoint}, nil, hold)
		var cur slog.Handler = h
		for i := 0; i < MaxNesting-1; i++ {
			cur = cur.WithGroup(fmt.Sprintf("n%d", i))
		}
		slog.New(cur).Info("depth ok", "leaf", 1)
		if got := len(queuedEvents(t, h)); got != 1 {
			t.Fatalf("7 groups + leaf should be accepted, queued=%d", got)
		}
		slog.New(cur.WithGroup("too_deep")).Warn("depth 9", "leaf", 1)
		slog.New(cur).Error("nested value", slog.Group("x", slog.Int("y", 1)))
		st := h.Stats()
		if st.Dropped["attribute_limit"]["WARN"] != 1 {
			t.Fatalf("depth drop not counted: %v", st.Dropped)
		}
		if st.Dropped["attribute_limit"]["ERROR"] != 1 {
			t.Fatalf("nested-value depth drop not counted: %v", st.Dropped)
		}
		checkAccounting(t, st)
	})

	t.Run("attributes", func(t *testing.T) {
		h := newTestHandler(t, Config{Endpoint: endpoint}, nil, hold)
		attrs := make([]any, 0, 2*MaxAttributes)
		for i := 0; i < MaxAttributes; i++ {
			attrs = append(attrs, fmt.Sprintf("k%d", i), i)
		}
		slog.New(h).Info("exactly 128", attrs...)
		// 127 + a group with 2 members = 130 members recursively.
		slog.New(h).Info("130 recursive", append(attrs[:2*(MaxAttributes-1)], slog.Group("g", "a", 1, "b", 2))...)
		st := h.Stats()
		if st.Enqueued != 1 || st.Dropped["attribute_limit"]["INFO"] != 1 {
			t.Fatalf("attribute bound: %+v", st)
		}
	})

	t.Run("event_size", func(t *testing.T) {
		h := newTestHandler(t, Config{Endpoint: endpoint}, nil, hold)
		s := strings.Repeat("x", 8000)
		slog.New(h).Info("ok", "a", s, "b", s, "c", s) // ~24KiB attrs is under 32KiB
		slog.New(h).Error("too big", "a", s, "b", s, "c", s, "d", s, "e", s)
		st := h.Stats()
		if st.Enqueued != 1 || st.Dropped["event_too_large"]["ERROR"] != 1 {
			t.Fatalf("event bound: %+v", st)
		}
	})

	t.Run("queue_events", func(t *testing.T) {
		h := newTestHandler(t, Config{Endpoint: endpoint}, nil, hold)
		l := slog.New(h)
		for i := 0; h.Stats().QueuedEvents < DefaultQueueEvents; i++ {
			l.Info("fill", "i", i)
			if i > DefaultQueueEvents+MaxInFlight*MaxBatchEvents {
				t.Fatal("queue never filled")
			}
		}
		if st := h.Stats(); st.InFlightEvents > MaxInFlight*MaxBatchEvents || st.TotalDropped() != 0 {
			t.Fatalf("before overflow: %+v", st)
		}
		l.Warn("newest dropped 1")
		l.Error("newest dropped 2")
		st := h.Stats()
		if st.QueuedEvents != DefaultQueueEvents || st.Dropped["queue_full"]["WARN"] != 1 || st.Dropped["queue_full"]["ERROR"] != 1 {
			t.Fatalf("queue event bound: queued=%d dropped=%v", st.QueuedEvents, st.Dropped)
		}
		// Oldest events are retained (drop newest).
		if evs := queuedEvents(t, h); evs[0].Message != "fill" || evs[len(evs)-1].Message != "fill" {
			t.Fatal("drop-newest violated")
		}
		checkAccounting(t, st)
	})

	t.Run("queue_bytes", func(t *testing.T) {
		h := newTestHandler(t, Config{Endpoint: endpoint}, nil, hold)
		l := slog.New(h)
		big := strings.Repeat("y", 8000)
		n := 0
		for h.Stats().Dropped["queue_bytes"] == nil {
			l.Info("big", "a", big, "b", big, "c", big)
			n++
			if n > DefaultQueueEvents {
				t.Fatal("byte bound never reached")
			}
		}
		st := h.Stats()
		if st.QueuedBytes > DefaultQueueBytes || st.QueuedEvents >= DefaultQueueEvents {
			t.Fatalf("byte bound: %+v", st)
		}
		if st.QueuedBytes+MaxEventBytes < DefaultQueueBytes {
			t.Fatalf("dropped too early: %d bytes queued", st.QueuedBytes)
		}
		checkAccounting(t, st)
	})

	t.Run("invalid_event", func(t *testing.T) {
		h := newTestHandler(t, Config{Endpoint: endpoint}, nil, hold)
		slog.New(h).Info("reserved", "service", "demo-api")
		r := slog.NewRecord(time.Now().Add(time.Hour), slog.LevelWarn, "future", 0)
		_ = h.Handle(context.Background(), r)
		st := h.Stats()
		if st.Dropped["invalid_event"]["INFO"] != 1 || st.Dropped["invalid_event"]["WARN"] != 1 {
			t.Fatalf("invalid events: %v", st.Dropped)
		}
		// A nested "service" key is ordinary data.
		slog.New(h).Info("nested ok", slog.Group("meta", "service", "demo-api"))
		if h.Stats().Enqueued != 1 {
			t.Fatal("nested reserved name should be accepted")
		}
	})
}

func TestDiagnosticHookNonRecursive(t *testing.T) {
	var calls []Diagnostic
	var h *Handler
	var logger *slog.Logger
	cfg := Config{
		Endpoint: "http://127.0.0.1:1" + NativePath,
		OnDiagnostic: func(d Diagnostic) {
			calls = append(calls, d)
			// A hook that logs a rejected value through the same handler
			// must not re-enter itself.
			logger.Error("diag", "bad", struct{}{})
		},
	}
	h = newTestHandler(t, cfg, nil, tuning{batchInterval: time.Hour, reqTimeout: time.Second, retryBudget: time.Second})
	logger = slog.New(h)
	logger.Info("unsupported", "obj", struct{ A int }{1})
	st := h.Stats()
	if len(calls) != 1 || calls[0].Reason != ReasonRedaction || calls[0].Severity != "INFO" {
		t.Fatalf("calls = %+v", calls)
	}
	if st.DiagnosticsSuppressed != 1 || st.Dropped["redaction_failed"]["ERROR"] != 1 || st.RedactionFailures != 2 {
		t.Fatalf("stats = %+v", st)
	}
	// Panicking hooks are contained and counted.
	h2 := newTestHandler(t, Config{Endpoint: cfg.Endpoint, OnDiagnostic: func(Diagnostic) { panic("synthetic") }}, nil, tuning{})
	slog.New(h2).Info("x", "obj", struct{}{})
	if h2.Stats().DiagnosticsSuppressed != 1 {
		t.Fatal("panicking hook not contained")
	}
	if errors.Is(nil, ErrUndelivered) {
		t.Fatal("unreachable")
	}
}
