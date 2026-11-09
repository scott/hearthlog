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
	"github.com/mattn/go-sqlite3"
)

const queryTimeout = 3 * time.Second

var ErrTooBroad = errors.New("query too broad")
var ErrQueryTimeout = errors.New("query timeout")

// BudgetError is a query that hit the VM-step budget. Steps is the handler
// count at abort. The HTTP body stays a fixed string.
type BudgetError struct {
	Steps int64
}

func (e *BudgetError) Error() string { return ErrTooBroad.Error() }
func (e *BudgetError) Unwrap() error { return ErrTooBroad }

// Page is one materialized historical result. Rows are closed before return.
type Page struct {
	Rows  []model.Record
	Next  string
	steps int64
}

type keyset struct {
	horizon int64
	cutoff  int64
	hasLast bool
	lastT   int64
	lastID  int64
}

type budgetCtxKey struct{}

// normalizeQuery accepts the strict native query. Callers that format
// time.Now() with nanoseconds are truncated to microseconds so existing
// fixtures keep working. A query that already validates is left unchanged
// so a signed cursor matches the caller's original text.
func normalizeQuery(q model.Query) (model.Query, error) {
	if err := model.ValidateQuery(q); err == nil {
		return q, nil
	}
	from, err1 := time.Parse(time.RFC3339Nano, q.From)
	to, err2 := time.Parse(time.RFC3339Nano, q.To)
	if err1 != nil || err2 != nil {
		return q, model.ErrValidation
	}
	q.From = from.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	q.To = to.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	if err := model.ValidateQuery(q); err != nil {
		return q, err
	}
	return q, nil
}

// retentionCutoff is the receipt-time horizon, truncated to the UTC minute.
// A cursor keeps the cutoff it was issued with. It expires only when a row
// inside that snapshot has a received_at the new cutoff would drop.
func retentionCutoff(now time.Time) int64 {
	return now.UTC().Truncate(time.Minute).Add(-7 * 24 * time.Hour).UnixMicro()
}

// Search returns the first page of rows. Extra matches are a cursor from
// ReadPage, not an error.
func (s *Store) Search(ctx context.Context, scope model.Scope, q model.Query) ([]model.Record, error) {
	page, err := s.ReadPage(ctx, scope, q)
	if err != nil {
		return nil, err
	}
	return page.Rows, nil
}

// ReadPage runs one bounded historical page. The signed cursor freezes
// workspace, filters, the committed watermark, and the receipt cutoff.
func (s *Store) ReadPage(ctx context.Context, scope model.Scope, q model.Query) (Page, error) {
	if scope.WorkspaceID == "" {
		return Page{}, ErrStorage
	}
	q, err := normalizeQuery(q)
	if err != nil {
		return Page{}, err
	}
	var page Page
	var key []byte
	var issued cursorBody
	err = s.withReader(ctx, func(ctx context.Context, conn *sql.Conn, budget *progressBudget) error {
		macHex, epoch, err := loadCursorMeta(ctx, conn)
		if err != nil {
			return err
		}
		key, err = decodeMAC(macHex)
		if err != nil {
			return err
		}
		ks, err := s.keysetFor(ctx, conn, scope, q, epoch, key)
		if err != nil {
			return err
		}
		// The retention EXISTS runs on this connection before the page. Re-arm
		// so that check cannot consume the page budget, including no-term pages.
		// Term queries try FTS-first. Each pass keeps the 100000-step cap.
		// A tripped FTS pass is discarded and the time-index pass runs under
		// a fresh cap, so a common token can still stop after LIMIT rows.
		// The binary does not run ANALYZE; the join order is not left to the planner.
		budget.arm(stepLimit(ctx))
		order := orderTime
		if len(q.Terms) > 0 {
			order = orderFTS
		}
		recs, more, lastT, lastID, err := readOrdered(ctx, conn, scope.WorkspaceID, q, ks, order)
		if len(q.Terms) > 0 && budget.tripped() {
			budget.arm(stepLimit(ctx))
			recs, more, lastT, lastID, err = readOrdered(ctx, conn, scope.WorkspaceID, q, ks, orderTime)
		}
		if err != nil {
			return err
		}
		page.Rows = recs
		page.steps = budget.stepCount()
		if more && len(recs) > 0 {
			issued = cursorBody{
				V: 1, Epoch: epoch, WS: scope.WorkspaceID,
				Sources: append([]string(nil), q.Sources...),
				From:    q.From, To: q.To, Sev: q.MinSeverity,
				Terms: append([]string(nil), q.Terms...),
				Preds: append([]model.Predicate(nil), q.Predicates...),
				Limit: q.Limit, Horizon: ks.horizon, Cutoff: ks.cutoff,
				HasLast: true, LastT: lastT, LastID: lastID,
				Exp: s.now().Add(cursorTTL).UnixMicro(),
			}
		}
		return nil
	})
	if err != nil {
		return Page{}, err
	}
	if issued.V == 1 {
		tok, err := signCursor(key, issued)
		if err != nil {
			return Page{}, ErrStorage
		}
		page.Next = tok
	}
	return page, nil
}

// ReadSince is the bounded ingest-id scan a live reader uses. It is not the
// htmx Live loop. examined is the highest committed id covered by this call.
func (s *Store) ReadSince(ctx context.Context, scope model.Scope, after int64, limit int) ([]model.Record, int64, error) {
	if scope.WorkspaceID == "" || after < 0 || limit < 1 || limit > 200 {
		return nil, 0, ErrStorage
	}
	var recs []model.Record
	var examined int64
	err := s.withReader(ctx, func(ctx context.Context, conn *sql.Conn, budget *progressBudget) error {
		wm, err := scanWatermark(ctx, conn)
		if err != nil {
			return err
		}
		examined = wm
		budget.arm(stepLimit(ctx))
		rows, err := conn.QueryContext(ctx, `SELECT ingest_id, event_id, source_id, event_time, received_at, severity_number, severity_text, message, attributes
			FROM logs
			WHERE workspace_id = ? AND ingest_id > ? AND ingest_id <= ? AND received_at >= ?
			ORDER BY ingest_id ASC LIMIT ?`,
			scope.WorkspaceID, after, wm, retentionCutoff(s.now()), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		got, more, _, lastID, err := collectRows(rows, limit)
		if err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		recs = got
		if lastID > 0 && (len(got) == limit || more) {
			examined = lastID
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return recs, examined, nil
}

// RotateCursorEpoch invalidates every cursor. Restore uses the same hook.
func (s *Store) RotateCursorEpoch(ctx context.Context) error {
	return s.SubmitMaintenance(ctx, 1, func(ctx context.Context) error {
		var v string
		if err := s.writer.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='cursor_epoch'`).Scan(&v); err != nil {
			return err
		}
		next, err := epochNext(v)
		if err != nil {
			return err
		}
		_, err = s.writer.ExecContext(ctx, `UPDATE metadata SET value=? WHERE key='cursor_epoch'`, next)
		return err
	})
}

// ReaderInUse reports reader connections checked out of the pool.
func (s *Store) ReaderInUse() int {
	if s.reader == nil {
		return 0
	}
	return s.reader.Stats().InUse
}

func (s *Store) keysetFor(ctx context.Context, conn *sql.Conn, scope model.Scope, q model.Query, epoch string, key []byte) (keyset, error) {
	if q.Cursor == "" {
		wm, err := scanWatermark(ctx, conn)
		if err != nil {
			return keyset{}, err
		}
		return keyset{horizon: wm, cutoff: retentionCutoff(s.now())}, nil
	}
	body, err := openCursor(key, q.Cursor)
	if err != nil {
		return keyset{}, err
	}
	if body.WS != scope.WorkspaceID {
		return keyset{}, ErrCursorScope
	}
	if body.Epoch != epoch || !s.now().Before(time.UnixMicro(body.Exp).UTC()) {
		return keyset{}, ErrCursorExpired
	}
	if !filtersMatch(q, body) {
		return keyset{}, ErrCursorInvalid
	}
	crossed, err := s.retentionExcludes(ctx, conn, scope.WorkspaceID, body.Sources, body.Horizon, body.Cutoff)
	if err != nil {
		return keyset{}, err
	}
	if crossed {
		return keyset{}, ErrCursorExpired
	}
	return keyset{horizon: body.Horizon, cutoff: body.Cutoff, hasLast: body.HasLast, lastT: body.LastT, lastID: body.LastID}, nil
}

// retentionExcludeSQL forces idx_logs_retention. Without stats, a plain
// filter on workspace_id and ingest_id is planned as idx_logs_ws_ingest and
// walks the workspace.
func retentionExcludeSQL(nSources int) string {
	var b strings.Builder
	b.WriteString(`SELECT 1 FROM logs INDEXED BY ` + IndexLogsRetention + ` WHERE received_at >= ? AND received_at < ? AND workspace_id = ? AND ingest_id <= ?`)
	if nSources > 0 {
		b.WriteString(` AND source_id IN (`)
		for i := 0; i < nSources; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('?')
		}
		b.WriteByte(')')
	}
	b.WriteString(` LIMIT 1`)
	return b.String()
}

// retentionExcludes reports whether any row still inside the cursor snapshot
// would disappear under the cutoff at s.now().
func (s *Store) retentionExcludes(ctx context.Context, conn *sql.Conn, workspace string, sources []string, horizon, frozen int64) (bool, error) {
	current := retentionCutoff(s.now())
	if current <= frozen {
		return false, nil
	}
	args := []any{frozen, current, workspace, horizon}
	for _, src := range sources {
		args = append(args, src)
	}
	var one int
	err := conn.QueryRowContext(ctx, retentionExcludeSQL(len(sources)), args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) withReader(ctx context.Context, fn func(context.Context, *sql.Conn, *progressBudget) error) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	conn, err := s.reader.Conn(ctx)
	if err != nil {
		return mapReadErr(ctx, err)
	}
	defer conn.Close()
	var budget *progressBudget
	if err := conn.Raw(func(dc any) error {
		sc, ok := dc.(*sqlite3.SQLiteConn)
		if !ok {
			return ErrStorage
		}
		budget = budgetFor(sc)
		if budget == nil {
			return ErrStorage
		}
		budget.arm(stepLimit(ctx))
		return nil
	}); err != nil {
		return ErrStorage
	}
	defer budget.disarm()
	err = fn(ctx, conn, budget)
	if budget.tripped() {
		return &BudgetError{Steps: budget.stepCount()}
	}
	return mapReadErr(ctx, err)
}

func stepLimit(ctx context.Context) int64 {
	if v, ok := ctx.Value(budgetCtxKey{}).(int64); ok && v > 0 {
		return v
	}
	return vmStepBudget
}

func mapReadErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrCursorExpired) || errors.Is(err, ErrCursorInvalid) || errors.Is(err, ErrCursorScope) ||
		errors.Is(err, ErrTooBroad) || errors.Is(err, ErrStorage) || errors.Is(err, model.ErrValidation) || errors.Is(err, model.ErrUnsupportedPath) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrQueryTimeout
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	var se sqlite3.Error
	if errors.As(err, &se) && se.Code == sqlite3.ErrInterrupt {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ErrQueryTimeout
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return ErrStorage
}

func loadCursorMeta(ctx context.Context, conn *sql.Conn) (string, string, error) {
	var mac, epoch string
	if err := conn.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='cursor_mac'`).Scan(&mac); err != nil {
		return "", "", err
	}
	if err := conn.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='cursor_epoch'`).Scan(&epoch); err != nil {
		return "", "", err
	}
	if mac == "" || epoch == "" {
		return "", "", ErrStorage
	}
	return mac, epoch, nil
}

// FiltersFromCursor reads the filters bound into a signed cursor. Pagination
// posts the cursor without repeating those filters, which keeps the form
// under the 4KiB cap.
func (s *Store) FiltersFromCursor(ctx context.Context, scope model.Scope, token string) (model.Query, error) {
	var q model.Query
	if scope.WorkspaceID == "" || token == "" {
		return q, ErrCursorInvalid
	}
	err := s.withReader(ctx, func(ctx context.Context, conn *sql.Conn, budget *progressBudget) error {
		macHex, _, err := loadCursorMeta(ctx, conn)
		if err != nil {
			return err
		}
		key, err := decodeMAC(macHex)
		if err != nil {
			return err
		}
		body, err := openCursor(key, token)
		if err != nil {
			return err
		}
		if body.WS != scope.WorkspaceID {
			return ErrCursorScope
		}
		preds := make([]model.Predicate, len(body.Preds))
		for i := range body.Preds {
			preds[i] = body.Preds[i]
			preds[i].Path = append([]string(nil), body.Preds[i].Path...)
			if body.Preds[i].Value != nil {
				preds[i].Value = append([]byte(nil), body.Preds[i].Value...)
			}
		}
		q = model.Query{
			Sources:     append([]string(nil), body.Sources...),
			From:        body.From,
			To:          body.To,
			MinSeverity: body.Sev,
			Terms:       append([]string(nil), body.Terms...),
			Predicates:  preds,
			Limit:       body.Limit,
			Cursor:      token,
		}
		return nil
	})
	return q, err
}

// HasRetainedLogs reports whether the workspace has a row at or after the
// live receipt cutoff. It is a LIMIT 1 probe, not a count.
func (s *Store) HasRetainedLogs(ctx context.Context, workspace string) (bool, error) {
	if workspace == "" {
		return false, ErrStorage
	}
	var one int
	err := s.withReader(ctx, func(ctx context.Context, conn *sql.Conn, budget *progressBudget) error {
		err := conn.QueryRowContext(ctx, `SELECT 1 FROM logs INDEXED BY `+IndexLogsRetention+` WHERE received_at >= ? AND workspace_id = ? LIMIT 1`,
			retentionCutoff(s.now()), workspace).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			one = 0
			return nil
		}
		return err
	})
	if err != nil {
		return false, err
	}
	return one == 1, nil
}

func scanWatermark(ctx context.Context, conn *sql.Conn) (int64, error) {
	var v string
	if err := conn.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='watermark'`).Scan(&v); err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id < 0 {
		return 0, ErrStorage
	}
	return id, nil
}

type joinOrder int

const (
	orderTime joinOrder = iota
	orderFTS
)

func readOrdered(ctx context.Context, conn *sql.Conn, workspace string, q model.Query, ks keyset, order joinOrder) ([]model.Record, bool, int64, int64, error) {
	sqlText, args, err := buildSearchSQL(q, ks, order)
	if err != nil {
		return nil, false, 0, 0, err
	}
	if strings.Contains(strings.ToUpper(sqlText), "OFFSET") {
		return nil, false, 0, 0, ErrStorage
	}
	rows, err := conn.QueryContext(ctx, sqlText, append([]any{workspace}, args...)...)
	if err != nil {
		return nil, false, 0, 0, err
	}
	recs, more, lastT, lastID, err := collectRows(rows, q.Limit)
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, false, 0, 0, err
	}
	return recs, more, lastT, lastID, nil
}

func buildSearchSQL(q model.Query, ks keyset, order joinOrder) (string, []any, error) {
	from, err := time.Parse(time.RFC3339Nano, q.From)
	if err != nil {
		return "", nil, ErrStorage
	}
	to, err := time.Parse(time.RFC3339Nano, q.To)
	if err != nil {
		return "", nil, ErrStorage
	}
	var b strings.Builder
	b.WriteString(`SELECT l.ingest_id, l.event_id, l.source_id, l.event_time, l.received_at, l.severity_number, l.severity_text, l.message, l.attributes FROM `)
	switch {
	case len(q.Terms) > 0 && order == orderFTS:
		// CROSS JOIN keeps logs_fts as the outer table. A plain JOIN lets
		// SQLite walk the time window and probe FTS once per row.
		b.WriteString(`logs_fts CROSS JOIN logs l ON logs_fts.rowid = l.ingest_id`)
	case len(q.Terms) > 0:
		b.WriteString(`logs l CROSS JOIN logs_fts ON logs_fts.rowid = l.ingest_id`)
	default:
		b.WriteString(`logs l`)
	}
	b.WriteString(` WHERE l.workspace_id = ?`)
	args := make([]any, 0, 16)
	if len(q.Sources) > 0 {
		b.WriteString(` AND l.source_id IN (`)
		for i, src := range q.Sources {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('?')
			args = append(args, src)
		}
		b.WriteByte(')')
	}
	if len(q.Terms) > 0 {
		b.WriteString(` AND logs_fts MATCH ?`)
		args = append(args, ftsMatch(q.Terms))
	}
	b.WriteString(` AND l.event_time >= ? AND l.event_time < ? AND l.received_at >= ? AND l.severity_number >= ? AND l.ingest_id <= ?`)
	args = append(args, from.UTC().UnixMicro(), to.UTC().UnixMicro(), ks.cutoff, q.MinSeverity, ks.horizon)
	if ks.hasLast {
		b.WriteString(` AND (l.event_time < ? OR (l.event_time = ? AND l.ingest_id < ?))`)
		args = append(args, ks.lastT, ks.lastT, ks.lastID)
	}
	for _, p := range q.Predicates {
		if err := appendPredicate(&b, &args, p); err != nil {
			return "", nil, err
		}
	}
	b.WriteString(` ORDER BY l.event_time DESC, l.ingest_id DESC LIMIT ?`)
	args = append(args, q.Limit+1)
	return b.String(), args, nil
}

func ftsMatch(terms []string) string {
	parts := make([]string, len(terms))
	for i, t := range terms {
		parts[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	return strings.Join(parts, " AND ")
}

func appendPredicate(b *strings.Builder, args *[]any, p model.Predicate) error {
	path, err := model.JSONPath(p.Path)
	if err != nil {
		return err
	}
	switch p.Op {
	case "missing":
		b.WriteString(` AND json_type(l.attributes, ?) IS NULL`)
		*args = append(*args, path)
		return nil
	case "eq":
		switch p.Type {
		case "null":
			b.WriteString(` AND json_type(l.attributes, ?) = 'null'`)
			*args = append(*args, path)
		case "boolean":
			want := "false"
			if bytesTrim(p.Value) == "true" {
				want = "true"
			}
			b.WriteString(` AND json_type(l.attributes, ?) = ?`)
			*args = append(*args, path, want)
		case "integer":
			n, err := strconv.ParseInt(bytesTrim(p.Value), 10, 64)
			if err != nil {
				return ErrStorage
			}
			b.WriteString(` AND json_type(l.attributes, ?) = 'integer' AND json_extract(l.attributes, ?) = ?`)
			*args = append(*args, path, path, n)
		case "real":
			f, err := strconv.ParseFloat(bytesTrim(p.Value), 64)
			if err != nil {
				return ErrStorage
			}
			b.WriteString(` AND json_type(l.attributes, ?) = 'real' AND json_extract(l.attributes, ?) = ?`)
			*args = append(*args, path, path, f)
		case "string":
			var s string
			if err := jsonUnmarshal(p.Value, &s); err != nil {
				return ErrStorage
			}
			b.WriteString(` AND json_type(l.attributes, ?) = 'text' AND json_extract(l.attributes, ?) = ?`)
			*args = append(*args, path, path, s)
		default:
			return ErrStorage
		}
		return nil
	default:
		return ErrStorage
	}
}

func bytesTrim(b []byte) string {
	return strings.TrimSpace(string(b))
}

func jsonUnmarshal(b []byte, dest any) error {
	return json.Unmarshal(b, dest)
}

func collectRows(rows *sql.Rows, limit int) ([]model.Record, bool, int64, int64, error) {
	var recs []model.Record
	var total int
	var lastT, lastID int64
	more := false
	for rows.Next() {
		rec, eventTime, ingestID, n, err := scanRecord(rows)
		if err != nil {
			return nil, false, 0, 0, err
		}
		if len(recs) == limit || (len(recs) > 0 && total+n > model.MaxResultBytes) {
			more = true
			break
		}
		if total+n > model.MaxResultBytes {
			return nil, false, 0, 0, ErrTooBroad
		}
		total += n
		recs = append(recs, rec)
		lastT = eventTime
		lastID = ingestID
	}
	if err := rows.Err(); err != nil {
		return nil, false, 0, 0, err
	}
	return recs, more, lastT, lastID, nil
}

func scanRecord(rows *sql.Rows) (model.Record, int64, int64, int, error) {
	var r model.Record
	var ingestID, eventTime, receivedAt int64
	var attrs []byte
	if err := rows.Scan(&ingestID, &r.EventID, &r.SourceID, &eventTime, &receivedAt, &r.SeverityNumber, &r.SeverityText, &r.Message, &attrs); err != nil {
		return r, 0, 0, 0, err
	}
	r.IngestID = strconv.FormatInt(ingestID, 10)
	r.EventTime = time.UnixMicro(eventTime).UTC().Format(time.RFC3339Nano)
	r.ReceivedAt = time.UnixMicro(receivedAt).UTC().Format(time.RFC3339Nano)
	r.Attributes = append([]byte(nil), attrs...)
	return r, eventTime, ingestID, rowBytes(r), nil
}

func rowBytes(r model.Record) int {
	return len(r.EventID) + len(r.IngestID) + len(r.SourceID) + len(r.EventTime) +
		len(r.ReceivedAt) + len(r.SeverityText) + len(r.Message) + len(r.Attributes) + 1024
}

func explainSearch(ctx context.Context, s *Store, scope model.Scope, q model.Query) (string, error) {
	if err := model.ValidateQuery(q); err != nil {
		return "", err
	}
	wm, err := s.Watermark(ctx)
	if err != nil {
		return "", err
	}
	order := orderTime
	if len(q.Terms) > 0 {
		order = orderFTS
	}
	sqlText, args, err := buildSearchSQL(q, keyset{horizon: wm, cutoff: retentionCutoff(s.now())}, order)
	if err != nil {
		return "", err
	}
	var lines []string
	err = s.withReader(ctx, func(ctx context.Context, conn *sql.Conn, budget *progressBudget) error {
		rows, err := conn.QueryContext(ctx, "EXPLAIN QUERY PLAN "+sqlText, append([]any{scope.WorkspaceID}, args...)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				return err
			}
			lines = append(lines, detail)
		}
		return rows.Err()
	})
	return strings.Join(lines, "\n"), err
}
