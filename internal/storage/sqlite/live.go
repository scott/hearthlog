package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"example.invalid/hearthlog/internal/model"
)

const (
	liveMatchLimit    = 200
	liveCatchupMax    = 5000
	liveCatchupWindow = 30 * time.Second
)

// liveScanLimit is the number of authorized rows one poll examines when a
// filter is set. The cursor moves to the last examined row, including rows
// the filter skips. A full-column scan of several thousand rows exceeds the
// 100000-step reader budget, so this stays at one page. Catch-up still
// counts up to liveCatchupMax with the cheaper id scan.
const liveScanLimit = 200

// LiveCursor is the poll position. received_at and ingest_id are the keyset.
// IngestID is also bounded by the watermark captured for that poll.
type LiveCursor struct {
	ReceivedAt int64
	IngestID   int64
}

// LiveFilter is the fixed generation filter. An empty filter matches every
// authorized row. MinSeverity <= 1 does not exclude a row.
type LiveFilter struct {
	Sources     []string
	MinSeverity int
	Terms       []string
	Predicates  []model.Predicate
}

func (f LiveFilter) active() bool {
	return len(f.Sources) > 0 || len(f.Terms) > 0 || len(f.Predicates) > 0 || f.MinSeverity > 1
}

// LivePage is one poll. Rows are closed before return. Gap is an explicit
// hole: the scan budget tripped, or the rows still ahead exceed the catch-up
// cap. Cursor is the highest examined (received_at, ingest_id). It does not
// move on a gap, and it is never set to AsOf.
type LivePage struct {
	Rows      []model.Record
	Cursor    LiveCursor
	Watermark int64
	AsOf      int64
	Gap       bool
	GapFrom   int64
	GapTo     int64
	CaughtUp  bool
	Idle      bool
}

// liveRowCols is the scanRecord column order. Both keyset arms use it.
const liveRowCols = `ingest_id, event_id, source_id, event_time, received_at, severity_number, severity_text, message, attributes`

// liveTailQuery reads the newest retained receipt in the workspace. The
// index is forced so a plain workspace filter is not planned as a walk of
// idx_logs_ws_ingest.
const liveTailQuery = `SELECT COALESCE(MAX(received_at), 0) FROM logs INDEXED BY ` + IndexLogsRetention + `
WHERE workspace_id = ? AND ingest_id <= ? AND received_at >= ?`

// liveSpanQuery is the poll keyset. Each arm seeks idx_logs_retention and
// stops at limit, so a cursor at the tail does not read older rows. The
// arms are ties at the cursor receipt, then strictly newer receipts.
func liveSpanQuery(cols string, workspace string, cursor LiveCursor, wm, cutoff int64, limit int) (string, []any) {
	from := `logs INDEXED BY ` + IndexLogsRetention
	scope := `workspace_id = ? AND ingest_id <= ?`
	var arms []string
	var args []any
	arm := func(where string, extra ...any) {
		arms = append(arms, `SELECT `+cols+` FROM (SELECT `+cols+` FROM `+from+` WHERE `+scope+` AND `+where+` ORDER BY received_at ASC, ingest_id ASC LIMIT ?)`)
		args = append(args, workspace, wm)
		args = append(args, extra...)
		args = append(args, limit)
	}
	if cursor.ReceivedAt >= cutoff {
		arm(`received_at = ? AND ingest_id > ?`, cursor.ReceivedAt, cursor.IngestID)
		arm(`received_at > ?`, cursor.ReceivedAt)
	} else {
		arm(`received_at >= ?`, cutoff)
	}
	q := `SELECT ` + cols + ` FROM (` + strings.Join(arms, ` UNION ALL `) + `) ORDER BY received_at ASC, ingest_id ASC LIMIT ?`
	args = append(args, limit)
	return q, args
}

// LiveTail is the committed end of the workspace. A new live view starts
// here so it does not replay retained history. The watermark is unchanged
// when no row remains.
func (s *Store) LiveTail(ctx context.Context, scope model.Scope) (LiveCursor, int64, error) {
	if scope.WorkspaceID == "" {
		return LiveCursor{}, 0, ErrStorage
	}
	var cur LiveCursor
	var wm int64
	err := s.withReader(ctx, func(ctx context.Context, conn *sql.Conn, budget *progressBudget) error {
		var err error
		wm, err = scanWatermark(ctx, conn)
		if err != nil {
			return err
		}
		budget.arm(stepLimit(ctx))
		var maxRecv int64
		if err := conn.QueryRowContext(ctx, liveTailQuery, scope.WorkspaceID, wm, retentionCutoff(s.now())).Scan(&maxRecv); err != nil {
			return err
		}
		cur = LiveCursor{ReceivedAt: maxRecv, IngestID: wm}
		return nil
	})
	if err != nil {
		return LiveCursor{}, 0, err
	}
	return cur, wm, nil
}

// Poll reads at most 200 matching committed rows after cursor. The scan is
// scoped to the workspace and stops at the watermark captured for this call.
// Sparse filters advance the cursor past examined non-matches. A tripped
// reader budget is a gap, not an empty page. asOf is the server time of the
// previous poll. The catch-up window is newest minus max(cursor, asOf), on
// every poll, so quiet time before asOf is not a gap and the keyset cursor
// stays on a real row.
func (s *Store) Poll(ctx context.Context, scope model.Scope, cursor LiveCursor, filter LiveFilter, asOf int64) (LivePage, error) {
	if scope.WorkspaceID == "" || cursor.ReceivedAt < 0 || cursor.IngestID < 0 || asOf < 0 {
		return LivePage{}, ErrStorage
	}
	page := LivePage{Cursor: cursor}
	err := s.withReader(ctx, func(ctx context.Context, conn *sql.Conn, budget *progressBudget) error {
		wm, err := scanWatermark(ctx, conn)
		if err != nil {
			return err
		}
		page.Watermark = wm
		page.AsOf = s.now().UTC().UnixMicro()
		if asOf > page.AsOf {
			asOf = page.AsOf
		}
		cutoff := retentionCutoff(s.now())
		budget.arm(stepLimit(ctx))
		n, maxRecv, err := countAhead(ctx, conn, scope.WorkspaceID, cursor, wm, cutoff)
		if err != nil {
			return err
		}
		if catchupGap(cursor.ReceivedAt, asOf, maxRecv, n) {
			page.Gap = true
			page.GapFrom = cursor.ReceivedAt
			page.GapTo = maxRecv
			return nil
		}
		budget.arm(stepLimit(ctx))
		return s.scanLive(ctx, conn, scope.WorkspaceID, cursor, filter, wm, cutoff, &page)
	})
	var budget *BudgetError
	if errors.As(err, &budget) {
		page.Gap = true
		page.Rows = nil
		page.Cursor = cursor
		if page.GapFrom == 0 {
			page.GapFrom = cursor.ReceivedAt
		}
		if page.GapTo == 0 {
			page.GapTo = s.now().UTC().UnixMicro()
		}
		return page, nil
	}
	if err != nil {
		return LivePage{}, err
	}
	if page.Gap {
		page.Rows = nil
		page.Cursor = cursor
	}
	return page, nil
}

func catchupGap(cursorRecv, asOf, maxRecv int64, n int) bool {
	if n > liveCatchupMax {
		return true
	}
	if n == 0 || maxRecv <= 0 {
		return false
	}
	base := cursorRecv
	if asOf > base {
		base = asOf
	}
	if base <= 0 {
		return false
	}
	return maxRecv-base > liveCatchupWindow.Microseconds()
}

func countAhead(ctx context.Context, conn *sql.Conn, workspace string, cursor LiveCursor, wm, cutoff int64) (int, int64, error) {
	q, args := liveSpanQuery(`received_at, ingest_id`, workspace, cursor, wm, cutoff, liveCatchupMax+1)
	rows, err := conn.QueryContext(ctx, q, args...)
	if err != nil {
		return 0, 0, err
	}
	n := 0
	var maxRecv int64
	for rows.Next() {
		var recv, id int64
		if err := rows.Scan(&recv, &id); err != nil {
			rows.Close()
			return 0, 0, err
		}
		n++
		if recv > maxRecv {
			maxRecv = recv
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, 0, err
	}
	return n, maxRecv, nil
}

func (s *Store) scanLive(ctx context.Context, conn *sql.Conn, workspace string, cursor LiveCursor, filter LiveFilter, wm, cutoff int64, page *LivePage) error {
	scanLimit := liveMatchLimit
	if filter.active() {
		scanLimit = liveScanLimit
	}
	q, args := liveSpanQuery(liveRowCols, workspace, cursor, wm, cutoff, scanLimit+1)
	rows, err := conn.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	examined := 0
	total := 0
	more := false
	var matches []model.Record
	cur := cursor
	for rows.Next() {
		if examined == scanLimit {
			more = true
			break
		}
		rec, recv, id, n, err := scanLiveRow(rows)
		if err != nil {
			return err
		}
		if liveMatch(rec, filter) {
			if total+n > model.MaxResultBytes {
				if len(matches) == 0 {
					return ErrTooBroad
				}
				more = true
				break
			}
			total += n
			matches = append(matches, rec)
		}
		examined++
		cur = LiveCursor{ReceivedAt: recv, IngestID: id}
		if len(matches) == liveMatchLimit {
			more = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	page.Rows = matches
	page.Cursor = cur
	page.CaughtUp = !more
	page.Idle = !more && len(matches) == 0
	return nil
}

func scanLiveRow(rows *sql.Rows) (model.Record, int64, int64, int, error) {
	rec, _, ingestID, n, err := scanRecord(rows)
	if err != nil {
		return rec, 0, 0, 0, err
	}
	recv, err := time.Parse(time.RFC3339Nano, rec.ReceivedAt)
	if err != nil {
		return rec, 0, 0, 0, err
	}
	return rec, recv.UnixMicro(), ingestID, n, nil
}

func liveMatch(rec model.Record, filter LiveFilter) bool {
	if filter.MinSeverity > 1 && rec.SeverityNumber < filter.MinSeverity {
		return false
	}
	if len(filter.Sources) > 0 {
		ok := false
		for _, src := range filter.Sources {
			if src == rec.SourceID {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(filter.Terms) > 0 {
		msg := strings.ToLower(rec.Message)
		for _, term := range filter.Terms {
			if term == "" || !strings.Contains(msg, strings.ToLower(term)) {
				return false
			}
		}
	}
	for _, p := range filter.Predicates {
		if !livePred(rec.Attributes, p) {
			return false
		}
	}
	return true
}

func livePred(attrs []byte, p model.Predicate) bool {
	dec := json.NewDecoder(strings.NewReader(string(attrs)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return p.Op == "missing"
	}
	cur := v
	for _, seg := range p.Path {
		m, ok := cur.(map[string]any)
		if !ok {
			return p.Op == "missing"
		}
		cur, ok = m[seg]
		if !ok {
			return p.Op == "missing"
		}
	}
	if p.Op == "missing" {
		return false
	}
	if p.Op != "eq" {
		return false
	}
	switch p.Type {
	case "null":
		return cur == nil
	case "boolean":
		b, ok := cur.(bool)
		if !ok {
			return false
		}
		return strconv.FormatBool(b) == strings.TrimSpace(string(p.Value))
	case "integer":
		num, ok := cur.(json.Number)
		if !ok || strings.ContainsAny(num.String(), ".eE") {
			return false
		}
		return num.String() == strings.TrimSpace(string(p.Value))
	case "real":
		num, ok := cur.(json.Number)
		if !ok || !strings.ContainsAny(num.String(), ".eE") {
			return false
		}
		got, err1 := num.Float64()
		want, err2 := strconv.ParseFloat(strings.TrimSpace(string(p.Value)), 64)
		return err1 == nil && err2 == nil && got == want
	case "string":
		s, ok := cur.(string)
		if !ok {
			return false
		}
		var want string
		if err := json.Unmarshal(p.Value, &want); err != nil {
			return false
		}
		return s == want
	default:
		return false
	}
}
