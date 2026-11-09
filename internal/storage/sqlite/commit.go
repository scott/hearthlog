package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"example.invalid/hearthlog/internal/model"
)

var (
	ErrBusy              = errors.New("busy")
	ErrReplayUnsupported = errors.New("replay not supported")
	ErrStorage           = errors.New("storage error")
)

func (s *Store) Commit(ctx context.Context, scope model.Scope, b model.Batch) (model.Receipt, error) {
	var receipt model.Receipt
	if scope.WorkspaceID == "" || scope.SourceID == "" {
		return receipt, ErrStorage
	}
	if len(b.Events) != 1 {
		return receipt, ErrStorage
	}
	e := b.Events[0]

	select {
	case s.admit <- struct{}{}:
	case <-time.After(100 * time.Millisecond):
		return receipt, ErrBusy
	}
	defer func() { <-s.admit }()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	tx, err := s.writer.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelDefault})
	if err != nil {
		return receipt, ErrStorage
	}
	defer tx.Rollback()

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

	now := time.Now().UTC().UnixMicro()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO logs (workspace_id, source_id, batch_id, event_id, event_time, received_at, severity_number, severity_text, message, attributes, trace_id, span_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		scope.WorkspaceID, scope.SourceID, b.BatchID, e.EventID, e.EventTimeMicros, now,
		e.SeverityNumber, e.SeverityText, e.Message, string(e.Attributes),
		nilIfEmpty(e.TraceID), nilIfEmpty(e.SpanID),
	)
	if err != nil {
		return receipt, ErrStorage
	}

	var ingestID int64
	err = tx.QueryRowContext(ctx,
		`SELECT ingest_id FROM logs WHERE workspace_id=? AND source_id=? AND batch_id=?`,
		scope.WorkspaceID, scope.SourceID, b.BatchID,
	).Scan(&ingestID)
	if err != nil {
		return receipt, ErrStorage
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE metadata SET value=? WHERE key='watermark'`,
		fmt.Sprintf("%d", ingestID),
	)
	if err != nil {
		return receipt, ErrStorage
	}

	if err = tx.Commit(); err != nil {
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
