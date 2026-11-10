package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"example.invalid/hearthlog/internal/model"
)

var (
	ErrBusy              = errors.New("busy")
	ErrReplayUnsupported = errors.New("replay not supported")
	ErrConflict          = errors.New("batch conflict")
	ErrStorage           = errors.New("storage error")
)

func (s *Store) Commit(ctx context.Context, scope model.Scope, b model.Batch) (model.Receipt, error) {
	var receipt model.Receipt
	if scope.WorkspaceID == "" || scope.SourceID == "" {
		return receipt, ErrStorage
	}
	if b.Version != model.SkeletonVersion || len(b.Events) != 1 {
		return receipt, ErrStorage
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := s.submit(ctx, scope.SourceID, 1, classIngest, func(ctx context.Context) error {
		var err error
		receipt, err = s.commitSkeleton(ctx, scope, b)
		return err
	})
	if err != nil {
		return model.Receipt{}, err
	}
	return receipt, nil
}

func (s *Store) commitSkeleton(ctx context.Context, scope model.Scope, b model.Batch) (model.Receipt, error) {
	var receipt model.Receipt
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		if isBusy(err) {
			return receipt, ErrBusy
		}
		return receipt, ErrStorage
	}
	defer tx.Rollback()

	e := b.Events[0]
	var exists int
	err = tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM logs WHERE workspace_id=? AND source_id=? AND batch_id=?`,
		scope.WorkspaceID, scope.SourceID, b.BatchID,
	).Scan(&exists)
	if err != nil {
		return receipt, ErrStorage
	}
	if exists > 0 {
		return receipt, ErrReplayUnsupported
	}

	receiptAt, err := monotonicReceipt(ctx, tx, s.now())
	if err != nil {
		return receipt, ErrStorage
	}
	now := receiptAt.UnixMicro()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO logs (workspace_id, source_id, batch_id, event_id, event_time, received_at, severity_number, severity_text, message, attributes, trace_id, span_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		scope.WorkspaceID, scope.SourceID, b.BatchID, e.EventID, e.EventTimeMicros, now,
		e.SeverityNumber, e.SeverityText, e.Message, string(e.Attributes),
		nilIfEmpty(e.TraceID), nilIfEmpty(e.SpanID),
	)
	if err != nil {
		return receipt, ErrStorage
	}
	ingestID, err := res.LastInsertId()
	if err != nil {
		return receipt, ErrStorage
	}
	if _, err = tx.ExecContext(ctx, `UPDATE metadata SET value=? WHERE key='watermark'`, fmt.Sprintf("%d", ingestID)); err != nil {
		return receipt, ErrStorage
	}
	if err = tx.Commit(); err != nil {
		if isBusy(err) {
			return receipt, ErrBusy
		}
		return receipt, ErrStorage
	}
	receipt = model.Receipt{
		Version:       model.SkeletonVersion,
		Protocol:      model.SkeletonProtocol,
		BatchID:       b.BatchID,
		Accepted:      1,
		FirstIngestID: fmt.Sprintf("%d", ingestID),
		LastIngestID:  fmt.Sprintf("%d", ingestID),
		ReceivedAt:    time.UnixMicro(now).UTC().Format(time.RFC3339Nano),
	}
	return receipt, nil
}

func nilIfEmpty(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

// monotonicReceipt is max(wall clock, last persisted receipt). A step
// backward must not store a received_at behind a row the live keyset has
// already passed. The floor is the metadata row plus the table max, so it
// survives a restart and a delete of the newest row.
func monotonicReceipt(ctx context.Context, tx *sql.Tx, wall time.Time) (time.Time, error) {
	var hi, mx int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM metadata WHERE key='receipt_hi'), 0)`).Scan(&hi); err != nil {
		return time.Time{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(received_at), 0) FROM logs`).Scan(&mx); err != nil {
		return time.Time{}, err
	}
	if mx > hi {
		hi = mx
	}
	got := wall.UTC().Truncate(time.Microsecond).UnixMicro()
	if hi > got {
		got = hi
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO metadata(key, value) VALUES('receipt_hi', ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.FormatInt(got, 10)); err != nil {
		return time.Time{}, err
	}
	return time.UnixMicro(got).UTC(), nil
}

func (s *Store) now() time.Time {
	fn, _ := s.clock.Load().(func() time.Time)
	if fn == nil {
		return time.Now()
	}
	return fn()
}

// Now is the clock used for retention cutoffs and cursor expiry.
func (s *Store) Now() time.Time {
	return s.now()
}

// SetClock replaces the clock. A nil function restores time.Now.
func (s *Store) SetClock(now func() time.Time) {
	if now == nil {
		s.clock.Store(func() time.Time { return time.Now() })
		return
	}
	s.clock.Store(now)
}

// Watermark reads the persisted commit watermark. It is not an in-memory counter.
func (s *Store) Watermark(ctx context.Context) (int64, error) {
	var v string
	if err := s.reader.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='watermark'`).Scan(&v); err != nil {
		return 0, ErrStorage
	}
	var id int64
	if _, err := fmt.Sscan(v, &id); err != nil || id < 0 {
		return 0, ErrStorage
	}
	return id, nil
}
