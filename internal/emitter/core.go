package emitter

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Handler is a bounded, redacting, asynchronous slog.Handler. Handle never
// waits for the network: it snapshots, redacts and serializes the record
// synchronously, then appends it to a bounded in-memory queue or drops it
// (newest first) with a counted reason. Handlers derived with WithAttrs and
// WithGroup are immutable and share one queue and sender.
type Handler struct {
	state   *state
	base    map[string]any
	groups  []string
	invalid bool
	// invalidReason is why a derived handler fails closed (default redaction_failed).
	invalidReason DropReason
}

type queued struct {
	seq  uint64
	data []byte
	sev  int
	at   time.Time
}

type batch struct {
	id       string
	payload  []byte // immutable after creation: retries resend identical bytes
	firstSeq uint64
	seqs     []uint64
	sevs     []int
}

type state struct {
	client   *http.Client
	endpoint string
	token    string
	native   bool
	secrets  []string
	level    slog.Leveler
	onDiag   func(Diagnostic)
	inDiag   atomic.Bool

	queueEvents int
	queueBytes  int
	batchEvents int
	clk         clock
	tune        tuning

	life   context.Context // sender lifecycle; independent of caller contexts
	cancel context.CancelFunc
	wg     sync.WaitGroup
	wake   chan struct{}
	done   chan struct{}

	mu       sync.Mutex
	q        []queued
	qHead    int
	qBytes   int
	nextSeq  uint64
	inflight map[*batch]struct{}
	closed   bool
	urgent   int  // pending Flush calls: send partial batches immediately
	authStop bool // 401/403 seen: sender stopped, credential must be rotated
	changed  chan struct{}

	closeOnce   sync.Once
	resultOnce  sync.Once
	closeResult error
	closeExpire error // ctx error of the first Close whose deadline expired

	c counters
}

// Enabled reports whether level meets the configured minimum.
func (h *Handler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.state.level.Level()
}

// Handle snapshots and enqueues r. The caller's context is deliberately not
// consulted: a canceled request context must not suppress diagnostic logs.
// It always returns nil because slog ignores Handle errors; use Stats and
// Config.OnDiagnostic to observe losses.
func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	s := h.state
	s.c.handled.Add(1)
	sev := sevIndex(r.Level)
	if h.invalid {
		if h.invalidReason == ReasonRedaction {
			s.c.redactionFailures.Add(1)
		}
		s.drop(h.invalidReason, sev, 1, 0)
		return nil
	}
	ev, err := h.snapshot(r)
	if err != nil {
		reason := reasonFor(err)
		if reason == ReasonRedaction {
			s.c.redactionFailures.Add(1)
		}
		s.drop(reason, sev, 1, 0)
		return nil
	}
	data, err := json.Marshal(ev)
	if err != nil {
		s.c.redactionFailures.Add(1)
		s.drop(ReasonRedaction, sev, 1, 0)
		return nil
	}
	if len(data) > MaxEventBytes {
		s.drop(ReasonEventTooLarge, sev, 1, 0)
		return nil
	}
	reason, ok := s.enqueue(data, sev)
	if !ok {
		s.drop(reason, sev, 1, 0)
	}
	return nil
}

func (s *state) enqueue(data []byte, sev int) (DropReason, bool) {
	now := time.Now()
	s.mu.Lock()
	switch {
	case s.closed:
		s.mu.Unlock()
		return ReasonClosed, false
	case s.authStop:
		s.mu.Unlock()
		return ReasonUnauthorized, false
	case len(s.q)-s.qHead >= s.queueEvents:
		s.mu.Unlock()
		return ReasonQueueFull, false
	case s.qBytes+len(data) > s.queueBytes:
		s.mu.Unlock()
		return ReasonQueueBytes, false
	}
	s.q = append(s.q, queued{seq: s.nextSeq, data: data, sev: sev, at: now})
	s.nextSeq++
	s.qBytes += len(data)
	s.c.enqueued.Add(1)
	s.mu.Unlock()
	s.poke()
	return 0, true
}

func (s *state) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func newUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("cryptographic RNG failure")
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// clock is injectable so retry/backoff tests run on a fake clock.
type clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
	Jitter() float64 // [0,1)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (realClock) Jitter() float64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0.5
	}
	v := uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 | uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48
	return float64(v) / float64(uint64(1)<<56)
}
