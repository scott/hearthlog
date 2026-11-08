package emitter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func (s *state) run() {
	defer close(s.done)
	for payload := range s.queue {
		s.send(payload)
	}
}

func (s *state) send(payload []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		s.mu.Lock()
		s.pending--
		s.fails.Add(1)
		s.mu.Unlock()
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := s.client.Do(req)
	if err != nil {
		s.mu.Lock()
		s.pending--
		s.fails.Add(1)
		s.mu.Unlock()
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		s.mu.Lock()
		s.pending--
		s.fails.Add(1)
		s.mu.Unlock()
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(body) > 4096 {
		s.mu.Lock()
		s.pending--
		s.fails.Add(1)
		s.mu.Unlock()
		return
	}

	if err := model.ValidateJSON(body); err != nil {
		s.mu.Lock()
		s.pending--
		s.fails.Add(1)
		s.mu.Unlock()
		return
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		s.mu.Lock()
		s.pending--
		s.fails.Add(1)
		s.mu.Unlock()
		return
	}

	allowed := map[string]struct{}{
		"version": {}, "protocol": {}, "batch_id": {}, "accepted": {},
		"first_ingest_id": {}, "last_ingest_id": {}, "received_at": {},
	}
	for k := range raw {
		if _, ok := allowed[k]; !ok {
			s.mu.Lock()
			s.pending--
			s.fails.Add(1)
			s.mu.Unlock()
			return
		}
	}

	var receipt struct {
		Version  *int   `json:"version"`
		Protocol string `json:"protocol"`
		BatchID  string `json:"batch_id"`
		Accepted int    `json:"accepted"`
	}
	if err := json.Unmarshal(body, &receipt); err != nil {
		s.mu.Lock()
		s.pending--
		s.fails.Add(1)
		s.mu.Unlock()
		return
	}

	var batch model.Batch
	if err := json.Unmarshal(payload, &batch); err != nil {
		s.mu.Lock()
		s.pending--
		s.fails.Add(1)
		s.mu.Unlock()
		return
	}

	if receipt.Version == nil || *receipt.Version != model.SkeletonVersion || receipt.Protocol != model.SkeletonProtocol || receipt.Accepted != 1 || receipt.BatchID != batch.BatchID {
		s.mu.Lock()
		s.pending--
		s.fails.Add(1)
		s.mu.Unlock()
		return
	}

	s.mu.Lock()
	s.pending--
	s.sent.Add(1)
	s.mu.Unlock()
}

func (h *Handler) Flush(ctx context.Context) error {
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		deadline, _ = ctx.Deadline()
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		h.state.mu.Lock()
		pending := h.state.pending
		closed := h.state.closed
		drops := h.state.drops.Load()
		fails := h.state.fails.Load()
		h.state.mu.Unlock()

		if pending == 0 {
			if drops > 0 || fails > 0 {
				return errors.New("flush incomplete: drops or failures present")
			}
			return nil
		}
		if closed {
			return errors.New("flush aborted: handler closed")
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return context.DeadlineExceeded
			}
		}
	}
}

func (h *Handler) Close(ctx context.Context) error {
	h.state.mu.Lock()
	if !h.state.closed {
		h.state.closed = true
		close(h.state.queue)
	}
	h.state.mu.Unlock()

	select {
	case <-h.state.done:
		d := h.state.drops.Load()
		f := h.state.fails.Load()
		if d > 0 || f > 0 {
			return errors.New("close incomplete: drops or failures present")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
