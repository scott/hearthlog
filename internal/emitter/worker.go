package emitter

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"example.invalid/hearthlog/internal/model"
)

// envelope overhead: {"version":1,"batch_id":"<36>","events":[...]}
const envelopeBytes = len(`{"version":1,"batch_id":"00000000-0000-4000-8000-000000000000","events":[]}`)

// dispatch forms batches (<=200 events AND <=256KiB, or after 250ms) and
// keeps at most MaxInFlight batches outstanding. It never blocks Handle.
func (s *state) dispatch() {
	defer s.wg.Done()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		s.mu.Lock()
		if s.life.Err() != nil {
			n := s.dropQueueLocked(ReasonShutdown)
			s.mu.Unlock()
			s.diagCount(ReasonShutdown, n)
			return
		}
		if s.authStop {
			if n := s.dropQueueLocked(ReasonUnauthorized); n > 0 {
				s.mu.Unlock()
				s.diagCount(ReasonUnauthorized, n)
				continue
			}
		}
		n := len(s.q) - s.qHead
		if s.closed && n == 0 {
			s.mu.Unlock()
			return
		}
		var wait time.Duration = -1
		if n > 0 && len(s.inflight) < MaxInFlight {
			age := time.Since(s.q[s.qHead].at)
			full := n >= s.batchEvents || s.qBytes+envelopeBytes+n >= MaxBatchBytes
			if full || s.closed || s.urgent > 0 || age >= s.tune.batchInterval {
				b := s.takeBatchLocked()
				s.inflight[b] = struct{}{}
				s.wg.Add(1)
				s.mu.Unlock()
				go s.deliver(b)
				continue
			}
			wait = s.tune.batchInterval - age
		}
		s.mu.Unlock()

		var tc <-chan time.Time
		if wait >= 0 {
			timer.Reset(wait)
			tc = timer.C
		}
		select {
		case <-s.wake:
		case <-tc:
		case <-s.life.Done():
		}
		if tc != nil && !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}

func (s *state) takeBatchLocked() *batch {
	b := &batch{id: newUUIDv4()}
	var buf bytes.Buffer
	version := model.SkeletonVersion
	if s.native {
		version = 1
	}
	buf.WriteString(`{"version":`)
	buf.WriteString(strconv.Itoa(version))
	buf.WriteString(`,"batch_id":"`)
	buf.WriteString(b.id)
	buf.WriteString(`","events":[`)
	size := envelopeBytes
	for s.qHead < len(s.q) && len(b.seqs) < s.batchEvents {
		e := s.q[s.qHead]
		add := len(e.data)
		if len(b.seqs) > 0 {
			add++ // comma
		}
		if len(b.seqs) > 0 && size+add > MaxBatchBytes {
			break
		}
		if len(b.seqs) > 0 {
			buf.WriteByte(',')
		}
		buf.Write(e.data)
		size += add
		b.seqs = append(b.seqs, e.seq)
		b.sevs = append(b.sevs, e.sev)
		s.qBytes -= len(e.data)
		s.q[s.qHead] = queued{}
		s.qHead++
	}
	buf.WriteString(`]}`)
	b.payload = buf.Bytes()
	b.firstSeq = b.seqs[0]
	s.compactLocked()
	return b
}

func (s *state) compactLocked() {
	if s.qHead == len(s.q) {
		s.q = s.q[:0]
		s.qHead = 0
	} else if s.qHead > 1024 && s.qHead*2 > len(s.q) {
		s.q = append(s.q[:0], s.q[s.qHead:]...)
		s.qHead = 0
	}
}

// dropQueueLocked counts every queued event as lost. The diagnostic hook is
// invoked by the caller after unlocking (diagCount), never under s.mu, so a
// hook that logs through this handler cannot deadlock.
func (s *state) dropQueueLocked(r DropReason) int {
	n := len(s.q) - s.qHead
	for i := s.qHead; i < len(s.q); i++ {
		s.c.dropped[r][s.q[i].sev].Add(1)
		s.q[i] = queued{}
	}
	s.qHead = len(s.q)
	s.qBytes = 0
	s.compactLocked()
	if n > 0 {
		s.notifyLocked()
	}
	return n
}

func (s *state) diagCount(r DropReason, n int) {
	if n > 0 {
		s.diag(Diagnostic{Reason: r, Events: n})
	}
}

// notifyLocked wakes Flush waiters after any progress.
func (s *state) notifyLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

type outcome int

const (
	outAck outcome = iota
	outRetry
	outTerminal
	outUnauthorized
	outInvalidReceipt
)

// deliver sends one immutable batch. Ambiguous failures (network, timeout,
// 429, 5xx) resend the identical bytes and batch_id with jittered exponential
// backoff (250ms..5s) or a Retry-After bounded by the remaining 60s budget.
// Batches are never split or re-identified.
func (s *state) deliver(b *batch) {
	defer s.wg.Done()
	start := s.clk.Now()
	deadline := start.Add(s.tune.retryBudget)
	for attempt := 1; ; attempt++ {
		if attempt > 1 {
			s.c.retries.Add(1)
		}
		remaining := deadline.Sub(s.clk.Now())
		if remaining <= 0 {
			s.finish(b, ReasonRetryExhausted, 0)
			return
		}
		timeout := min(s.tune.reqTimeout, remaining)
		out, status, retryAfter := s.post(b, timeout)
		switch out {
		case outAck:
			s.finishAck(b)
			return
		case outTerminal:
			s.finish(b, ReasonRejected, status)
			return
		case outInvalidReceipt:
			s.finish(b, ReasonInvalidReceipt, status)
			return
		case outUnauthorized:
			s.mu.Lock()
			s.authStop = true
			s.mu.Unlock()
			s.finish(b, ReasonUnauthorized, status)
			s.poke()
			return
		}
		if s.life.Err() != nil {
			s.finish(b, ReasonShutdown, 0)
			return
		}
		delay := backoff(attempt, s.clk.Jitter())
		if retryAfter >= 0 {
			delay = retryAfter
		}
		if s.clk.Now().Add(delay).After(deadline) {
			s.finish(b, ReasonRetryExhausted, status)
			return
		}
		if err := s.clk.Sleep(s.life, delay); err != nil {
			s.finish(b, ReasonShutdown, 0)
			return
		}
	}
}

// backoff returns a jittered delay in [base/2, base] for base = 250ms*2^(n-1)
// capped at 5s, never below 250ms.
func backoff(attempt int, jitter float64) time.Duration {
	base := MinBackoff
	for i := 1; i < attempt && base < MaxBackoff; i++ {
		base *= 2
	}
	base = min(base, MaxBackoff)
	d := base/2 + time.Duration(jitter*float64(base/2))
	return max(d, MinBackoff)
}

func (s *state) finishAck(b *batch) {
	s.mu.Lock()
	delete(s.inflight, b)
	s.c.sentBatches.Add(1)
	s.c.ackedEvents.Add(uint64(len(b.seqs)))
	s.notifyLocked()
	s.mu.Unlock()
	s.poke()
}

// finish records a lost batch. Counters change in the same critical section
// that removes it from flight, so Stats and Flush never see it in neither or
// both; the payload-free diagnostic is delivered after unlocking.
func (s *state) finish(b *batch, r DropReason, status int) {
	s.mu.Lock()
	delete(s.inflight, b)
	s.c.rejectedBatches.Add(1)
	s.countBatchLocked(b, r)
	s.notifyLocked()
	s.mu.Unlock()
	s.diag(Diagnostic{Reason: r, Events: len(b.sevs), Status: status})
	s.poke()
}

// post performs one request on the sender's own lifecycle context (never a
// caller's request context). retryAfter is -1 when absent.
func (s *state) post(b *batch, timeout time.Duration) (outcome, int, time.Duration) {
	ctx, cancel := context.WithTimeout(s.life, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(b.payload))
	if err != nil {
		return outTerminal, 0, -1
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := s.client.Do(req)
	if err != nil {
		return outRetry, 0, -1 // network failure or timeout: outcome unknown
	}
	defer resp.Body.Close()
	status := resp.StatusCode
	switch {
	case status == http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxReceiptBytes+1))
		if err != nil {
			return outRetry, status, -1 // committed-or-not is unknown: replay same batch
		}
		if s.validReceipt(body, b) {
			return outAck, status, -1
		}
		return outInvalidReceipt, status, -1
	case status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable ||
		status == http.StatusInternalServerError || status == http.StatusBadGateway ||
		status == http.StatusGatewayTimeout:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxReceiptBytes))
		return outRetry, status, parseRetryAfter(resp.Header.Get("Retry-After"), s.clk.Now())
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxReceiptBytes))
		return outUnauthorized, status, -1
	default:
		// 3xx (redirects are never followed), 400/404/409/413/415/501 etc. are terminal.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxReceiptBytes))
		return outTerminal, status, -1
	}
}

// parseRetryAfter accepts delta-seconds or an HTTP-date; -1 when absent/invalid.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return -1
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 || n > maxRetryAfterSeconds {
			return -1
		}
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		d := t.Sub(now)
		if d < 0 {
			return 0
		}
		if d > maxRetryAfterSeconds*time.Second {
			return -1
		}
		return d
	}
	return -1
}

func (s *state) validReceipt(body []byte, b *batch) bool {
	if len(body) > maxReceiptBytes {
		return false
	}
	if err := model.ValidateJSON(body); err != nil {
		return false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return false
	}
	allowed := map[string]struct{}{
		"version": {}, "batch_id": {}, "accepted": {},
		"first_ingest_id": {}, "last_ingest_id": {}, "received_at": {},
	}
	if s.native {
		allowed["replay_until"] = struct{}{}
	} else {
		allowed["protocol"] = struct{}{}
	}
	for k := range raw {
		if _, ok := allowed[k]; !ok {
			return false
		}
	}
	var r struct {
		Version  *int    `json:"version"`
		Protocol *string `json:"protocol"`
		BatchID  string  `json:"batch_id"`
		Accepted *int    `json:"accepted"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return false
	}
	if r.Version == nil || r.Accepted == nil || *r.Accepted != len(b.seqs) || r.BatchID != b.id {
		return false
	}
	if s.native {
		return *r.Version == 1
	}
	return *r.Version == model.SkeletonVersion && r.Protocol != nil && *r.Protocol == model.SkeletonProtocol
}

// lowestUnresolvedLocked is the smallest sequence not yet acked or dropped.
func (s *state) lowestUnresolvedLocked() uint64 {
	low := s.nextSeq
	if s.qHead < len(s.q) {
		low = min(low, s.q[s.qHead].seq)
	}
	for b := range s.inflight {
		low = min(low, b.firstSeq)
	}
	return low
}

func (s *state) unsentThroughLocked(target uint64) int {
	n := 0
	for i := s.qHead; i < len(s.q) && s.q[i].seq <= target; i++ {
		n++
	}
	for b := range s.inflight {
		for _, q := range b.seqs {
			if q <= target {
				n++
			}
		}
	}
	return n
}

// Flush is a barrier: it waits until every event accepted before the call is
// acknowledged or dropped, sending partial batches immediately. On ctx expiry
// it returns a *DeliveryError with the unsent count (errors.Is ctx.Err()).
// It also returns a *DeliveryError (errors.Is ErrUndelivered) when any event
// has been dropped since the handler started; see Stats for reasons.
func (h *Handler) Flush(ctx context.Context) error {
	s := h.state
	s.mu.Lock()
	target := s.nextSeq - 1
	s.urgent++
	s.mu.Unlock()
	s.poke()
	defer func() {
		s.mu.Lock()
		s.urgent--
		s.mu.Unlock()
	}()
	for {
		s.mu.Lock()
		if s.lowestUnresolvedLocked() > target {
			st := s.statsLocked()
			s.mu.Unlock()
			if d := st.TotalDropped(); d > 0 {
				return &DeliveryError{Op: "flush", Dropped: d}
			}
			return nil
		}
		ch := s.changed
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			s.mu.Lock()
			unsent := s.unsentThroughLocked(target)
			st := s.statsLocked()
			s.mu.Unlock()
			return &DeliveryError{Op: "flush", Unsent: unsent, Dropped: st.TotalDropped(), Deadline: ctx.Err()}
		}
	}
}

// Close stops acceptance (later records count as dropped/closed), drains the
// queue until ctx expires, then cancels in-flight work and counts what was
// abandoned as shutdown_unsent. It is idempotent and safe to call
// concurrently; every call returns the same result once shutdown completes.
// No goroutines remain after Close returns.
func (h *Handler) Close(ctx context.Context) error {
	s := h.state
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.notifyLocked()
		s.mu.Unlock()
		s.poke()
	})
	select {
	case <-s.done:
	case <-ctx.Done():
		s.mu.Lock()
		if s.closeExpire == nil {
			s.closeExpire = ctx.Err()
		}
		s.mu.Unlock()
		s.cancel()
		<-s.done
	}
	s.resultOnce.Do(func() {
		s.cancel()
		s.client.CloseIdleConnections()
		s.mu.Lock()
		st := s.statsLocked()
		expired := s.closeExpire
		s.mu.Unlock()
		switch {
		case expired != nil:
			s.closeResult = &DeliveryError{Op: "close", Unsent: int(st.ShutdownUnsent), Dropped: st.TotalDropped(), Deadline: expired}
		case st.TotalDropped() > 0:
			s.closeResult = &DeliveryError{Op: "close", Unsent: int(st.ShutdownUnsent), Dropped: st.TotalDropped()}
		}
	})
	return s.closeResult
}
