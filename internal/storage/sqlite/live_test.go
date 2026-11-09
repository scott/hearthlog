package sqlite

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func TestLiveQueryIsWorkspaceScoped(t *testing.T) {
	q, _ := liveSpanQuery(liveRowCols, "ws_demo_a", LiveCursor{ReceivedAt: 10, IngestID: 4}, 9, 1, 5)
	count, _ := liveSpanQuery(`received_at, ingest_id`, "ws_demo_a", LiveCursor{ReceivedAt: 10, IngestID: 4}, 9, 1, 5)
	for _, sqlText := range []string{q, count, liveTailQuery} {
		if !strings.Contains(sqlText, "workspace_id = ?") {
			t.Fatalf("query missing workspace scope: %s", sqlText)
		}
		if strings.Contains(strings.ToUpper(sqlText), "OFFSET") {
			t.Fatalf("query uses OFFSET: %s", sqlText)
		}
		if !strings.Contains(sqlText, IndexLogsRetention) {
			t.Fatalf("query does not seek %s: %s", IndexLogsRetention, sqlText)
		}
		if strings.Contains(sqlText, "idx_logs_ws_ingest") {
			t.Fatalf("query walks idx_logs_ws_ingest: %s", sqlText)
		}
	}
	if !strings.Contains(q, "received_at > ?") || !strings.Contains(q, "ingest_id > ?") {
		t.Fatal("scan is not the (received_at, ingest_id) keyset")
	}
	if !strings.Contains(q, "ingest_id <= ?") {
		t.Fatal("scan is not capped at the watermark")
	}
}

func TestLivePollEqualReceivedAt(t *testing.T) {
	store, _ := newStore(t)
	when := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return when })
	ctx := context.Background()
	want := map[string]struct{}{}
	for i, n := range []int{200, 50} {
		b := mustNative(t, fmt.Sprintf("%08x-0000-4000-8000-%012x", i+1, 1), n, "synthetic tie", when, 100+i)
		for _, ev := range b.Events {
			want[ev.EventID] = struct{}{}
		}
		if _, err := store.CommitNative(ctx, demoScope, b, 1); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]int{}
	var recv string
	cur := LiveCursor{}
	for pageN := 0; pageN < 6; pageN++ {
		page, err := store.Poll(ctx, demoScope, cur, LiveFilter{}, false)
		if err != nil {
			t.Fatal(err)
		}
		if page.Gap {
			t.Fatal("gap on a short tie page")
		}
		for _, row := range page.Rows {
			got[row.EventID]++
			if recv == "" {
				recv = row.ReceivedAt
			} else if row.ReceivedAt != recv {
				t.Fatalf("received_at changed %s %s", recv, row.ReceivedAt)
			}
		}
		cur = page.Cursor
		if page.CaughtUp {
			break
		}
	}
	if len(got) != len(want) {
		t.Fatalf("rows %d want %d", len(got), len(want))
	}
	for id := range want {
		if got[id] != 1 {
			t.Fatalf("id %s count %d", id, got[id])
		}
	}
	if cur.ReceivedAt == 0 || cur.IngestID == 0 {
		t.Fatalf("cursor %+v", cur)
	}
}

func TestLivePollConcurrentNoLoss(t *testing.T) {
	store, _ := newStore(t)
	when := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return when })
	ctx := context.Background()
	other := mustNative(t, "00aa0000-0000-4000-8000-0000000000aa", 1, "synthetic other workspace", when, 7)
	if _, err := store.CommitNative(ctx, otherScope, other, 1); err != nil {
		t.Fatal(err)
	}

	const workers = 4
	const batches = 4
	const size = 40
	want := map[string]struct{}{}
	var built []model.Batch
	for w := 0; w < workers; w++ {
		for b := 0; b < batches; b++ {
			tag := 1000 + w*100 + b
			batch := mustNative(t, fmt.Sprintf("%08x-0000-4000-8000-%012x", tag, 1), size, "synthetic live race", when, tag)
			for _, ev := range batch.Events {
				if _, ok := want[ev.EventID]; ok {
					t.Fatalf("duplicate event id %s", ev.EventID)
				}
				want[ev.EventID] = struct{}{}
			}
			built = append(built, batch)
		}
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(built))
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for b := 0; b < batches; b++ {
				batch := built[w*batches+b]
				if _, err := store.CommitNative(ctx, demoScope, batch, 1); err != nil {
					errCh <- err
					return
				}
			}
		}(w)
	}

	seen := map[string]int{}
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		cur := LiveCursor{}
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			page, err := store.Poll(ctx, demoScope, cur, LiveFilter{}, false)
			if err != nil {
				errCh <- err
				return
			}
			if page.Gap {
				errCh <- errors.New("gap during concurrent poll")
				return
			}
			mu.Lock()
			for _, row := range page.Rows {
				seen[row.EventID]++
			}
			n := len(seen)
			mu.Unlock()
			cur = page.Cursor
			if page.CaughtUp && n >= len(want) {
				return
			}
			if page.CaughtUp {
				time.Sleep(time.Millisecond)
			}
		}
	}()
	wg.Wait()
	<-done
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != len(want) {
		t.Fatalf("polled %d inserted %d", len(seen), len(want))
	}
	for id := range want {
		if seen[id] != 1 {
			t.Fatalf("id %s count %d", id, seen[id])
		}
	}
	if _, ok := seen[other.Events[0].EventID]; ok {
		t.Fatal("other workspace row was polled")
	}
}

func TestLivePollSparseFilter(t *testing.T) {
	store, _ := newStore(t)
	when := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return when })
	ctx := context.Background()
	for i := 0; i < 25; i++ {
		b := mustNative(t, fmt.Sprintf("%08x-0000-4000-8000-%012x", 3000+i, 1), 200, "synthetic sparse miss", when, 3000+i)
		if _, err := store.CommitNative(ctx, demoScope, b, 1); err != nil {
			t.Fatal(err)
		}
	}
	hit := mustNative(t, "00bb0000-0000-4000-8000-0000000000bb", 1, "synthetic sparse hit", when, 3999)
	hit.Events[0].SeverityNumber = 17
	hit.Events[0].SeverityText = "ERROR"
	if _, err := store.CommitNative(ctx, demoScope, hit, 1); err != nil {
		t.Fatal(err)
	}
	filter := LiveFilter{MinSeverity: 17}
	cur := LiveCursor{}
	var found string
	for pageN := 0; pageN < 40; pageN++ {
		page, err := store.Poll(ctx, demoScope, cur, filter, false)
		if err != nil {
			t.Fatal(err)
		}
		if page.Gap {
			t.Fatalf("page %d gap cursor=%+v", pageN, page.Cursor)
		}
		if pageN == 0 && (len(page.Rows) != 0 || page.Cursor.IngestID == 0) {
			t.Fatalf("first sparse page rows=%d cursor=%+v", len(page.Rows), page.Cursor)
		}
		if len(page.Rows) > 0 {
			if len(page.Rows) != 1 {
				t.Fatalf("rows %+v", page.Rows)
			}
			found = page.Rows[0].EventID
			break
		}
		if page.Cursor == cur {
			t.Fatalf("cursor stuck %+v", cur)
		}
		cur = page.Cursor
	}
	if found != hit.Events[0].EventID {
		t.Fatalf("found %s", found)
	}
}

func TestLivePollCatchupGap(t *testing.T) {
	store, _ := newStore(t)
	when := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return when })
	ctx := context.Background()
	first := mustNative(t, "00cc0000-0000-4000-8000-0000000000cc", 1, "synthetic tail", when, 41)
	if _, err := store.CommitNative(ctx, demoScope, first, 1); err != nil {
		t.Fatal(err)
	}
	tail, wm, err := store.LiveTail(ctx, demoScope)
	if err != nil {
		t.Fatal(err)
	}
	if wm == 0 || tail.IngestID != wm {
		t.Fatalf("tail %+v wm %d", tail, wm)
	}
	store.SetClock(func() time.Time { return when.Add(31 * time.Second) })
	later := mustNative(t, "00cd0000-0000-4000-8000-0000000000cd", 1, "synthetic later", when.Add(31*time.Second), 42)
	if _, err := store.CommitNative(ctx, demoScope, later, 1); err != nil {
		t.Fatal(err)
	}
	gap, err := store.Poll(ctx, demoScope, tail, LiveFilter{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !gap.Gap || len(gap.Rows) != 0 || gap.Cursor != tail {
		t.Fatalf("30s resume %+v", gap)
	}
	near, err := store.Poll(ctx, demoScope, tail, LiveFilter{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if near.Gap || len(near.Rows) != 1 {
		t.Fatalf("steady poll should still return the row: %+v", near.Rows)
	}

	store.SetClock(func() time.Time { return when })
	cur := tail
	for i := 0; i < 26; i++ {
		n := 200
		if i == 25 {
			n = 1
		}
		b := mustNative(t, fmt.Sprintf("%08x-0000-4000-8000-%012x", 5000+i, 1), n, "synthetic backlog", when, 5000+i)
		if _, err := store.CommitNative(ctx, demoScope, b, 1); err != nil {
			t.Fatal(err)
		}
	}
	// The 31s row is already ahead of tail. Count includes it plus 5001.
	over, err := store.Poll(ctx, demoScope, cur, LiveFilter{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !over.Gap {
		t.Fatalf("5001 resume was not a gap, rows=%d", len(over.Rows))
	}
}

func TestLivePollBudgetGap(t *testing.T) {
	store, _ := newStore(t)
	when := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return when })
	ctx := context.WithValue(context.Background(), budgetCtxKey{}, int64(1000))
	for i := 0; i < 8; i++ {
		b := mustNative(t, fmt.Sprintf("%08x-0000-4000-8000-%012x", 7000+i, 1), 200, "synthetic budget", when, 7000+i)
		if _, err := store.CommitNative(context.Background(), demoScope, b, 1); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.Poll(ctx, demoScope, LiveCursor{}, LiveFilter{MinSeverity: 17}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !page.Gap || len(page.Rows) != 0 {
		t.Fatalf("budget exhaustion rows=%d gap=%v", len(page.Rows), page.Gap)
	}
}

func TestLivePollRollbackRestartAndEmpty(t *testing.T) {
	store, path := newStore(t)
	when := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return when })
	ctx := context.Background()
	b := mustNative(t, "00de0000-0000-4000-8000-0000000000de", 2, "synthetic kept", when, 80)
	if _, err := store.CommitNative(ctx, demoScope, b, 1); err != nil {
		t.Fatal(err)
	}
	wm, err := store.Watermark(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.writer.ExecContext(ctx, `CREATE TRIGGER abort_live BEFORE UPDATE ON metadata BEGIN SELECT RAISE(ABORT, 'forced'); END`)
	if err != nil {
		t.Fatal(err)
	}
	rejected := mustNative(t, "00df0000-0000-4000-8000-0000000000df", 1, "synthetic rejected", when, 81)
	if _, err := store.CommitNative(ctx, demoScope, rejected, 1); err == nil {
		t.Fatal("expected rollback")
	}
	if got, err := store.Watermark(ctx); err != nil || got != wm {
		t.Fatalf("watermark %d want %d err %v", got, wm, err)
	}
	page, err := store.Poll(ctx, demoScope, LiveCursor{}, LiveFilter{}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range page.Rows {
		if row.Message == "synthetic rejected" {
			t.Fatal("rolled-back row was visible")
		}
	}
	if _, err := store.writer.ExecContext(ctx, `DROP TRIGGER abort_live`); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	holdErr := make(chan error, 1)
	go func() {
		holdErr <- store.SubmitMaintenance(ctx, 1, func(ctx context.Context) error {
			tx, err := store.writer.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			_, err = tx.ExecContext(ctx, `INSERT INTO logs (workspace_id, source_id, batch_id, event_id, event_time, received_at, severity_number, severity_text, message, attributes)
				VALUES ('ws_demo_a', 'src_demo_api', '00e00000-0000-4000-8000-0000000000e0', '00e10000-0000-4000-8000-0000000000e1', ?, ?, 9, 'INFO', 'synthetic uncommitted', '{"k":1}')`,
				when.UnixMicro(), when.UnixMicro())
			if err != nil {
				return err
			}
			close(started)
			<-release
			return nil
		})
	}()
	select {
	case <-started:
	case err := <-holdErr:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not hold the transaction")
	}
	held, err := store.Poll(ctx, demoScope, LiveCursor{}, LiveFilter{}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range held.Rows {
		if strings.Contains(row.Message, "uncommitted") {
			t.Fatal("uncommitted row was visible")
		}
	}
	if got, err := store.Watermark(ctx); err != nil || got != wm {
		t.Fatalf("watermark during delay %d want %d (%v)", got, wm, err)
	}
	close(release)
	if err := <-holdErr; err != nil {
		t.Fatal(err)
	}

	store = reopen(t, path, store)
	store.SetClock(func() time.Time { return when })
	again, err := store.Poll(ctx, demoScope, LiveCursor{}, LiveFilter{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Rows) != 2 {
		t.Fatalf("restart rows %d", len(again.Rows))
	}
	if got, err := store.Watermark(ctx); err != nil || got != wm {
		t.Fatalf("watermark after restart %d want %d (%v)", got, wm, err)
	}
	if err := store.SubmitMaintenance(ctx, 1, func(ctx context.Context) error {
		_, err := store.writer.ExecContext(ctx, `DELETE FROM logs`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Watermark(ctx); err != nil || got != wm {
		t.Fatalf("watermark after delete %d want %d (%v)", got, wm, err)
	}
	empty, err := store.Poll(ctx, demoScope, LiveCursor{ReceivedAt: when.UnixMicro(), IngestID: wm}, LiveFilter{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Rows) != 0 || empty.Gap || !empty.Idle {
		t.Fatalf("empty poll %+v", empty)
	}
	if got, err := store.Watermark(ctx); err != nil || got != wm {
		t.Fatalf("watermark reset %d (%v)", got, err)
	}
}

func TestLivePollTailAfterHistory(t *testing.T) {
	store, _ := newStore(t)
	when := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return when })
	ctx := context.Background()
	for i := 0; i < 25; i++ {
		b := mustNative(t, fmt.Sprintf("%08x-0000-4000-8000-%012x", 9000+i, 1), 200, "synthetic history", when, 9000+i)
		if _, err := store.CommitNative(ctx, demoScope, b, 1); err != nil {
			t.Fatal(err)
		}
	}
	tail, _, err := store.LiveTail(ctx, demoScope)
	if err != nil {
		t.Fatal(err)
	}
	q, args := liveSpanQuery(liveRowCols, demoScope.WorkspaceID, tail, tail.IngestID, retentionCutoff(when), 50)
	rows, err := store.writer.QueryContext(ctx, `EXPLAIN QUERY PLAN `+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var id, parent, n int
		var detail string
		if err := rows.Scan(&id, &parent, &n, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
		plan.WriteByte('\n')
	}
	rows.Close()
	t.Logf("explain_live=%s", plan.String())
	if !strings.Contains(plan.String(), IndexLogsRetention) || strings.Contains(plan.String(), "idx_logs_ws_ingest") {
		t.Fatalf("plan %s", plan.String())
	}
	fresh := mustNative(t, "00ee0000-0000-4000-8000-0000000000ee", 1, "synthetic after history", when, 9900)
	if _, err := store.CommitNative(ctx, demoScope, fresh, 1); err != nil {
		t.Fatal(err)
	}
	page, err := store.Poll(ctx, demoScope, tail, LiveFilter{Sources: []string{demoScope.SourceID}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if page.Gap || len(page.Rows) != 1 || page.Rows[0].EventID != fresh.Events[0].EventID {
		t.Fatalf("tail page gap=%v rows=%d", page.Gap, len(page.Rows))
	}
}

func TestLivePollVisibility(t *testing.T) {
	store, _ := newStore(t)
	when := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	var lats []time.Duration
	cur := LiveCursor{}
	for i := 0; i < 20; i++ {
		at := when.Add(time.Duration(i) * time.Millisecond)
		store.SetClock(func() time.Time { return at })
		b := mustNative(t, fmt.Sprintf("%08x-0000-4000-8000-%012x", 8000+i, 1), 1, "synthetic visible", at, 8000+i)
		start := time.Now()
		if _, err := store.CommitNative(ctx, demoScope, b, 1); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(2 * time.Second)
		seen := false
		for time.Now().Before(deadline) {
			page, err := store.Poll(ctx, demoScope, cur, LiveFilter{}, false)
			if err != nil {
				t.Fatal(err)
			}
			cur = page.Cursor
			for _, row := range page.Rows {
				if row.EventID == b.Events[0].EventID {
					seen = true
				}
			}
			if seen {
				break
			}
		}
		if !seen {
			t.Fatalf("event %d not visible", i)
		}
		lats = append(lats, time.Since(start))
	}
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	p95 := lats[len(lats)*95/100]
	t.Logf("visibility_n=%d p95=%s max=%s", len(lats), p95, lats[len(lats)-1])
	if p95 >= 2*time.Second {
		t.Fatalf("p95 %s", p95)
	}
}
