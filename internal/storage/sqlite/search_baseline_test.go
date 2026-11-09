package sqlite

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func TestBaselineSearch(t *testing.T) {
	store, _ := freshStore(t)
	frozen := time.Date(2026, 4, 1, 12, 0, 30, 0, time.UTC)
	store.clock.Store(func() time.Time { return frozen })
	ctx := context.Background()
	const n = 50000
	seedStart := time.Now()
	if err := store.SubmitMaintenance(ctx, 1, func(ctx context.Context) error {
		if _, err := store.writer.ExecContext(ctx, `INSERT INTO sources(workspace_id, source_id, service, environment)
			VALUES ('ws_demo_a','src_demo_cron','demo-cron','staging'),
			       ('ws_demo_a','src_demo_batch','demo-batch','staging')`); err != nil {
			return err
		}
		tx, err := store.writer.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO logs (workspace_id, source_id, batch_id, event_id, event_time, received_at, severity_number, severity_text, message, attributes)
			VALUES ('ws_demo_a', ?, ?, ?, ?, ?, 9, 'INFO', ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		sources := []string{"src_demo_api", "src_demo_cron", "src_demo_batch"}
		pad := strings.Repeat("x ", 400)
		base := frozen.Add(-6 * 24 * time.Hour)
		span := 6 * 24 * time.Hour
		received := frozen.UnixMicro()
		var last int64
		step := span / time.Duration(n)
		for i := 0; i < n; i++ {
			ev := base.Add(step * time.Duration(i))
			msg := fmt.Sprintf("synthetic baseline common %s rare-%d", pad, i%1000)
			if i >= n-400 {
				ev = frozen.Add(-5 * time.Minute)
				msg = "synthetic baseline representative common " + pad
			}
			attrs := fmt.Sprintf(`{"task":{"attempt":%d,"kind":"alpha"},"n":%d}`, i%5, i)
			res, err := stmt.ExecContext(ctx, sources[i%3], fmt.Sprintf("seed-%d", i), fmt.Sprintf("%08x-0000-4000-8000-%012d", i+1, i+1), ev.UnixMicro(), received, msg, attrs)
			if err != nil {
				return err
			}
			last, _ = res.LastInsertId()
		}
		if _, err := tx.ExecContext(ctx, `UPDATE metadata SET value=? WHERE key='watermark'`, fmt.Sprintf("%d", last)); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		_, err = store.writer.ExecContext(ctx, `INSERT INTO logs_fts(logs_fts, rank) VALUES('integrity-check', 1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("seed_n=%d seed_elapsed=%s", n, time.Since(seedStart))

	var logs int
	if err := store.reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM logs`).Scan(&logs); err != nil || logs != n {
		t.Fatalf("logs %d err %v", logs, err)
	}

	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}
	window := model.Query{
		Sources: []string{"src_demo_api"}, From: frozen.Add(-15 * time.Minute).Format(time.RFC3339Nano),
		To: frozen.Add(time.Second).Format(time.RFC3339Nano), MinSeverity: 9, Limit: 200,
	}
	plan, err := explainSearch(ctx, store, scope, window)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("explain_source_time=%s", plan)
	if !strings.Contains(plan, "idx_logs_time") && !strings.Contains(plan, "idx_logs_ws_time") {
		t.Fatalf("time plan: %s", plan)
	}
	termQ := window
	termQ.Terms = []string{"representative"}
	plan, err = explainSearch(ctx, store, scope, termQ)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("explain_term=%s", plan)
	if !strings.Contains(plan, "logs_fts") {
		t.Fatalf("fts plan: %s", plan)
	}

	var pending []model.Batch
	for i := 0; i < 20; i++ {
		pending = append(pending, eventBatch(t, 900000+i, frozen.Add(-2*time.Second), "live ingest synthetic", `{"task":{"attempt":1}}`))
	}
	measureCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var ingestN atomic.Int64
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		for i < len(pending) {
			select {
			case <-measureCtx.Done():
				return
			case <-ticker.C:
				if _, err := store.Commit(context.Background(), scope, pending[i]); err == nil {
					ingestN.Add(1)
				}
				i++
			}
		}
	}()
	var liveN atomic.Int64
	var liveRows atomic.Int64
	go func() {
		var after int64
		for measureCtx.Err() == nil {
			rows, examined, err := store.ReadSince(context.Background(), scope, after, 200)
			if err != nil {
				return
			}
			liveN.Add(1)
			liveRows.Add(int64(len(rows)))
			if examined > after {
				after = examined
			}
			if len(rows) == 0 {
				time.Sleep(15 * time.Millisecond)
			}
		}
	}()

	attrQ := window
	attrQ.Predicates = []model.Predicate{{Path: []string{"task", "attempt"}, Op: "eq", Type: "integer", Value: []byte("1")}}
	queries := []model.Query{window, termQ, attrQ}
	var lat []time.Duration
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) && measureCtx.Err() == nil {
		q := queries[len(lat)%len(queries)]
		start := time.Now()
		page, err := store.ReadPage(context.Background(), scope, q)
		d := time.Since(start)
		if err != nil {
			t.Fatalf("representative: %v", err)
		}
		if len(page.Rows) == 0 {
			t.Fatalf("representative query %d returned no rows", len(lat)%len(queries))
		}
		lat = append(lat, d)
		time.Sleep(80 * time.Millisecond)
	}
	cancel()
	if len(lat) < 20 {
		t.Fatalf("samples %d", len(lat))
	}
	got := p95(lat)
	t.Logf("samples=%d p95=%s max=%s ingest=%d live_reads=%d live_rows=%d", len(lat), got, maxDur(lat), ingestN.Load(), liveN.Load(), liveRows.Load())
	if got >= time.Second {
		t.Fatalf("p95 %s", got)
	}
	if ingestN.Load() < 10 || liveN.Load() < 1 {
		t.Fatalf("concurrency ingest=%d live=%d", ingestN.Load(), liveN.Load())
	}

	broad := model.Query{
		From: frozen.Add(-6 * 24 * time.Hour).Format(time.RFC3339Nano), To: frozen.Format(time.RFC3339Nano),
		MinSeverity: 1, Terms: []string{"common"}, Limit: 200,
		Predicates: []model.Predicate{{Path: []string{"task", "attempt"}, Op: "eq", Type: "integer", Value: []byte("999")}},
	}
	start := time.Now()
	_, err = store.ReadPage(context.Background(), scope, broad)
	elapsed := time.Since(start)
	var budget *BudgetError
	if !errors.As(err, &budget) || elapsed > 5*time.Second || budget.Steps < vmStepBudget {
		t.Fatalf("pathological err=%v elapsed=%s", err, elapsed)
	}
	t.Logf("pathological_steps=%d elapsed=%s", budget.Steps, elapsed)
}

func p95(ds []time.Duration) time.Duration {
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := int(math.Ceil(0.95*float64(len(s)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return s[idx]
}

func maxDur(ds []time.Duration) time.Duration {
	var m time.Duration
	for _, d := range ds {
		if d > m {
			m = d
		}
	}
	return m
}
