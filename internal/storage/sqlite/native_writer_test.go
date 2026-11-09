package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "test.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, path
}

func reopen(t *testing.T, path string, prev *Store) *Store {
	t.Helper()
	if err := prev.Close(); err != nil {
		t.Logf("close: %v", err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

var (
	demoScope  = model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}
	otherScope = model.Scope{WorkspaceID: "ws_demo_b", SourceID: "src_demo_worker"}
)

func mustNative(t *testing.T, batchID string, n int, msg string, when time.Time, tag int) model.Batch {
	t.Helper()
	when = when.UTC().Truncate(time.Microsecond)
	events := make([]model.Event, n)
	for i := range events {
		events[i] = model.Event{
			EventID:        fmt.Sprintf("%08x-0000-4000-8000-%012x", tag, i+1),
			EventTime:      when.Format(time.RFC3339Nano),
			SeverityNumber: 9,
			SeverityText:   "INFO",
			Message:        msg,
			Attributes:     json.RawMessage(`{"b":true,"a":1}`),
		}
	}
	raw, err := json.Marshal(model.Batch{Version: model.NativeVersion, BatchID: batchID, Events: events})
	if err != nil {
		t.Fatal(err)
	}
	b, err := model.ValidateNative(raw)
	if err != nil {
		t.Fatalf("validate %s: %v raw=%s", batchID, err, raw)
	}
	return b
}

func snap(t *testing.T, s *Store) (logs, receipts int, wm int64) {
	t.Helper()
	ctx := context.Background()
	if err := s.writer.QueryRowContext(ctx, `SELECT COUNT(*) FROM logs`).Scan(&logs); err != nil {
		t.Fatal(err)
	}
	if err := s.writer.QueryRowContext(ctx, `SELECT COUNT(*) FROM receipts`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	var err error
	wm, err = s.Watermark(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func ftsOK(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.writer.Exec(`INSERT INTO logs_fts(logs_fts) VALUES('integrity-check')`); err != nil {
		t.Fatalf("fts integrity: %v", err)
	}
}

func holdWriter(t *testing.T, s *Store) func() {
	t.Helper()
	return hold(t, s, false)
}

func holdTx(t *testing.T, s *Store) func() {
	t.Helper()
	return hold(t, s, true)
}

func hold(t *testing.T, s *Store, begin bool) func() {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.submit(context.Background(), "blocker", 1, classIngest, func(ctx context.Context) error {
			if begin {
				tx, err := s.writer.BeginTx(ctx, nil)
				if err != nil {
					return err
				}
				defer tx.Rollback()
			}
			close(started)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not start")
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			close(release)
			if err := <-errCh; err != nil {
				t.Errorf("hold: %v", err)
			}
		})
	}
}

func waitQueued(t *testing.T, s *Store, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.sched.mu.Lock()
		n := s.sched.queued
		s.sched.mu.Unlock()
		if n >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("queued %d want %d", n, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSecondInstanceRefuses(t *testing.T) {
	s, path := newStore(t)
	if _, err := Open(path); err == nil {
		t.Fatal("second open succeeded")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	again.Close()
}

func TestPragmaReadbackReplacement(t *testing.T) {
	s, path := newStore(t)
	ctx := context.Background()
	first, err := s.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"writer_journal_mode", "writer_synchronous", "writer_foreign_keys",
		"writer_busy_timeout", "writer_locking_mode", "writer_auto_vacuum", "writer_query_only",
		"reader_journal_mode", "reader_synchronous", "reader_foreign_keys",
		"reader_busy_timeout", "reader_locking_mode", "reader_auto_vacuum", "reader_query_only",
	}
	expect := map[string]string{
		"writer_journal_mode": "wal", "writer_synchronous": "2", "writer_foreign_keys": "1",
		"writer_busy_timeout": "250", "writer_locking_mode": "normal", "writer_auto_vacuum": "2",
		"writer_query_only":   "0",
		"reader_journal_mode": "wal", "reader_synchronous": "2", "reader_foreign_keys": "1",
		"reader_busy_timeout": "250", "reader_locking_mode": "normal", "reader_auto_vacuum": "2",
		"reader_query_only": "1",
	}
	for _, k := range want {
		if first[k] != expect[k] {
			t.Errorf("%s=%q want %q", k, first[k], expect[k])
		}
	}
	t.Logf("sqlite_version=%s fts5=%s source_id=%s", first["sqlite_version"], first["fts5"], first["sqlite_source_id"])
	t.Logf("compile_options=%s", first["compile_options"])
	s = reopen(t, path, s)
	second, err := s.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range want {
		if second[k] != expect[k] {
			t.Errorf("reopen %s=%q want %q", k, second[k], expect[k])
		}
	}
}

func TestSchemaWithoutReceiptsRefused(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "old.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, q := range []string{
		`PRAGMA auto_vacuum=2`,
		`PRAGMA journal_mode=WAL`,
		`CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO metadata(key, value) VALUES('watermark','0')`,
		`PRAGMA user_version=1`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Open(path)
	if !errors.Is(err, ErrOlderSlice) {
		t.Fatalf("open old schema: %v", err)
	}
	if err.Error() != "database is from an older slice and cannot be opened; start the service with a new -state directory" {
		t.Fatalf("open message: %v", err)
	}
}

func TestWriterBeginImmediateLock(t *testing.T) {
	s, path := newStore(t)
	release := holdTx(t, s)
	defer release()
	u := url.URL{Scheme: "file", Path: path, RawQuery: "_busy_timeout=0&_txlock=immediate"}
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("BEGIN IMMEDIATE"); err == nil {
		_, _ = db.Exec("ROLLBACK")
		t.Fatal("second BEGIN IMMEDIATE acquired the writer lock")
	}
}

func TestSchedulerAdmission(t *testing.T) {
	t.Run("jobs_and_wait", func(t *testing.T) {
		s, _ := newStore(t)
		release := holdWriter(t, s)
		defer release()
		errCh := make(chan error, 64)
		for i := 0; i < maxWriteJobs; i++ {
			go func() {
				errCh <- s.submit(context.Background(), "src_a", 1, classIngest, func(context.Context) error { return nil })
			}()
		}
		waitQueued(t, s, maxWriteJobs)
		start := time.Now()
		err := s.submit(context.Background(), "src_a", 1, classIngest, func(context.Context) error { return nil })
		elapsed := time.Since(start)
		t.Logf("admission_reject_after=%s cap_jobs=%d", elapsed, maxWriteJobs)
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("65th job: %v", err)
		}
		if elapsed < admitWait/2 || elapsed > 750*time.Millisecond {
			t.Fatalf("admission wait %s", elapsed)
		}
		release()
		for i := 0; i < maxWriteJobs; i++ {
			if err := <-errCh; err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("payload_bytes", func(t *testing.T) {
		s, _ := newStore(t)
		release := holdWriter(t, s)
		defer release()
		half := maxWriteBytes / 2
		errCh := make(chan error, 2)
		for i := 0; i < 2; i++ {
			go func() {
				errCh <- s.submit(context.Background(), "src_a", half, classIngest, func(context.Context) error { return nil })
			}()
		}
		waitQueued(t, s, 2)
		err := s.submit(context.Background(), "src_a", 1, classIngest, func(context.Context) error { return nil })
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("over 16MiB: %v", err)
		}
		huge := s.submit(context.Background(), "src_b", maxWriteBytes+1, classIngest, func(context.Context) error { return nil })
		if !errors.Is(huge, ErrBusy) {
			t.Fatalf("single oversize job: %v", huge)
		}
		t.Logf("queue_byte_cap=%d rejected_extra=1 rejected_single=%d", maxWriteBytes, maxWriteBytes+1)
	})

	t.Run("per_source", func(t *testing.T) {
		s, _ := newStore(t)
		release := holdWriter(t, s)
		defer release()
		errCh := make(chan error, maxPerSource+1)
		for i := 0; i < maxPerSource; i++ {
			go func() {
				errCh <- s.submit(context.Background(), "src_a", 1, classIngest, func(context.Context) error { return nil })
			}()
		}
		go func() {
			errCh <- s.submit(context.Background(), "src_b", 1, classIngest, func(context.Context) error { return nil })
		}()
		deadline := time.Now().Add(2 * time.Second)
		for {
			s.sched.mu.Lock()
			a, b := s.sched.perSource["src_a"], s.sched.perSource["src_b"]
			s.sched.mu.Unlock()
			if a >= maxPerSource && b >= 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("per-source queue a=%d b=%d", a, b)
			}
			time.Sleep(time.Millisecond)
		}
		err := s.submit(context.Background(), "src_a", 1, classIngest, func(context.Context) error { return nil })
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("source A over quota: %v", err)
		}
		t.Logf("per_source_cap=%d other_source_queued=1 extra_rejected=1", maxPerSource)
	})

	t.Run("round_robin", func(t *testing.T) {
		s, _ := newStore(t)
		release := holdWriter(t, s)
		defer release()
		var mu sync.Mutex
		var order []string
		record := func(name string) func(context.Context) error {
			return func(context.Context) error {
				mu.Lock()
				order = append(order, name)
				mu.Unlock()
				return nil
			}
		}
		errCh := make(chan error, 11)
		for i := 0; i < 10; i++ {
			go func() {
				errCh <- s.submit(context.Background(), "src_a", 1, classIngest, record("A"))
			}()
		}
		go func() {
			errCh <- s.submit(context.Background(), "src_b", 1, classIngest, record("B"))
		}()
		waitQueued(t, s, 11)
		release()
		for i := 0; i < 11; i++ {
			if err := <-errCh; err != nil {
				t.Fatal(err)
			}
		}
		mu.Lock()
		got := append([]string(nil), order...)
		mu.Unlock()
		bAt := -1
		for i, name := range got {
			if name == "B" {
				bAt = i
				break
			}
		}
		t.Logf("service_order=%v", got)
		if bAt < 0 || bAt > 1 {
			t.Fatalf("source B ran at %d, want within the first round", bAt)
		}
	})

	t.Run("maintenance_turn", func(t *testing.T) {
		s, _ := newStore(t)
		release := holdWriter(t, s)
		defer release()
		var mu sync.Mutex
		var order []string
		record := func(name string) func(context.Context) error {
			return func(context.Context) error {
				mu.Lock()
				order = append(order, name)
				mu.Unlock()
				return nil
			}
		}
		errCh := make(chan error, 16)
		for i := 0; i < 15; i++ {
			go func() {
				errCh <- s.submit(context.Background(), "src_a", 1, classIngest, record("A"))
			}()
		}
		go func() {
			errCh <- s.SubmitMaintenance(context.Background(), 1, record("M"))
		}()
		waitQueued(t, s, 16)
		release()
		for i := 0; i < 16; i++ {
			if err := <-errCh; err != nil {
				t.Fatal(err)
			}
		}
		mu.Lock()
		got := append([]string(nil), order...)
		mu.Unlock()
		mAt := -1
		for i, name := range got {
			if name == "M" {
				mAt = i
				break
			}
		}
		t.Logf("maintenance_order=%v", got)
		if mAt < 0 || mAt > 10 {
			t.Fatalf("maintenance ran at %d, want at or before index 10", mAt)
		}
	})
}

func TestConcurrentWritersSerialized(t *testing.T) {
	s, _ := newStore(t)
	when := time.Now().UTC().Truncate(time.Second)
	const n = 32
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("10000000-0000-4000-8000-%012x", i+1)
			b := mustNative(t, id, 1, "concurrent", when, i+1)
			_, err := s.CommitNative(context.Background(), demoScope, b, 64)
			errCh <- err
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
		if err != nil && strings.Contains(err.Error(), "SQLITE_BUSY") {
			t.Fatalf("busy leaked: %v", err)
		}
	}
	logs, receipts, wm := snap(t, s)
	t.Logf("concurrent_writers=%d logs=%d receipts=%d watermark=%d", n, logs, receipts, wm)
	if logs != n || receipts != n || wm != int64(n) {
		t.Fatalf("logs=%d receipts=%d watermark=%d", logs, receipts, wm)
	}
	ftsOK(t, s)
}

func TestBusyRetryAndRecovery(t *testing.T) {
	s, path := newStore(t)
	u := url.URL{Scheme: "file", Path: path, RawQuery: "_busy_timeout=0&_txlock=immediate"}
	ext, err := sql.Open("sqlite3", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Close()
	if _, err := ext.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	when := time.Now().UTC().Truncate(time.Second)
	b := mustNative(t, "20000000-0000-4000-8000-000000000001", 2, "busy-batch", when, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = s.CommitNative(ctx, demoScope, b, len("busy"))
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("held lock: %v", err)
	}
	if strings.Contains(err.Error(), "SQLITE_BUSY") {
		t.Fatalf("raw busy leaked: %v", err)
	}
	logs, receipts, wm := snap(t, s)
	if logs != 0 || receipts != 0 || wm != 0 {
		t.Fatalf("partial writes logs=%d receipts=%d wm=%d", logs, receipts, wm)
	}
	if _, err := ext.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitNative(context.Background(), demoScope, b, 32); err != nil {
		t.Fatal(err)
	}
	logs, receipts, wm = snap(t, s)
	t.Logf("after_busy_release logs=%d receipts=%d watermark=%d", logs, receipts, wm)
	if logs != 2 || receipts != 1 || wm != 2 {
		t.Fatalf("logs=%d receipts=%d wm=%d", logs, receipts, wm)
	}
	ftsOK(t, s)
}

func TestAtomicMidBatch(t *testing.T) {
	s, _ := newStore(t)
	when := time.Now().UTC().Truncate(time.Second)
	ok := mustNative(t, "30000000-0000-4000-8000-000000000001", 1, "baseline", when, 3)
	if _, err := s.CommitNative(context.Background(), demoScope, ok, 32); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec(`CREATE TRIGGER abort_mid BEFORE INSERT ON logs WHEN new.message='boom' BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	bad := mustNative(t, "30000000-0000-4000-8000-000000000002", 3, "boom", when, 4)
	_, err := s.CommitNative(context.Background(), demoScope, bad, 64)
	if err == nil {
		t.Fatal("mid-batch failure committed")
	}
	logs, receipts, wm := snap(t, s)
	t.Logf("mid_batch_failure logs=%d receipts=%d watermark=%d err=%v", logs, receipts, wm, err)
	if logs != 1 || receipts != 1 || wm != 1 {
		t.Fatalf("partial commit logs=%d receipts=%d wm=%d", logs, receipts, wm)
	}
	var boom int
	if err := s.writer.QueryRow(`SELECT COUNT(*) FROM logs_fts WHERE logs_fts MATCH 'boom'`).Scan(&boom); err != nil {
		t.Fatal(err)
	}
	if boom != 0 {
		t.Fatalf("fts kept rolled-back rows: %d", boom)
	}
	ftsOK(t, s)
	if _, err := s.writer.Exec(`DROP TRIGGER abort_mid`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitNative(context.Background(), demoScope, bad, 64); err != nil {
		t.Fatal(err)
	}
	logs, receipts, wm = snap(t, s)
	if logs != 4 || receipts != 2 || wm != 4 {
		t.Fatalf("retry logs=%d receipts=%d wm=%d", logs, receipts, wm)
	}
}

func TestIdempotentReplayAndConflict(t *testing.T) {
	s, _ := newStore(t)
	when := time.Now().UTC().Truncate(time.Second)
	const id = "40000000-0000-4000-8000-000000000001"
	b := mustNative(t, id, 3, "replay-me", when, 5)
	first, err := s.CommitNative(context.Background(), demoScope, b, 128)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := model.ParseNativeReceipt(first.Body)
	if err != nil {
		t.Fatal(err)
	}
	if rec.BatchID != id || rec.Accepted != 3 || rec.FirstIngestID == rec.LastIngestID {
		t.Fatalf("receipt %+v", rec)
	}
	got, err := time.Parse(time.RFC3339Nano, rec.ReceivedAt)
	if err != nil {
		t.Fatal(err)
	}
	until, err := time.Parse(time.RFC3339Nano, rec.ReplayUntil)
	if err != nil {
		t.Fatal(err)
	}
	if until.Sub(got) != 48*time.Hour {
		t.Fatalf("replay window %s", until.Sub(got))
	}
	second, err := s.CommitNative(context.Background(), demoScope, b, 128)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Body, second.Body) {
		t.Fatalf("replay body\n%s\n%s", first.Body, second.Body)
	}
	// Same identity with different key order and whitespace.
	raw := []byte(`{ "version" : 1 , "events" : [ ` +
		`{"attributes":{"a":1,"b":true},"message":"replay-me","severity_text":"INFO","severity_number":9,"event_time":"` + when.Format(time.RFC3339Nano) + `","event_id":"00000005-0000-4000-8000-000000000001"},` +
		`{"attributes":{"b":true,"a":1},"message":"replay-me","severity_text":"INFO","severity_number":9,"event_time":"` + when.Format(time.RFC3339Nano) + `","event_id":"00000005-0000-4000-8000-000000000002"},` +
		`{"attributes":{"a":1,"b":true},"message":"replay-me","severity_text":"INFO","severity_number":9,"event_time":"` + when.Format(time.RFC3339Nano) + `","event_id":"00000005-0000-4000-8000-000000000003"}` +
		`], "batch_id" : "` + id + `" }`)
	alt, err := model.ValidateNative(raw)
	if err != nil {
		t.Fatal(err)
	}
	third, err := s.CommitNative(context.Background(), demoScope, alt, len(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Body, third.Body) {
		t.Fatalf("canonical replay\n%s\n%s", first.Body, third.Body)
	}
	logs, receipts, wm := snap(t, s)
	if logs != 3 || receipts != 1 || wm != 3 {
		t.Fatalf("after replay logs=%d receipts=%d wm=%d", logs, receipts, wm)
	}
	changed := b
	changed.Events = append([]model.Event(nil), b.Events...)
	changed.Events[0].Message = "changed"
	if _, err := s.CommitNative(context.Background(), demoScope, changed, 128); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	logs2, _, wm2 := snap(t, s)
	if logs2 != logs || wm2 != wm {
		t.Fatalf("conflict wrote logs=%d wm=%d", logs2, wm2)
	}
	t.Logf("replay_equivalent=1 conflict_rows_unchanged=1 logs=%d watermark=%d", logs, wm)

	// Other source is a different identity.
	if _, err := s.CommitNative(context.Background(), otherScope, b, 128); err != nil {
		t.Fatal(err)
	}
	logs, receipts, wm = snap(t, s)
	if logs != 6 || receipts != 2 {
		t.Fatalf("other source logs=%d receipts=%d wm=%d", logs, receipts, wm)
	}
}

func TestConcurrentReplay(t *testing.T) {
	s, _ := newStore(t)
	when := time.Now().UTC().Truncate(time.Second)
	b := mustNative(t, "50000000-0000-4000-8000-000000000001", 2, "race-replay", when, 6)
	const n = 8
	bodies := make([][]byte, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := s.CommitNative(context.Background(), demoScope, b, 40)
			errs[i] = err
			bodies[i] = res.Body
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(bodies[0], bodies[i]) {
			t.Fatalf("body %d differs", i)
		}
	}
	logs, _, wm := snap(t, s)
	if logs != 2 || wm != 2 {
		t.Fatalf("duplicate rows logs=%d wm=%d", logs, wm)
	}
	changed := b
	changed.Events = append([]model.Event(nil), b.Events...)
	changed.Events[1].Message = "nope"
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.CommitNative(context.Background(), demoScope, changed, 40)
			if !errors.Is(err, ErrConflict) {
				t.Errorf("conflict: %v", err)
			}
		}()
	}
	wg.Wait()
	logs2, _, _ := snap(t, s)
	if logs2 != 2 {
		t.Fatalf("conflict duplicated logs=%d", logs2)
	}
}

func TestReceiptSurvivesLogDeletion(t *testing.T) {
	s, path := newStore(t)
	when := time.Now().UTC().Truncate(time.Second)
	b := mustNative(t, "60000000-0000-4000-8000-000000000001", 1, "keep-receipt", when, 7)
	first, err := s.CommitNative(context.Background(), demoScope, b, 16)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec(`DELETE FROM logs`); err != nil {
		t.Fatal(err)
	}
	logs, receipts, wm := snap(t, s)
	if logs != 0 || receipts != 1 || wm != 1 {
		t.Fatalf("after delete logs=%d receipts=%d wm=%d", logs, receipts, wm)
	}
	s = reopen(t, path, s)
	wm, err = s.Watermark(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if wm != 1 {
		t.Fatalf("watermark recovered as %d", wm)
	}
	again, err := s.CommitNative(context.Background(), demoScope, b, 16)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Body, again.Body) {
		t.Fatalf("replay after log delete\n%s\n%s", first.Body, again.Body)
	}
	logs, _, wm = snap(t, s)
	if logs != 0 || wm != 1 {
		t.Fatalf("replay recreated logs=%d wm=%d", logs, wm)
	}
	t.Logf("receipt_retained_without_logs watermark=%d", wm)
}

func TestKillRestartReplay(t *testing.T) {
	when := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	const id = "70000000-0000-4000-8000-000000000001"

	t.Run("before_commit", func(t *testing.T) {
		s, path := newStore(t)
		s.clock.Store(func() time.Time { return when })
		s.fault.Store(faultBeforeCommit)
		b := mustNative(t, id, 2, "die-before", when, 8)
		_, err := s.CommitNative(context.Background(), demoScope, b, 20)
		if !errors.Is(err, errKilled) {
			t.Fatalf("kill: %v", err)
		}
		s = reopen(t, path, s)
		logs, receipts, wm := snap(t, s)
		t.Logf("cycle=before_commit logs=%d receipts=%d watermark=%d", logs, receipts, wm)
		if logs != 0 || receipts != 0 || wm != 0 {
			t.Fatalf("partial state logs=%d receipts=%d wm=%d", logs, receipts, wm)
		}
		ftsOK(t, s)
	})

	t.Run("post_commit_pre_response", func(t *testing.T) {
		s, path := newStore(t)
		s.clock.Store(func() time.Time { return when })
		s.fault.Store(faultAfterCommit)
		b := mustNative(t, id, 2, "die-after", when, 9)
		_, err := s.CommitNative(context.Background(), demoScope, b, 20)
		if !errors.Is(err, errKilled) {
			t.Fatalf("kill: %v", err)
		}
		s = reopen(t, path, s)
		s.clock.Store(func() time.Time { return when })
		logs, _, wm := snap(t, s)
		replay, err := s.CommitNative(context.Background(), demoScope, b, 20)
		if err != nil {
			t.Fatal(err)
		}
		rec, err := model.ParseNativeReceipt(replay.Body)
		if err != nil {
			t.Fatal(err)
		}
		logs2, receipts2, wm2 := snap(t, s)
		t.Logf("cycle=post_commit logs=%d receipts=%d watermark=%d replay_accepted=%d", logs2, receipts2, wm2, rec.Accepted)
		if logs != 2 || logs2 != 2 || receipts2 != 1 || wm2 != wm || rec.BatchID != id || rec.Accepted != 2 {
			t.Fatalf("duplicated or lost logs=%d/%d wm=%d/%d", logs, logs2, wm, wm2)
		}
		ftsOK(t, s)
	})

	t.Run("clean_restart", func(t *testing.T) {
		s, path := newStore(t)
		s.clock.Store(func() time.Time { return when })
		b := mustNative(t, id, 2, "clean", when, 10)
		first, err := s.CommitNative(context.Background(), demoScope, b, 20)
		if err != nil {
			t.Fatal(err)
		}
		s = reopen(t, path, s)
		s.clock.Store(func() time.Time { return when })
		second, err := s.CommitNative(context.Background(), demoScope, b, 20)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first.Body, second.Body) {
			t.Fatal("restart replay body changed")
		}
		logs, receipts, wm := snap(t, s)
		t.Logf("cycle=clean_restart logs=%d receipts=%d watermark=%d", logs, receipts, wm)
		if logs != 2 || receipts != 1 || wm != 2 {
			t.Fatalf("logs=%d receipts=%d wm=%d", logs, receipts, wm)
		}
	})

	t.Run("midnight", func(t *testing.T) {
		s, _ := newStore(t)
		night := time.Date(2026, 1, 2, 23, 30, 0, 0, time.UTC)
		s.clock.Store(func() time.Time { return night })
		b := mustNative(t, id, 1, "midnight", night, 11)
		first, err := s.CommitNative(context.Background(), demoScope, b, 16)
		if err != nil {
			t.Fatal(err)
		}
		s.clock.Store(func() time.Time { return night.Add(2 * time.Hour) })
		second, err := s.CommitNative(context.Background(), demoScope, b, 16)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first.Body, second.Body) {
			t.Fatalf("midnight changed receipt\n%s\n%s", first.Body, second.Body)
		}
		logs, _, wm := snap(t, s)
		t.Logf("cycle=midnight logs=%d watermark=%d receipt=%s", logs, wm, first.Body)
		if logs != 1 || wm != 1 {
			t.Fatalf("logs=%d wm=%d", logs, wm)
		}
	})

	t.Run("expiry", func(t *testing.T) {
		s, _ := newStore(t)
		start := time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)
		s.clock.Store(func() time.Time { return start })
		b := mustNative(t, id, 1, "expires", start, 12)
		first, err := s.CommitNative(context.Background(), demoScope, b, 16)
		if err != nil {
			t.Fatal(err)
		}
		// Inside the window, an event that is now outside the 7d horizon still replays.
		old := start.Add(-7 * 24 * time.Hour)
		horizonBatch := mustNative(t, "70000000-0000-4000-8000-000000000002", 1, "horizon", old, 13)
		s.clock.Store(func() time.Time { return start })
		kept, err := s.CommitNative(context.Background(), demoScope, horizonBatch, 16)
		if err != nil {
			t.Fatal(err)
		}
		s.clock.Store(func() time.Time { return start.Add(24 * time.Hour) })
		again, err := s.CommitNative(context.Background(), demoScope, horizonBatch, 16)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(kept.Body, again.Body) {
			t.Fatal("horizon replay changed the receipt")
		}
		fresh := mustNative(t, "70000000-0000-4000-8000-000000000003", 1, "too-old", old, 14)
		if _, err := s.CommitNative(context.Background(), demoScope, fresh, 16); !errors.Is(err, model.ErrValidation) {
			t.Fatalf("new batch outside horizon: %v", err)
		}
		// At replay_until the guarantee ends and a duplicate insert is allowed.
		s.clock.Store(func() time.Time { return start.Add(48 * time.Hour) })
		dup, err := s.CommitNative(context.Background(), demoScope, b, 16)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(first.Body, dup.Body) {
			t.Fatal("expired replay returned the original receipt")
		}
		logs, receipts, wm := snap(t, s)
		t.Logf("cycle=expiry logs=%d receipts=%d watermark=%d", logs, receipts, wm)
		// horizon batch + original + expired reinsert = 3 logs, 3 receipts (expired one replaced, horizon remains, new one added: receipts = horizon + replaced original = 2? )
		// receipts: original batch replaced, horizon batch remains => 2 receipts. logs: horizon + original + duplicate = 3.
		if logs != 3 || receipts != 2 || wm != 3 {
			t.Fatalf("expiry logs=%d receipts=%d wm=%d", logs, receipts, wm)
		}
		ftsOK(t, s)
	})
}

func TestCommitLatencyLog(t *testing.T) {
	s, _ := newStore(t)
	when := time.Now().UTC().Truncate(time.Second)
	var max time.Duration
	const n = 20
	start := time.Now()
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("80000000-0000-4000-8000-%012x", i+1)
		b := mustNative(t, id, 1, "latency", when, 20+i)
		t0 := time.Now()
		if _, err := s.CommitNative(context.Background(), demoScope, b, 32); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(t0); d > max {
			max = d
		}
	}
	logs, _, wm := snap(t, s)
	t.Logf("commit_latency_n=%d max=%s total=%s logs=%d watermark=%d", n, max, time.Since(start), logs, wm)
	if max > time.Second {
		t.Fatalf("commit max %s", max)
	}
	if logs != n || wm != int64(n) {
		t.Fatalf("logs=%d wm=%d", logs, wm)
	}
}

func TestNativeBatchOf200(t *testing.T) {
	s, _ := newStore(t)
	when := time.Now().UTC().Truncate(time.Second)
	b := mustNative(t, "90000000-0000-4000-8000-000000000001", 200, "bulk", when, 30)
	res, err := s.CommitNative(context.Background(), demoScope, b, 4096)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := model.ParseNativeReceipt(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Accepted != 200 || rec.FirstIngestID != "1" || rec.LastIngestID != "200" {
		t.Fatalf("receipt %+v", rec)
	}
	logs, receipts, wm := snap(t, s)
	if logs != 200 || receipts != 1 || wm != 200 {
		t.Fatalf("logs=%d receipts=%d wm=%d", logs, receipts, wm)
	}
	var fts int
	if err := s.writer.QueryRow(`SELECT COUNT(*) FROM logs_fts WHERE logs_fts MATCH 'bulk'`).Scan(&fts); err != nil {
		t.Fatal(err)
	}
	if fts != 200 {
		t.Fatalf("fts=%d", fts)
	}
	ftsOK(t, s)
}

func TestUnknownScopeWritesNothing(t *testing.T) {
	s, _ := newStore(t)
	when := time.Now().UTC().Truncate(time.Second)
	b := mustNative(t, "91000000-0000-4000-8000-000000000001", 1, "nope", when, 31)
	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "missing_source"}
	if _, err := s.CommitNative(context.Background(), scope, b, 16); err == nil {
		t.Fatal("missing source committed")
	}
	logs, receipts, wm := snap(t, s)
	if logs != 0 || receipts != 0 || wm != 0 {
		t.Fatalf("logs=%d receipts=%d wm=%d", logs, receipts, wm)
	}
}
