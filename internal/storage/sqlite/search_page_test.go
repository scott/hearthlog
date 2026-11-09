package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func TestSearchPageKeysetAndCursor(t *testing.T) {
	store, path := freshStore(t)
	frozen := time.Date(2026, 1, 2, 3, 15, 30, 0, time.UTC)
	store.clock.Store(func() time.Time { return frozen })
	ctx := context.Background()
	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}

	when := frozen.Add(-time.Minute)
	for i := 0; i < 5; i++ {
		b := eventBatch(t, i, when, "equal-stamp synthetic", `{"task":{"attempt":1},"flag":true,"n":1,"r":1.0,"s":"1"}`)
		if _, err := store.Commit(ctx, scope, b); err != nil {
			t.Fatal(err)
		}
	}
	// A row the hostile term must not return, and a diacritic row.
	if _, err := store.Commit(ctx, scope, eventBatch(t, 20, when, "Café token", `{"a":null}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(ctx, model.Scope{WorkspaceID: "ws_demo_b", SourceID: "src_demo_worker"}, eventBatch(t, 21, when, "equal-stamp synthetic", `{"task":{"attempt":1}}`)); err != nil {
		t.Fatal(err)
	}

	q := model.Query{
		Sources: []string{"src_demo_api"}, From: frozen.Add(-15 * time.Minute).Format(time.RFC3339Nano),
		To: frozen.Format(time.RFC3339Nano), MinSeverity: 1, Terms: []string{"equal-stamp"}, Limit: 2,
	}
	sqlText, args, err := buildSearchSQL(q, keyset{horizon: 100, cutoff: 0})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToUpper(sqlText), "OFFSET") {
		t.Fatalf("sql uses OFFSET: %s", sqlText)
	}
	joined := fmt.Sprint(args)
	if strings.Contains(sqlText, "equal-stamp") || strings.Contains(joined, "equal-stamp") && !strings.Contains(joined, `"equal-stamp"`) {
		t.Fatal("term was not a bound quoted literal")
	}
	if !strings.Contains(fmt.Sprint(args), `"equal-stamp"`) {
		t.Fatalf("match arg: %v", args)
	}

	var got []string
	var firstNext string
	cursor := ""
	for pageN := 0; pageN < 5; pageN++ {
		q.Cursor = cursor
		page, err := store.ReadPage(ctx, scope, q)
		if err != nil {
			t.Fatalf("page %d: %v", pageN, err)
		}
		if store.ReaderInUse() != 0 {
			t.Fatal("reader still in use")
		}
		if len(page.Rows) == 0 || len(page.Rows) > 2 {
			t.Fatalf("page %d rows %d", pageN, len(page.Rows))
		}
		for _, r := range page.Rows {
			got = append(got, r.IngestID)
		}
		if pageN == 0 {
			firstNext = page.Next
		}
		cursor = page.Next
		if cursor == "" {
			break
		}
	}
	if len(got) != 5 || firstNext == "" {
		t.Fatalf("paged ids %v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] <= got[i] {
			t.Fatalf("order %v", got)
		}
	}

	// Restart keeps the MAC and epoch.
	store.Close()
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	store.clock.Store(func() time.Time { return frozen })
	first, err := store.ReadPage(ctx, scope, model.Query{
		Sources: q.Sources, From: q.From, To: q.To, MinSeverity: 1, Terms: q.Terms, Limit: 2,
	})
	if err != nil || first.Next == "" {
		t.Fatalf("reopen first: %v next=%t", err, first.Next != "")
	}
	again := q
	again.Limit = 2
	again.Cursor = first.Next
	if _, err := store.ReadPage(ctx, scope, again); err != nil {
		t.Fatalf("cursor after reopen: %v", err)
	}

	// The final base64 character can encode unused bits, so a last-character
	// flip is not a reliable tamper. Change the first MAC character and append.
	bad := again
	bad.Cursor = first.Next + "x"
	if _, err := store.ReadPage(ctx, scope, bad); !errors.Is(err, ErrCursorInvalid) {
		t.Fatalf("tampered suffix: %v", err)
	}
	macAt := strings.LastIndex(first.Next, ".") + 1
	flipped := []byte(first.Next)
	if flipped[macAt] == 'A' {
		flipped[macAt] = 'B'
	} else {
		flipped[macAt] = 'A'
	}
	bad.Cursor = string(flipped)
	if _, err := store.ReadPage(ctx, scope, bad); !errors.Is(err, ErrCursorInvalid) {
		t.Fatalf("tampered mac: %v", err)
	}
	other := again
	if _, err := store.ReadPage(ctx, model.Scope{WorkspaceID: "ws_demo_b"}, other); !errors.Is(err, ErrCursorScope) {
		t.Fatalf("cross workspace: %v", err)
	}
	store.clock.Store(func() time.Time { return frozen.Add(15*time.Minute + time.Second) })
	if _, err := store.ReadPage(ctx, scope, other); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("expired: %v", err)
	}
	store.clock.Store(func() time.Time { return frozen })
	if err := store.RotateCursorEpoch(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadPage(ctx, scope, other); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("epoch: %v", err)
	}

	// Retention minute crossing, still inside the 15m TTL.
	store.clock.Store(func() time.Time { return frozen })
	fresh, err := store.ReadPage(ctx, scope, model.Query{
		Sources: q.Sources, From: q.From, To: q.To, MinSeverity: 1, Terms: q.Terms, Limit: 2,
	})
	if err != nil || fresh.Next == "" {
		t.Fatal(err)
	}
	store.clock.Store(func() time.Time { return frozen.Add(time.Minute) })
	next := model.Query{Sources: q.Sources, From: q.From, To: q.To, MinSeverity: 1, Terms: q.Terms, Limit: 2, Cursor: fresh.Next}
	if _, err := store.ReadPage(ctx, scope, next); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("retention: %v", err)
	}
}

func TestHorizonExcludesLaterRows(t *testing.T) {
	store, _ := freshStore(t)
	frozen := time.Date(2026, 2, 2, 2, 2, 30, 0, time.UTC)
	store.clock.Store(func() time.Time { return frozen })
	ctx := context.Background()
	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}
	when := frozen.Add(-2 * time.Minute)
	for i := 0; i < 3; i++ {
		if _, err := store.Commit(ctx, scope, eventBatch(t, i+1, when, "horizon synthetic", `{"k":1}`)); err != nil {
			t.Fatal(err)
		}
	}
	q := model.Query{
		Sources: []string{"src_demo_api"}, From: frozen.Add(-15 * time.Minute).Format(time.RFC3339Nano),
		To: frozen.Format(time.RFC3339Nano), MinSeverity: 1, Terms: []string{"horizon"}, Limit: 1,
	}
	first, err := store.ReadPage(ctx, scope, q)
	if err != nil || len(first.Rows) != 1 || first.Next == "" {
		t.Fatalf("first: %+v %v", first, err)
	}
	if _, err := store.Commit(ctx, scope, eventBatch(t, 9, when.Add(-time.Second), "horizon synthetic later", `{"k":1}`)); err != nil {
		t.Fatal(err)
	}
	q.Cursor = first.Next
	for pageN := 0; pageN < 5 && q.Cursor != ""; pageN++ {
		page, err := store.ReadPage(ctx, scope, q)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page.Rows {
			if strings.Contains(r.Message, "later") {
				t.Fatal("cursor included a row committed after the horizon")
			}
		}
		q.Cursor = page.Next
	}
	fresh := q
	fresh.Cursor = ""
	fresh.Limit = 10
	all, err := store.ReadPage(ctx, scope, fresh)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range all.Rows {
		if strings.Contains(r.Message, "later") {
			found = true
		}
	}
	if !found {
		t.Fatal("fresh query did not see the later row")
	}
}

func TestTypedJSONFilters(t *testing.T) {
	store, _ := freshStore(t)
	frozen := time.Date(2026, 3, 4, 5, 6, 30, 0, time.UTC)
	store.clock.Store(func() time.Time { return frozen })
	ctx := context.Background()
	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}
	when := frozen.Add(-time.Minute)
	rows := []struct {
		id   int
		msg  string
		attr string
	}{
		{1, "case NULL", `{"a":null}`},
		{2, "case MISSING", `{"other":1}`},
		{3, "case TRUE", `{"a":true}`},
		{4, "case INT", `{"a":1}`},
		{5, "case REAL", `{"a":1.0}`},
		{6, "case STR", `{"a":"1"}`},
		{7, "case NEST", `{"task":{"attempt":1},"a":false}`},
	}
	for _, row := range rows {
		if _, err := store.Commit(ctx, scope, eventBatch(t, row.id, when, row.msg, row.attr)); err != nil {
			t.Fatal(err)
		}
	}
	q := func(p model.Predicate) model.Query {
		return model.Query{
			Sources: []string{"src_demo_api"}, From: frozen.Add(-15 * time.Minute).Format(time.RFC3339Nano),
			To: frozen.Format(time.RFC3339Nano), MinSeverity: 1, Predicates: []model.Predicate{p}, Limit: 20,
		}
	}
	check := func(name string, p model.Predicate, want string) {
		t.Helper()
		page, err := store.ReadPage(ctx, scope, q(p))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(page.Rows) != 1 || !strings.Contains(page.Rows[0].Message, want) {
			t.Fatalf("%s rows %#v", name, messages(page.Rows))
		}
	}
	check("null", model.Predicate{Path: []string{"a"}, Op: "eq", Type: "null"}, "NULL")
	check("missing", model.Predicate{Path: []string{"a"}, Op: "missing"}, "MISSING")
	check("bool", model.Predicate{Path: []string{"a"}, Op: "eq", Type: "boolean", Value: []byte("true")}, "TRUE")
	check("int", model.Predicate{Path: []string{"a"}, Op: "eq", Type: "integer", Value: []byte("1")}, "INT")
	check("real", model.Predicate{Path: []string{"a"}, Op: "eq", Type: "real", Value: []byte("1.0")}, "REAL")
	check("str", model.Predicate{Path: []string{"a"}, Op: "eq", Type: "string", Value: []byte(`"1"`)}, "STR")
	check("nest", model.Predicate{Path: []string{"task", "attempt"}, Op: "eq", Type: "integer", Value: []byte("1")}, "NEST")

	fold := model.Query{
		Sources: []string{"src_demo_api"}, From: frozen.Add(-15 * time.Minute).Format(time.RFC3339Nano),
		To: frozen.Format(time.RFC3339Nano), MinSeverity: 1, Terms: []string{"cafe"}, Limit: 10,
	}
	if _, err := store.Commit(ctx, scope, eventBatch(t, 30, when, "Café folded", `{"z":false}`)); err != nil {
		t.Fatal(err)
	}
	page, err := store.ReadPage(ctx, scope, fold)
	if err != nil || len(page.Rows) != 1 {
		t.Fatalf("diacritic: %v n=%d", err, len(page.Rows))
	}
	upper := fold
	upper.Terms = []string{"CAFE"}
	page, err = store.ReadPage(ctx, scope, upper)
	if err != nil || len(page.Rows) != 1 {
		t.Fatalf("case fold: %v n=%d", err, len(page.Rows))
	}

	star := fold
	star.Terms = []string{"*"}
	page, err = store.ReadPage(ctx, scope, star)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 0 {
		t.Fatalf("star matched %d", len(page.Rows))
	}
	inj := fold
	inj.Terms = []string{`" OR 1=1 --`}
	page, err = store.ReadPage(ctx, scope, inj)
	if err != nil || len(page.Rows) != 0 {
		t.Fatalf("inject: %v n=%d", err, len(page.Rows))
	}
}

func TestReaderPoolAndInterrupt(t *testing.T) {
	store, _ := freshStore(t)
	if store.reader.Stats().MaxOpenConnections != maxReaders {
		t.Fatalf("max open %d", store.reader.Stats().MaxOpenConnections)
	}
	ctx := context.Background()
	var held []*sql.Conn
	for i := 0; i < maxReaders; i++ {
		c, err := store.reader.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	short, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if _, err := store.reader.Conn(short); err == nil {
		t.Fatal("fifth reader was admitted")
	}
	for _, c := range held {
		c.Close()
	}
	if store.ReaderInUse() != 0 {
		t.Fatal("in use after release")
	}

	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}
	frozen := time.Now().UTC().Truncate(time.Minute).Add(20 * time.Second)
	store.clock.Store(func() time.Time { return frozen })
	if _, err := store.Commit(ctx, scope, eventBatch(t, 1, frozen.Add(-time.Second), "pool synthetic", `{"k":1}`)); err != nil {
		t.Fatal(err)
	}
	q := model.Query{
		Sources: []string{"src_demo_api"}, From: frozen.Add(-time.Minute).Format(time.RFC3339Nano),
		To: frozen.Add(time.Second).Format(time.RFC3339Nano), MinSeverity: 1, Terms: []string{"pool"}, Limit: 10,
	}
	for i := 0; i < 8; i++ {
		if _, err := store.ReadPage(ctx, scope, q); err != nil {
			t.Fatal(err)
		}
	}
	st := store.reader.Stats()
	if st.OpenConnections > maxReaders || st.InUse != 0 {
		t.Fatalf("pool open=%d inuse=%d", st.OpenConnections, st.InUse)
	}

	start := time.Now()
	err := store.withReader(ctx, func(ctx context.Context, conn *sql.Conn, b *progressBudget) error {
		b.arm(vmStepBudget)
		_, err := conn.ExecContext(ctx, `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c LIMIT 100000000) SELECT max(x) FROM c`)
		return err
	})
	elapsed := time.Since(start)
	var budget *BudgetError
	if !errors.As(err, &budget) || budget.Steps < vmStepBudget || elapsed > 5*time.Second {
		t.Fatalf("budget err=%v elapsed=%s", err, elapsed)
	}
	if store.ReaderInUse() != 0 {
		t.Fatal("rows left open after budget abort")
	}
	t.Logf("vm_steps=%d elapsed=%s", budget.Steps, elapsed)

	slowCtx := context.WithValue(ctx, budgetCtxKey{}, int64(1_000_000_000))
	slowCtx, cancel2 := context.WithTimeout(slowCtx, 80*time.Millisecond)
	defer cancel2()
	start = time.Now()
	err = store.withReader(slowCtx, func(ctx context.Context, conn *sql.Conn, b *progressBudget) error {
		b.arm(stepLimit(ctx))
		_, err := conn.ExecContext(ctx, `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c LIMIT 100000000) SELECT max(x) FROM c`)
		return err
	})
	elapsed = time.Since(start)
	if !errors.Is(err, ErrQueryTimeout) || elapsed > 2*time.Second {
		t.Fatalf("interrupt err=%v elapsed=%s", err, elapsed)
	}
	if store.ReaderInUse() != 0 {
		t.Fatal("rows left open after interrupt")
	}
	t.Logf("interrupt_elapsed=%s", elapsed)
}

func TestIndexesAndExplain(t *testing.T) {
	store, _ := freshStore(t)
	ctx := context.Background()
	var names []string
	rows, err := store.writer.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='logs'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	rows.Close()
	for _, want := range []string{"idx_logs_time", "idx_logs_ws_time", "idx_logs_ws_ingest", "idx_logs_retention"} {
		if !containsStr(names, want) {
			t.Fatalf("missing %s in %v", want, names)
		}
	}
	frozen := time.Date(2026, 6, 1, 12, 0, 30, 0, time.UTC)
	store.clock.Store(func() time.Time { return frozen })
	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}
	if _, err := store.Commit(ctx, scope, eventBatch(t, 1, frozen.Add(-time.Minute), "explain synthetic", `{"k":1}`)); err != nil {
		t.Fatal(err)
	}
	q := model.Query{
		Sources: []string{"src_demo_api"}, From: frozen.Add(-15 * time.Minute).Format(time.RFC3339Nano),
		To: frozen.Format(time.RFC3339Nano), MinSeverity: 1, Limit: 50,
	}
	plan, err := explainSearch(ctx, store, scope, q)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("explain_time=%s", plan)
	if !strings.Contains(plan, "idx_logs_time") && !strings.Contains(plan, "idx_logs_ws_time") {
		t.Fatalf("plan missing time index: %s", plan)
	}
	q.Terms = []string{"explain"}
	plan, err = explainSearch(ctx, store, scope, q)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("explain_fts=%s", plan)
	if !strings.Contains(plan, "logs_fts") {
		t.Fatalf("plan missing fts: %s", plan)
	}
}

func TestFTSDeleteTrigger(t *testing.T) {
	store, _ := freshStore(t)
	ctx := context.Background()
	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}
	frozen := time.Date(2026, 6, 1, 12, 0, 30, 0, time.UTC)
	store.clock.Store(func() time.Time { return frozen })
	if _, err := store.Commit(ctx, scope, eventBatch(t, 1, frozen.Add(-time.Minute), "delete-me token", `{"k":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.SubmitMaintenance(ctx, 1, func(ctx context.Context) error {
		_, err := store.writer.ExecContext(ctx, `DELETE FROM logs WHERE message LIKE 'delete-me%'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	q := model.Query{
		Sources: []string{"src_demo_api"}, From: frozen.Add(-15 * time.Minute).Format(time.RFC3339Nano),
		To: frozen.Format(time.RFC3339Nano), MinSeverity: 1, Terms: []string{"delete-me"}, Limit: 10,
	}
	page, err := store.ReadPage(ctx, scope, q)
	if err != nil || len(page.Rows) != 0 {
		t.Fatalf("after delete: %v n=%d", err, len(page.Rows))
	}
	if _, err := store.writer.ExecContext(ctx, `INSERT INTO logs_fts(logs_fts) VALUES('integrity-check')`); err != nil {
		t.Fatalf("fts integrity: %v", err)
	}
}

func eventBatch(t *testing.T, n int, when time.Time, message, attrs string) model.Batch {
	t.Helper()
	b := model.Batch{
		Version: 0,
		BatchID: fmt.Sprintf("%08x-0000-4000-8000-%012d", n, n),
		Events: []model.Event{{
			EventID:        fmt.Sprintf("%08x-0000-4000-8000-%012d", n+1000, n),
			EventTime:      when.UTC().Format(time.RFC3339Nano),
			SeverityNumber: 9,
			SeverityText:   "INFO",
			Message:        message,
			Attributes:     json.RawMessage(attrs),
		}},
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	out, err := model.ValidateBatch(raw, when.UTC())
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	return out
}

func messages(rows []model.Record) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Message
	}
	return out
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
