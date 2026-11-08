package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"example.invalid/hearthlog/internal/model"
)

var ErrTooBroad = errors.New("query too broad")

func (s *Store) Search(ctx context.Context, scope model.Scope, q model.Query) ([]model.Record, error) {
	if scope.WorkspaceID == "" {
		return nil, ErrStorage
	}
	if len(q.Sources) > 32 {
		return nil, ErrStorage
	}
	if q.Limit < 1 || q.Limit > 200 {
		return nil, ErrStorage
	}

	from, err := time.Parse(time.RFC3339, q.From)
	if err != nil {
		return nil, ErrStorage
	}
	to, err := time.Parse(time.RFC3339, q.To)
	if err != nil {
		return nil, ErrStorage
	}
	fromMicros := from.UTC().UnixMicro()
	toMicros := to.UTC().UnixMicro()
	retentionCutoff := time.Now().UTC().Add(-7 * 24 * time.Hour).UnixMicro()

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var matchClause string
	var args []any
	args = append(args, scope.WorkspaceID)

	if len(q.Sources) > 0 {
		placeholders := make([]string, len(q.Sources))
		for i, src := range q.Sources {
			placeholders[i] = "?"
			args = append(args, src)
		}
		matchClause = " AND source_id IN (" + strings.Join(placeholders, ",") + ")"
	}

	matchExpr := ""
	if len(q.Terms) > 0 {
		quoted := make([]string, len(q.Terms))
		for i, t := range q.Terms {
			quoted[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
		}
		matchExpr = " AND logs_fts MATCH ?"
		args = append(args, strings.Join(quoted, " AND "))
	}

	query := `SELECT l.ingest_id, l.event_id, l.source_id, l.event_time, l.received_at, l.severity_number, l.severity_text, l.message, l.attributes
		FROM logs l`
	if matchExpr != "" {
		query += ` JOIN logs_fts ON logs_fts.rowid = l.ingest_id`
	}
	query += ` WHERE l.workspace_id = ?` + matchClause + matchExpr +
		` AND l.event_time >= ? AND l.event_time < ?` +
		` AND l.received_at >= ?` +
		` AND l.severity_number >= ?` +
		` ORDER BY l.event_time DESC, l.ingest_id DESC LIMIT ?`

	args = append(args, fromMicros, toMicros, retentionCutoff, q.MinSeverity, q.Limit+1)

	rows, err := s.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, ErrStorage
	}
	defer rows.Close()

	var records []model.Record
	var cumulative int
	for rows.Next() {
		var r model.Record
		var ingestID int64
		var eventTime, receivedAt int64
		var attrs []byte
		if err := rows.Scan(&ingestID, &r.EventID, &r.SourceID, &eventTime, &receivedAt, &r.SeverityNumber, &r.SeverityText, &r.Message, &attrs); err != nil {
			return nil, ErrStorage
		}
		r.IngestID = fmt.Sprintf("%d", ingestID)
		r.EventTime = time.UnixMicro(eventTime).UTC().Format(time.RFC3339Nano)
		r.ReceivedAt = time.UnixMicro(receivedAt).UTC().Format(time.RFC3339Nano)
		r.Attributes = attrs

		// Conservative per-record overhead: struct padding, slice headers, fixed fields
		size := len(r.EventID) + len(r.IngestID) + len(r.SourceID) + len(r.EventTime) +
			len(r.ReceivedAt) + len(r.SeverityText) + len(r.Message) + len(attrs) + 256
		if cumulative+size > model.MaxResultBytes {
			return nil, ErrTooBroad
		}
		cumulative += size
		records = append(records, r)
		if len(records) > q.Limit {
			return nil, ErrTooBroad
		}
	}
	if err := rows.Err(); err != nil {
		return nil, ErrStorage
	}
	return records, nil
}
