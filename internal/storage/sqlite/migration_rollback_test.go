package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestMigrationRollback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dbPath := filepath.Join(state, "mig.db")

	// Create fixture: metadata table without value column, version 0
	raw, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	ctx := context.Background()
	if _, err := raw.ExecContext(ctx, `PRAGMA auto_vacuum = 2`); err != nil {
		raw.Close()
		t.Fatalf("set auto_vacuum: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `CREATE TABLE metadata (key TEXT PRIMARY KEY)`); err != nil {
		raw.Close()
		t.Fatalf("create metadata: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `PRAGMA user_version = 0`); err != nil {
		raw.Close()
		t.Fatalf("set version: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	if err := os.Chmod(dbPath, 0600); err != nil {
		t.Fatalf("chmod db: %v", err)
	}

	// Open must fail during migration (INSERT into metadata fails due to missing column)
	st, err := Open(dbPath)
	if err == nil {
		st.Close()
		t.Fatalf("expected ErrOpen, got nil")
	}
	if !errors.Is(err, ErrOpen) {
		t.Fatalf("expected ErrOpen, got: %v", err)
	}

	// Verify rollback: metadata remains, version 0, other tables absent
	raw2, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("reopen raw: %v", err)
	}
	defer raw2.Close()

	var ver int
	if err := raw2.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&ver); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	if ver != 0 {
		t.Fatalf("version: got %d want 0", ver)
	}

	var metaCount int
	if err := raw2.QueryRowContext(ctx, `SELECT COUNT(*) FROM metadata`).Scan(&metaCount); err != nil {
		t.Fatalf("count metadata: %v", err)
	}
	if metaCount != 0 {
		t.Fatalf("metadata count: got %d want 0", metaCount)
	}

	for _, tbl := range []string{"sources", "logs", "logs_fts"} {
		var cnt int
		if err := raw2.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&cnt); err != nil {
			t.Fatalf("check %s: %v", tbl, err)
		}
		if cnt != 0 {
			t.Fatalf("%s should not exist", tbl)
		}
	}

	// Positive control: repair fixture, Open succeeds
	if _, err := raw2.ExecContext(ctx, `ALTER TABLE metadata ADD COLUMN value TEXT NOT NULL`); err != nil {
		t.Fatalf("alter: %v", err)
	}
	if err := raw2.Close(); err != nil {
		t.Fatalf("close raw2: %v", err)
	}

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open after repair: %v", err)
	}
	defer store.Close()

	var ver2 int
	if err := store.writer.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&ver2); err != nil {
		t.Fatalf("pragma 2: %v", err)
	}
	if ver2 != 1 {
		t.Fatalf("version 2: got %d want 1", ver2)
	}
}
