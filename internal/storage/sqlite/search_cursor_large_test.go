package sqlite

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

// TestCursorPageAfterMinuteOnLargeWorkspace is the no-stats plan for the
// retention EXISTS. A cursor with no source filter must still page after the
// cutoff minute moves.
func TestCursorPageAfterMinuteOnLargeWorkspace(t *testing.T) {
	store, _ := freshStore(t)
	frozen := time.Date(2026, 4, 1, 12, 0, 10, 0, time.UTC)
	store.clock.Store(func() time.Time { return frozen })
	ctx := context.Background()
	const n = 50000
	if err := store.SubmitMaintenance(ctx, 1, func(ctx context.Context) error {
		tx, err := store.writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO logs (workspace_id, source_id, batch_id, event_id, event_time, received_at, severity_number, severity_text, message, attributes)
			VALUES ('ws_demo_a', 'src_demo_api', ?, ?, ?, ?, 9, 'INFO', 'page common', '{"k":1}')`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		when := frozen.Add(-time.Minute).UnixMicro()
		received := frozen.UnixMicro()
		var last int64
		for i := 0; i < n; i++ {
			res, err := stmt.ExecContext(ctx, fmt.Sprintf("page-%d", i), fmt.Sprintf("%08x-0000-4000-8000-%012d", i+1, i+1), when, received)
			if err != nil {
				return err
			}
			last, _ = res.LastInsertId()
		}
		if _, err := tx.ExecContext(ctx, `UPDATE metadata SET value=? WHERE key='watermark'`, fmt.Sprintf("%d", last)); err != nil {
			return err
		}
		return tx.Commit()
	}); err != nil {
		t.Fatal(err)
	}
	var stats int
	if err := store.reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name='sqlite_stat1'`).Scan(&stats); err != nil || stats != 0 {
		t.Fatalf("sqlite_stat1=%d err=%v", stats, err)
	}

	explainSQL := "EXPLAIN QUERY PLAN " + retentionExcludeSQL(0)
	rows, err := store.reader.QueryContext(ctx, explainSQL, int64(0), int64(1), "ws_demo_a", int64(n))
	if err != nil {
		t.Fatal(err)
	}
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	joined := strings.Join(plan, "\n")
	t.Logf("explain_retention_exists=%s", joined)
	if !strings.Contains(joined, IndexLogsRetention) || strings.Contains(joined, "idx_logs_ws_ingest") {
		t.Fatalf("retention plan: %s", joined)
	}

	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}
	base := model.Query{
		From: frozen.Add(-24 * time.Hour).Format(time.RFC3339Nano),
		To:   frozen.Add(time.Second).Format(time.RFC3339Nano), MinSeverity: 1, Limit: 50,
	}
	cases := []model.Query{base, func() model.Query { q := base; q.Terms = []string{"common"}; return q }()}
	names := []string{"no term", "term"}
	for i, q := range cases {
		store.clock.Store(func() time.Time { return frozen })
		first, err := store.ReadPage(ctx, scope, q)
		if err != nil || first.Next == "" || len(first.Rows) == 0 {
			t.Fatalf("%s first: %v rows=%d next=%t", names[i], err, len(first.Rows), first.Next != "")
		}
		next := q
		next.Cursor = first.Next
		for _, d := range []time.Duration{61 * time.Second, 14 * time.Minute} {
			store.clock.Store(func() time.Time { return frozen.Add(d) })
			page, err := store.ReadPage(ctx, scope, next)
			if err != nil || len(page.Rows) == 0 {
				t.Fatalf("%s +%s: %v rows=%d", names[i], d, err, len(page.Rows))
			}
			t.Logf("%s +%s rows=%d steps=%d", names[i], d, len(page.Rows), page.steps)
		}
	}
}
