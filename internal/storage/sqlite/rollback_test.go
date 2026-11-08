package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"example.invalid/hearthlog/internal/model"
)

func TestRollbackAtomic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dbPath := filepath.Join(state, "rollback.db")

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}

	// Baseline commit
	b1 := negativeBatch(t, 1, "baseline event")
	b1.BatchID = "aaaa0000-0000-4000-8000-000000000001"
	b1.Events[0].EventID = "bbbb0000-0000-4000-8000-000000000001"
	r1, err := store.Commit(ctx, scope, b1)
	if err != nil {
		t.Fatalf("baseline commit: %v", err)
	}
	if r1.Accepted != 1 {
		t.Fatalf("baseline accepted: got %d want 1", r1.Accepted)
	}

	// Install trigger to abort on metadata update
	_, err = store.writer.ExecContext(ctx,
		`CREATE TRIGGER abort_meta BEFORE UPDATE ON metadata BEGIN SELECT RAISE(ABORT, 'forced'); END`)
	if err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	// Second commit should fail
	b2 := negativeBatch(t, 1, "rejected marker")
	b2.BatchID = "aaaa0000-0000-4000-8000-000000000002"
	b2.Events[0].EventID = "bbbb0000-0000-4000-8000-000000000002"
	r2, err := store.Commit(ctx, scope, b2)
	if err == nil {
		t.Fatalf("expected error on second commit, got nil")
	}
	if r2.Accepted != 0 {
		t.Fatalf("rejected accepted: got %d want 0", r2.Accepted)
	}

	// Verify rollback: logs count should be 1, FTS baseline 1, rejected 0
	var logCount int
	if err := store.writer.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM logs WHERE workspace_id=? AND source_id=?`,
		scope.WorkspaceID, scope.SourceID,
	).Scan(&logCount); err != nil {
		t.Fatalf("count logs: %v", err)
	}
	if logCount != 1 {
		t.Fatalf("logs count: got %d want 1", logCount)
	}

	var ftsBase int
	if err := store.writer.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM logs_fts WHERE logs_fts MATCH ?`,
		"baseline",
	).Scan(&ftsBase); err != nil {
		t.Fatalf("fts baseline: %v", err)
	}
	if ftsBase != 1 {
		t.Fatalf("fts baseline: got %d want 1", ftsBase)
	}

	var ftsReject int
	if err := store.writer.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM logs_fts WHERE logs_fts MATCH ?`,
		"rejected",
	).Scan(&ftsReject); err != nil {
		t.Fatalf("fts rejected: %v", err)
	}
	if ftsReject != 0 {
		t.Fatalf("fts rejected: got %d want 0", ftsReject)
	}

	var wm string
	if err := store.writer.QueryRowContext(ctx,
		`SELECT value FROM metadata WHERE key='watermark'`,
	).Scan(&wm); err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if wm != "1" {
		t.Fatalf("watermark: got %q want \"1\"", wm)
	}

	// Drop trigger, retry commit
	_, err = store.writer.ExecContext(ctx, `DROP TRIGGER abort_meta`)
	if err != nil {
		t.Fatalf("drop trigger: %v", err)
	}

	r3, err := store.Commit(ctx, scope, b2)
	if err != nil {
		t.Fatalf("retry commit: %v", err)
	}
	if r3.Accepted != 1 {
		t.Fatalf("retry accepted: got %d want 1", r3.Accepted)
	}

	var logCount2 int
	if err := store.writer.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM logs WHERE workspace_id=? AND source_id=?`,
		scope.WorkspaceID, scope.SourceID,
	).Scan(&logCount2); err != nil {
		t.Fatalf("count logs 2: %v", err)
	}
	if logCount2 != 2 {
		t.Fatalf("logs count 2: got %d want 2", logCount2)
	}

	var wm2 string
	if err := store.writer.QueryRowContext(ctx,
		`SELECT value FROM metadata WHERE key='watermark'`,
	).Scan(&wm2); err != nil {
		t.Fatalf("watermark 2: %v", err)
	}
	if wm2 != "2" {
		t.Fatalf("watermark 2: got %q want \"2\"", wm2)
	}
}

func TestFutureSchema(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dbPath := filepath.Join(state, "future.db")

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	ctx := context.Background()
	_, err = store.writer.ExecContext(ctx, `PRAGMA user_version = 2`)
	if err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, err = Open(dbPath)
	if !errors.Is(err, ErrOpen) {
		t.Fatalf("expected ErrOpen, got: %v", err)
	}

	db, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open ro: %v", err)
	}
	defer db.Close()
	var uv int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&uv); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	if uv != 2 {
		t.Fatalf("user_version: got %d want 2", uv)
	}
}
