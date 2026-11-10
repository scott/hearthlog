package sqlite

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"example.invalid/hearthlog/internal/model"
	"github.com/mattn/go-sqlite3"
)

const (
	faultBeforeCommit int32 = 1
	faultAfterCommit  int32 = 2
)

// errKilled is a deterministic stand-in for a process kill. Before commit it
// abandons the transaction. After commit it drops the connection before the
// caller receives the receipt.
var errKilled = errors.New("killed")

type nativeResult struct {
	Body []byte
}

// CommitNative admits one native batch onto the single writer and commits it
// atomically, or returns the stored receipt when the same batch is replayed.
func (s *Store) CommitNative(ctx context.Context, scope model.Scope, b model.Batch, payloadBytes int) (nativeResult, error) {
	var out nativeResult
	if scope.WorkspaceID == "" || scope.SourceID == "" || b.Version != model.NativeVersion || len(b.Events) == 0 {
		return out, ErrStorage
	}
	if payloadBytes < 1 {
		payloadBytes = 1
	}
	err := s.submit(ctx, scope.WorkspaceID+"/"+scope.SourceID, payloadBytes, classIngest, func(ctx context.Context) error {
		var err error
		out, err = s.commitNativeRetry(ctx, scope, b)
		return err
	})
	if err != nil {
		return nativeResult{}, err
	}
	return out, nil
}

// SubmitMaintenance queues a non-ingest job. The scheduler runs one after at
// most 10 ingest jobs. There is no automatic checkpoint loop in this slice.
func (s *Store) SubmitMaintenance(ctx context.Context, payloadBytes int, fn func(context.Context) error) error {
	if fn == nil {
		return ErrStorage
	}
	return s.submit(ctx, "", payloadBytes, classMaint, fn)
}

func (s *Store) commitNativeRetry(ctx context.Context, scope model.Scope, b model.Batch) (nativeResult, error) {
	var last error
	for attempt := 0; attempt < 8; attempt++ {
		if err := ctx.Err(); err != nil {
			if last != nil {
				return nativeResult{}, ErrBusy
			}
			return nativeResult{}, err
		}
		res, err := s.commitNativeOnce(ctx, scope, b)
		if err == nil {
			return res, nil
		}
		if errors.Is(err, errKilled) || errors.Is(err, ErrConflict) || errors.Is(err, model.ErrValidation) {
			return nativeResult{}, err
		}
		if !isBusy(err) {
			return nativeResult{}, ErrStorage
		}
		last = err
		timer := time.NewTimer(time.Duration(5+rand.IntN(20)) * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nativeResult{}, ErrBusy
		}
	}
	return nativeResult{}, ErrBusy
}

func (s *Store) commitNativeOnce(ctx context.Context, scope model.Scope, b model.Batch) (nativeResult, error) {
	sum, _, err := model.CanonicalDigest(b)
	if err != nil {
		return nativeResult{}, ErrStorage
	}
	hashHex := hex.EncodeToString(sum[:])
	wall := s.now().UTC().Truncate(time.Microsecond)

	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return nativeResult{}, err
	}
	defer tx.Rollback()

	now, err := monotonicReceipt(ctx, tx, wall)
	if err != nil {
		return nativeResult{}, err
	}

	var sources int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sources WHERE workspace_id=? AND source_id=?`, scope.WorkspaceID, scope.SourceID).Scan(&sources); err != nil {
		return nativeResult{}, err
	}
	if sources != 1 {
		return nativeResult{}, ErrStorage
	}

	var gotHash, stored string
	var until int64
	err = tx.QueryRowContext(ctx,
		`SELECT content_hash, body, replay_until FROM receipts WHERE workspace_id=? AND source_id=? AND batch_id=?`,
		scope.WorkspaceID, scope.SourceID, b.BatchID,
	).Scan(&gotHash, &stored, &until)
	if err == nil {
		if now.Before(time.UnixMicro(until).UTC()) {
			if subtle.ConstantTimeCompare([]byte(gotHash), []byte(hashHex)) == 1 {
				return nativeResult{Body: []byte(stored)}, nil
			}
			return nativeResult{}, ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM receipts WHERE workspace_id=? AND source_id=? AND batch_id=?`, scope.WorkspaceID, scope.SourceID, b.BatchID); err != nil {
			return nativeResult{}, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nativeResult{}, err
	}

	for i := range b.Events {
		if err := model.AdmitEventTime(b.Events[i].EventTimeMicros, wall); err != nil {
			return nativeResult{}, err
		}
	}

	received := now.UnixMicro()
	var first, last int64
	for i := range b.Events {
		e := &b.Events[i]
		res, err := tx.ExecContext(ctx,
			`INSERT INTO logs (workspace_id, source_id, batch_id, event_id, event_time, received_at, severity_number, severity_text, message, attributes, trace_id, span_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			scope.WorkspaceID, scope.SourceID, b.BatchID, e.EventID, e.EventTimeMicros, received,
			e.SeverityNumber, e.SeverityText, e.Message, string(e.Attributes),
			nilIfEmpty(e.TraceID), nilIfEmpty(e.SpanID),
		)
		if err != nil {
			return nativeResult{}, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nativeResult{}, err
		}
		if i == 0 {
			first = id
		}
		last = id
	}
	if _, err := tx.ExecContext(ctx, `UPDATE metadata SET value=? WHERE key='watermark'`, fmt.Sprintf("%d", last)); err != nil {
		return nativeResult{}, err
	}
	body, err := model.EncodeNativeReceipt(model.NativeReceipt{
		Version:       model.NativeVersion,
		BatchID:       b.BatchID,
		Accepted:      len(b.Events),
		FirstIngestID: fmt.Sprintf("%d", first),
		LastIngestID:  fmt.Sprintf("%d", last),
		ReceivedAt:    model.FormatReceiptTime(now),
		ReplayUntil:   model.FormatReceiptTime(now.Add(48 * time.Hour)),
	})
	if err != nil {
		return nativeResult{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO receipts (workspace_id, source_id, batch_id, content_hash, accepted, first_ingest_id, last_ingest_id, received_at, replay_until, body)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		scope.WorkspaceID, scope.SourceID, b.BatchID, hashHex, len(b.Events), first, last, received, now.Add(48*time.Hour).UnixMicro(), string(body),
	); err != nil {
		return nativeResult{}, err
	}

	if s.fault.CompareAndSwap(faultBeforeCommit, 0) {
		_ = s.writer.Close()
		return nativeResult{}, errKilled
	}
	if err := tx.Commit(); err != nil {
		if rec, ok := s.lookupReceipt(ctx, scope, b.BatchID, hashHex, now); ok {
			return nativeResult{Body: rec}, nil
		}
		return nativeResult{}, err
	}
	if s.fault.CompareAndSwap(faultAfterCommit, 0) {
		_ = s.writer.Close()
		return nativeResult{}, errKilled
	}
	return nativeResult{Body: body}, nil
}

func (s *Store) lookupReceipt(ctx context.Context, scope model.Scope, batchID, hashHex string, now time.Time) ([]byte, bool) {
	var gotHash, stored string
	var until int64
	err := s.writer.QueryRowContext(ctx,
		`SELECT content_hash, body, replay_until FROM receipts WHERE workspace_id=? AND source_id=? AND batch_id=?`,
		scope.WorkspaceID, scope.SourceID, batchID,
	).Scan(&gotHash, &stored, &until)
	if err != nil {
		return nil, false
	}
	if subtle.ConstantTimeCompare([]byte(gotHash), []byte(hashHex)) != 1 {
		return nil, false
	}
	if !now.Before(time.UnixMicro(until).UTC()) {
		return nil, false
	}
	return []byte(stored), true
}

func isBusy(err error) bool {
	var se sqlite3.Error
	if errors.As(err, &se) {
		return se.Code == sqlite3.ErrBusy || se.Code == sqlite3.ErrLocked
	}
	return false
}
