package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

// TestDenseWindowSelectiveTerm is the shipped posture: Open, no ANALYZE,
// 50k rows at 5/s. A one-row term in the default 15m window must return that
// row. A common term must finish within 5s, as a page or a budget error.
func TestDenseWindowSelectiveTerm(t *testing.T) {
	store, _ := freshStore(t)
	frozen := time.Date(2026, 5, 2, 12, 0, 30, 0, time.UTC)
	store.clock.Store(func() time.Time { return frozen })
	ctx := context.Background()
	const n = 50000
	const perSecond = 5
	step := time.Second / perSecond
	base := frozen.Add(-time.Duration(n) * step)
	seedStart := time.Now()
	if err := store.SubmitMaintenance(ctx, 1, func(ctx context.Context) error {
		tx, err := store.writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO logs (workspace_id, source_id, batch_id, event_id, event_time, received_at, severity_number, severity_text, message, attributes)
			VALUES ('ws_demo_a', 'src_demo_api', ?, ?, ?, ?, 9, 'INFO', ?, '{"k":1}')`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		received := frozen.UnixMicro()
		var last int64
		for i := 0; i < n; i++ {
			msg := "dense common"
			if i == n-1 {
				msg = "dense needle"
			}
			res, err := stmt.ExecContext(ctx, fmt.Sprintf("dense-%d", i), fmt.Sprintf("%08x-0000-4000-8000-%012d", i+1, i+1), base.Add(step*time.Duration(i)).UnixMicro(), received, msg)
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
	t.Logf("dense_seed_n=%d dense_seed_elapsed=%s", n, time.Since(seedStart))

	var stats int
	if err := store.reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name='sqlite_stat1'`).Scan(&stats); err != nil || stats != 0 {
		t.Fatalf("sqlite_stat1=%d err=%v", stats, err)
	}

	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}
	needle := model.Query{
		Sources: []string{"src_demo_api"}, From: frozen.Add(-15 * time.Minute).Format(time.RFC3339Nano),
		To: frozen.Add(time.Second).Format(time.RFC3339Nano), MinSeverity: 1, Terms: []string{"needle"}, Limit: 200,
	}
	plan, err := explainSearch(ctx, store, scope, needle)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("explain_needle=%s", plan)
	first := plan
	if i := strings.IndexByte(plan, '\n'); i >= 0 {
		first = plan[:i]
	}
	if !strings.Contains(first, "logs_fts") {
		t.Fatalf("needle plan: %s", plan)
	}
	start := time.Now()
	page, err := store.ReadPage(ctx, scope, needle)
	needleElapsed := time.Since(start)
	if err != nil || len(page.Rows) != 1 || !strings.Contains(page.Rows[0].Message, "needle") {
		t.Fatalf("needle: %v rows=%d elapsed=%s", err, len(page.Rows), needleElapsed)
	}
	t.Logf("needle_rows=1 needle_elapsed=%s needle_steps=%d", needleElapsed, page.steps)

	common := needle
	common.Terms = []string{"common"}
	start = time.Now()
	page, err = store.ReadPage(ctx, scope, common)
	commonElapsed := time.Since(start)
	if commonElapsed > 5*time.Second {
		t.Fatalf("common elapsed %s", commonElapsed)
	}
	var budget *BudgetError
	switch {
	case errors.As(err, &budget):
		t.Logf("common_budget_steps=%d common_elapsed=%s", budget.Steps, commonElapsed)
	case err != nil:
		t.Fatalf("common: %v", err)
	case len(page.Rows) == 0:
		t.Fatal("common returned zero rows")
	default:
		t.Logf("common_rows=%d common_elapsed=%s common_steps=%d", len(page.Rows), commonElapsed, page.steps)
	}
}
