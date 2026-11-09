package sqlite

import (
	"context"
	"errors"
	"fmt"
)

var ErrVersionMismatch = errors.New("schema version mismatch")

// IndexLogsRetention is the receipt-time index. Schema statements and the
// cursor retention lookup both use this name.
const IndexLogsRetention = "idx_logs_retention"

// logsImmutableTrigger rejects UPDATE. Logs are insert-and-delete only, and
// there is no FTS update trigger. rank=1 integrity-check compares external content.
const logsImmutableTrigger = `CREATE TRIGGER IF NOT EXISTS logs_bu BEFORE UPDATE ON logs BEGIN
	SELECT RAISE(ABORT, 'logs are immutable');
END`

func (s *Store) migrate(ctx context.Context) error {
	var ver int
	if err := s.writer.QueryRowContext(ctx, "PRAGMA user_version").Scan(&ver); err != nil {
		return err
	}
	if ver > 1 {
		return fmt.Errorf("%w: found %d", ErrVersionMismatch, ver)
	}
	if ver == 1 {
		return nil
	}

	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS sources (
			workspace_id TEXT NOT NULL,
			source_id TEXT NOT NULL,
			service TEXT NOT NULL,
			environment TEXT NOT NULL,
			PRIMARY KEY (workspace_id, source_id)
		)`,
		`CREATE TABLE IF NOT EXISTS logs (
			ingest_id INTEGER PRIMARY KEY AUTOINCREMENT,
			workspace_id TEXT NOT NULL,
			source_id TEXT NOT NULL,
			batch_id TEXT NOT NULL,
			event_id TEXT NOT NULL,
			event_time INTEGER NOT NULL,
			received_at INTEGER NOT NULL,
			severity_number INTEGER NOT NULL,
			severity_text TEXT NOT NULL,
			message TEXT NOT NULL,
			attributes TEXT NOT NULL CHECK (json_valid(attributes)),
			trace_id TEXT,
			span_id TEXT,
			FOREIGN KEY (workspace_id, source_id) REFERENCES sources(workspace_id, source_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_time ON logs(workspace_id, source_id, event_time DESC, ingest_id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_ws_time ON logs(workspace_id, event_time DESC, ingest_id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_ws_ingest ON logs(workspace_id, ingest_id)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_received ON logs(workspace_id, source_id, received_at)`,
		`CREATE INDEX IF NOT EXISTS ` + IndexLogsRetention + ` ON logs(received_at, ingest_id)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS logs_fts USING fts5(
			message,
			content='logs',
			content_rowid='ingest_id',
			tokenize='unicode61 remove_diacritics 1'
		)`,
		`CREATE TRIGGER IF NOT EXISTS logs_ai AFTER INSERT ON logs BEGIN
			INSERT INTO logs_fts(rowid, message) VALUES (new.ingest_id, new.message);
		END`,
		`CREATE TRIGGER IF NOT EXISTS logs_ad AFTER DELETE ON logs BEGIN
			INSERT INTO logs_fts(logs_fts, rowid, message) VALUES('delete', old.ingest_id, old.message);
		END`,
		logsImmutableTrigger,
		`CREATE TABLE IF NOT EXISTS receipts (
			workspace_id TEXT NOT NULL,
			source_id TEXT NOT NULL,
			batch_id TEXT NOT NULL,
			content_hash TEXT NOT NULL,
			accepted INTEGER NOT NULL,
			first_ingest_id INTEGER NOT NULL,
			last_ingest_id INTEGER NOT NULL,
			received_at INTEGER NOT NULL,
			replay_until INTEGER NOT NULL,
			body TEXT NOT NULL,
			PRIMARY KEY (workspace_id, source_id, batch_id),
			FOREIGN KEY (workspace_id, source_id) REFERENCES sources(workspace_id, source_id)
		)`,
		`CREATE TABLE IF NOT EXISTS metadata (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`INSERT OR IGNORE INTO metadata(key, value) VALUES('watermark', '0')`,
		`INSERT OR IGNORE INTO sources(workspace_id, source_id, service, environment) VALUES
			('ws_demo_a', 'src_demo_api', 'demo-api', 'staging'),
			('ws_demo_b', 'src_demo_worker', 'demo-worker', 'staging')`,
		`PRAGMA user_version = 1`,
	}

	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}
