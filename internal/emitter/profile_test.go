package emitter

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Profile knobs for local evidence runs. CI uses the short defaults; the
// required 5-minute outage and longer baseline are run locally with e.g.
//
//	HEARTHLOG_SDK_OUTAGE=5m HEARTHLOG_SDK_OUTAGE_RATE=100 go test -run Outage -v ./internal/emitter
//	HEARTHLOG_SDK_BASELINE=5m go test -run Baseline -v ./internal/emitter
func envDuration(t *testing.T, key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		t.Fatalf("%s: %v", key, err)
	}
	return d
}

func envInt(t *testing.T, key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		t.Fatalf("%s: invalid", key)
	}
	return n
}

// rssBytes reads the resident set size on Linux; ok=false elsewhere.
func rssBytes() (int64, bool) {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "VmRSS:"); ok {
			kb, err := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")), 10, 64)
			return kb * 1024, err == nil
		}
	}
	return 0, false
}

func percentile(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := slices.Clone(d)
	slices.Sort(s)
	i := int(float64(len(s)-1) * p)
	return s[i]
}

// baselineRecord is a generic ~1KiB synthetic event (PLAN.md baseline size).
var filler = strings.Repeat("synthetic-payload-", 50)

func emitBaseline(l *slog.Logger, i int) {
	l.Info("synthetic baseline event",
		"source", "demo-api", "i", i, "ok", true, "duration_ms", 1.5,
		slog.Group("task", "name", "demo-task", "attempt", 1),
		"detail", filler)
}

type profileResult struct {
	Profile         string           `json:"profile"`
	Race            bool             `json:"race"`
	GoVersion       string           `json:"go_version"`
	GOOS            string           `json:"goos"`
	GOARCH          string           `json:"goarch"`
	NumCPU          int              `json:"num_cpu"`
	Duration        string           `json:"duration"`
	RatePerSec      int              `json:"rate_per_sec"`
	Samples         int              `json:"samples"`
	HandleP50       string           `json:"handle_p50"`
	HandleP95       string           `json:"handle_p95"`
	HandleP99       string           `json:"handle_p99"`
	HandleMax       string           `json:"handle_max"`
	RSSBaselineMiB  float64          `json:"rss_baseline_mib,omitempty"`
	RSSPeakMiB      float64          `json:"rss_peak_mib,omitempty"`
	RSSDeltaMiB     float64          `json:"rss_delta_mib,omitempty"`
	ServerCommitted int              `json:"server_committed"`
	Stats           Stats            `json:"stats"`
	Extra           map[string]int64 `json:"extra,omitempty"`
}

func logResult(t *testing.T, r profileResult) {
	r.Race, r.GoVersion, r.GOOS, r.GOARCH, r.NumCPU = raceEnabled, runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU()
	b, _ := json.Marshal(r)
	t.Logf("PROFILE_RESULT %s", b)
}

// TestHandlerLatencyBaseline paces synthetic ~1KiB events at the required
// 5/s baseline against a healthy receiver and gates handler p95 < 1ms. The
// gate applies to non-race builds; -race (CI) inflates timings and only logs.
func TestHandlerLatencyBaseline(t *testing.T) {
	dur := envDuration(t, "HEARTHLOG_SDK_BASELINE", 2*time.Second)
	const rate = 5
	ns := newNativeServer(t)
	h := newTestHandler(t, Config{Endpoint: ns.endpoint()}, nil, tuning{})
	l := slog.New(h)
	tick := time.NewTicker(time.Second / rate)
	defer tick.Stop()
	var lat []time.Duration
	end := time.Now().Add(dur)
	for i := 0; time.Now().Before(end); i++ {
		<-tick.C
		start := time.Now()
		emitBaseline(l, i)
		lat = append(lat, time.Since(start))
	}
	if err := flushWithin(t, h, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	st := h.Stats()
	_, committed, _ := ns.snapshot()
	p95 := percentile(lat, 0.95)
	logResult(t, profileResult{Profile: "baseline_5ps", Duration: dur.String(), RatePerSec: rate, Samples: len(lat),
		HandleP50: percentile(lat, 0.5).String(), HandleP95: p95.String(), HandleP99: percentile(lat, 0.99).String(),
		HandleMax: percentile(lat, 1).String(), ServerCommitted: len(committed), Stats: st})
	if st.AckedEvents != uint64(len(lat)) || len(committed) != len(lat) || st.TotalDropped() != 0 {
		t.Fatalf("baseline loss: %+v committed=%d", st, len(committed))
	}
	if !raceEnabled && p95 >= time.Millisecond {
		t.Fatalf("handler p95 %v >= 1ms", p95)
	}
}

// TestEndpointOutageBoundedAndAccounted keeps emitting through an endpoint
// outage (connections dropped), then restores it. Memory stays bounded,
// overflow drops are counted by reason, and after recovery every handled
// event is exactly accounted: acked + dropped == handled, and the receiver
// committed exactly the acked events.
func TestEndpointOutageBoundedAndAccounted(t *testing.T) {
	dur := envDuration(t, "HEARTHLOG_SDK_OUTAGE", 2*time.Second)
	rate := envInt(t, "HEARTHLOG_SDK_OUTAGE_RATE", 100)
	ns := newNativeServer(t)
	ns.dropBodies = true
	var down atomic.Bool
	ns.respond = func(w http.ResponseWriter, r *http.Request, _ []byte, _ int) bool {
		if !down.Load() {
			return false
		}
		if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
			conn.Close() // network failure: outcome unknown, client retries
		}
		return true
	}
	h := newTestHandler(t, Config{Endpoint: ns.endpoint()}, nil, tuning{})
	l := slog.New(h)
	for i := 0; i < 200; i++ { // warm up allocator and connections
		emitBaseline(l, i)
	}
	if err := flushWithin(t, h, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	rss0, rssOK := rssBytes()
	peak := rss0
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	heap0, heapPeak := ms.HeapInuse, ms.HeapInuse

	down.Store(true)
	tick := time.NewTicker(time.Second / time.Duration(rate))
	defer tick.Stop()
	var lat []time.Duration
	end := time.Now().Add(dur)
	nextSample := time.Now()
	for i := 0; time.Now().Before(end); i++ {
		<-tick.C
		start := time.Now()
		emitBaseline(l, i)
		lat = append(lat, time.Since(start))
		if rssOK && time.Now().After(nextSample) {
			if r, ok := rssBytes(); ok {
				peak = max(peak, r)
			}
			runtime.ReadMemStats(&ms)
			heapPeak = max(heapPeak, ms.HeapInuse)
			nextSample = time.Now().Add(time.Second)
		}
	}
	during := h.Stats()
	down.Store(false)
	// In-flight batches may be sleeping up to MaxBackoff before their next try.
	if err := flushWithin(t, h, RetryBudget+10*time.Second); err != nil && !strings.Contains(err.Error(), "dropped") {
		t.Fatalf("Flush after outage: %v", err)
	}
	st := h.Stats()
	_, committed, _ := ns.snapshot()
	mib := func(b int64) float64 { return float64(b) / (1 << 20) }
	logResult(t, profileResult{Profile: "outage", Duration: dur.String(), RatePerSec: rate, Samples: len(lat),
		HandleP50: percentile(lat, 0.5).String(), HandleP95: percentile(lat, 0.95).String(),
		HandleP99: percentile(lat, 0.99).String(), HandleMax: percentile(lat, 1).String(),
		RSSBaselineMiB: mib(rss0), RSSPeakMiB: mib(peak), RSSDeltaMiB: mib(peak - rss0),
		ServerCommitted: len(committed), Stats: st,
		Extra: map[string]int64{"queued_events_at_outage_end": int64(during.QueuedEvents), "queued_bytes_at_outage_end": int64(during.QueuedBytes),
			"heap_inuse_baseline_bytes": int64(heap0), "heap_inuse_peak_bytes": int64(heapPeak)}})

	checkAccounting(t, st)
	if st.QueuedEvents != 0 || st.InFlightBatches != 0 {
		t.Fatalf("not drained: %+v", st)
	}
	if st.Handled != st.AckedEvents+st.TotalDropped() || uint64(len(committed)) != st.AckedEvents {
		t.Fatalf("inexact accounting: handled=%d acked=%d dropped=%d committed=%d", st.Handled, st.AckedEvents, st.TotalDropped(), len(committed))
	}
	for id, n := range committed {
		if n != 1 {
			t.Fatalf("event %s committed %d times", id, n)
		}
	}
	if during.QueuedEvents > DefaultQueueEvents || during.QueuedBytes > DefaultQueueBytes {
		t.Fatalf("queue exceeded bounds: %+v", during)
	}
	if capacity := DefaultQueueEvents + MaxInFlight*MaxBatchEvents; len(lat) > capacity && st.Dropped["queue_full"] == nil && st.Dropped["queue_bytes"] == nil {
		t.Fatalf("expected counted overflow for %d events over capacity %d: %v", len(lat), capacity, st.Dropped)
	}
	if rssOK && peak-rss0 >= 64<<20 {
		t.Fatalf("RSS delta %.1f MiB >= 64 MiB", mib(peak-rss0))
	}
}

// BenchmarkHandle is the optional microbenchmark (not an MVP gate): the
// synchronous cost of snapshot + redaction + serialization + enqueue.
func BenchmarkHandle(b *testing.B) {
	ns := newNativeServer(b)
	h, err := NewWithConfig(Config{Endpoint: ns.endpoint(), Token: testToken})
	if err != nil {
		b.Fatal(err)
	}
	defer h.Close(context.Background())
	l := slog.New(h)
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		emitBaseline(l, i)
		i++
		if i%1000 == 0 {
			_ = h.Flush(context.Background())
		}
	}
}
