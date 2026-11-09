package emitter

import (
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
)

// DropReason is a bounded label for a lost event. Labels never include
// event IDs, messages, tokens or attribute data.
type DropReason int

const (
	ReasonRedaction      DropReason = iota // redaction/conversion failed (fail closed)
	ReasonAttributeLimit                   // >128 attributes or nesting >8
	ReasonEventTooLarge                    // serialized event >32KiB
	ReasonInvalidEvent                     // reserved root key or time outside server admission window
	ReasonQueueFull                        // queue already holds the event limit (drop newest)
	ReasonQueueBytes                       // queue byte limit would be exceeded (drop newest)
	ReasonClosed                           // Handle after Close
	ReasonUnauthorized                     // 401/403: sender stopped, credential needs rotation
	ReasonRejected                         // terminal non-retryable HTTP status
	ReasonInvalidReceipt                   // 200 with a receipt that does not match the batch
	ReasonRetryExhausted                   // 60s retry budget ran out
	ReasonShutdown                         // still unsent when the Close deadline expired
	numReasons
)

var reasonNames = [numReasons]string{
	"redaction_failed", "attribute_limit", "event_too_large", "invalid_event", "queue_full", "queue_bytes",
	"closed", "unauthorized", "rejected", "invalid_receipt", "retry_exhausted", "shutdown_unsent",
}

func (r DropReason) String() string {
	if r < 0 || r >= numReasons {
		return "unknown"
	}
	return reasonNames[r]
}

var severityNames = [6]string{"TRACE", "DEBUG", "INFO", "WARN", "ERROR", "FATAL"}

func sevIndex(l slog.Level) int {
	n, _ := mapLevel(l)
	return (n - 1) / 4
}

type counters struct {
	handled           atomic.Uint64
	enqueued          atomic.Uint64
	sentBatches       atomic.Uint64
	ackedEvents       atomic.Uint64
	retries           atomic.Uint64
	rejectedBatches   atomic.Uint64
	redactionFailures atomic.Uint64
	diagSuppressed    atomic.Uint64
	dropped           [numReasons][6]atomic.Uint64
}

// Stats is a point-in-time copy of the SDK counters. Accounting invariant:
// Handled == Acked + TotalDropped() + QueuedEvents + InFlightEvents.
type Stats struct {
	Handled               uint64 // enabled records passed to Handle
	Enqueued              uint64 // records accepted into the bounded queue
	QueuedEvents          int
	QueuedBytes           int
	InFlightBatches       int
	InFlightEvents        int
	SentBatches           uint64 // batches acknowledged with a matching receipt
	AckedEvents           uint64
	Retries               uint64 // repeated sends of an unchanged batch
	RejectedBatches       uint64 // batches that ended without an acknowledgment
	RedactionFailures     uint64
	ShutdownUnsent        uint64
	DiagnosticsSuppressed uint64
	// Dropped counts lost events by reason and then severity (TRACE..FATAL).
	Dropped map[string]map[string]uint64
}

// TotalDropped sums Dropped.
func (s Stats) TotalDropped() uint64 {
	var n uint64
	for _, bySev := range s.Dropped {
		for _, v := range bySev {
			n += v
		}
	}
	return n
}

// Stats returns a consistent snapshot of the counters.
func (h *Handler) Stats() Stats {
	s := h.state
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statsLocked()
}

func (s *state) statsLocked() Stats {
	st := Stats{
		Handled:               s.c.handled.Load(),
		Enqueued:              s.c.enqueued.Load(),
		QueuedEvents:          len(s.q) - s.qHead,
		QueuedBytes:           s.qBytes,
		InFlightBatches:       len(s.inflight),
		SentBatches:           s.c.sentBatches.Load(),
		AckedEvents:           s.c.ackedEvents.Load(),
		Retries:               s.c.retries.Load(),
		RejectedBatches:       s.c.rejectedBatches.Load(),
		RedactionFailures:     s.c.redactionFailures.Load(),
		DiagnosticsSuppressed: s.c.diagSuppressed.Load(),
		Dropped:               map[string]map[string]uint64{},
	}
	for b := range s.inflight {
		st.InFlightEvents += len(b.sevs)
	}
	for r := DropReason(0); r < numReasons; r++ {
		for i := range severityNames {
			if v := s.c.dropped[r][i].Load(); v > 0 {
				if st.Dropped[r.String()] == nil {
					st.Dropped[r.String()] = map[string]uint64{}
				}
				st.Dropped[r.String()][severityNames[i]] = v
			}
		}
	}
	for i := range severityNames {
		st.ShutdownUnsent += s.c.dropped[ReasonShutdown][i].Load()
	}
	return st
}

// drop records lost events and emits one payload-free diagnostic.
func (s *state) drop(r DropReason, sev int, n int, status int) {
	if n <= 0 {
		return
	}
	s.c.dropped[r][sev].Add(uint64(n))
	s.diag(Diagnostic{Reason: r, Severity: severityNames[sev], Events: n, Status: status})
}

func (s *state) countBatchLocked(b *batch, r DropReason) {
	for _, sv := range b.sevs {
		s.c.dropped[r][sv].Add(1)
	}
}

// diag delivers to the hook at most one level deep. If the hook logs through
// this handler and that log is dropped, or another goroutine is already inside
// the hook, the notice is only counted, so the hook can never recurse.
func (s *state) diag(d Diagnostic) {
	if s.onDiag == nil {
		return
	}
	if !s.inDiag.CompareAndSwap(false, true) {
		s.c.diagSuppressed.Add(1)
		return
	}
	defer s.inDiag.Store(false)
	defer func() {
		if recover() != nil {
			s.c.diagSuppressed.Add(1)
		}
	}()
	s.onDiag(d)
}

// ErrUndelivered is matched (errors.Is) by DeliveryError when events were lost.
var ErrUndelivered = errors.New("hearthlog: events not delivered")

// DeliveryError reports a Flush/Close outcome without payload data.
type DeliveryError struct {
	Op       string // "flush" or "close"
	Unsent   int    // events still queued/in flight at the deadline (flush) or abandoned (close)
	Dropped  uint64 // cumulative lost events since the handler started
	Deadline error  // context error when the deadline expired, else nil
}

func (e *DeliveryError) Error() string {
	if e.Deadline != nil {
		return fmt.Sprintf("hearthlog %s: deadline exceeded with %d unsent events (%d dropped total)", e.Op, e.Unsent, e.Dropped)
	}
	return fmt.Sprintf("hearthlog %s: %d events dropped since start", e.Op, e.Dropped)
}

// Unwrap lets errors.Is match the context error and ErrUndelivered.
func (e *DeliveryError) Unwrap() []error {
	errs := []error{ErrUndelivered}
	if e.Deadline != nil {
		errs = append(errs, e.Deadline)
	}
	return errs
}
