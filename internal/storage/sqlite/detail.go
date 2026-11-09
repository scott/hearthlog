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

var ErrNotFound = errors.New("not found")

// ErrRowExpired means the row is in this workspace but its receipt time is
// older than the live retention cutoff. A cursor page can still show it
// because that page keeps the cutoff from when the cursor was issued.
var ErrRowExpired = errors.New("row expired")

func (s *Store) Detail(ctx context.Context, scope model.Scope, id string) (model.Record, error) {
	var rec model.Record
	if scope.WorkspaceID == "" {
		return rec, ErrNotFound
	}
	idInt, err := strconv.ParseInt(id, 10, 64)
	if err != nil || idInt <= 0 || id != fmt.Sprintf("%d", idInt) {
		return rec, ErrNotFound
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cutoff := retentionCutoff(s.now())

	query := `SELECT ingest_id, event_id, source_id, event_time, received_at, severity_number, severity_text, message, attributes
		FROM logs
		WHERE workspace_id = ? AND ingest_id = ?`
	args := []any{scope.WorkspaceID, idInt}
	if scope.SourceID != "" {
		query += ` AND source_id = ?`
		args = append(args, scope.SourceID)
	}

	var ingestID int64
	var eventTime, receivedAt int64
	var attrs []byte
	err = s.reader.QueryRowContext(ctx, query, args...).Scan(
		&ingestID, &rec.EventID, &rec.SourceID, &eventTime, &receivedAt,
		&rec.SeverityNumber, &rec.SeverityText, &rec.Message, &attrs,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rec, ErrNotFound
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return rec, ErrQueryTimeout
		}
		return rec, ErrStorage
	}
	if receivedAt < cutoff {
		return model.Record{}, ErrRowExpired
	}

	rec.IngestID = fmt.Sprintf("%d", ingestID)
	rec.EventTime = time.UnixMicro(eventTime).UTC().Format(time.RFC3339Nano)
	rec.ReceivedAt = time.UnixMicro(receivedAt).UTC().Format(time.RFC3339Nano)
	rec.Attributes = attrs
	return rec, nil
}
