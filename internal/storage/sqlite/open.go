package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

var ErrOpen = errors.New("open failed")

// ErrOlderSlice is returned when the file was written by a slice that has no
// receipts table. The operator starts a new state directory. There is no
// in-place upgrade.
var ErrOlderSlice = errors.New("database is from an older slice and cannot be opened; start the service with a new -state directory")

type Store struct {
	writer    *sql.DB
	reader    *sql.DB
	lock      *os.File
	sched     *writeSched
	clock     atomic.Value // func() time.Time
	fault     atomic.Int32
	closeOnce sync.Once
	closeErr  error
}

func Open(path string) (*Store, error) {
	f, dsn, err := prepareFile(path)
	if err != nil {
		return nil, ErrOpen
	}

	w, err := connect(dsn, false)
	if err != nil {
		f.Close()
		return nil, ErrOpen
	}

	s := &Store{writer: w, lock: f}
	s.clock.Store(func() time.Time { return time.Now() })
	if err := s.migrate(context.Background()); err != nil {
		s.Close()
		return nil, ErrOpen
	}
	if err := s.ensureNativeSchema(context.Background()); err != nil {
		s.Close()
		if errors.Is(err, ErrOlderSlice) {
			return nil, ErrOlderSlice
		}
		return nil, ErrOpen
	}
	if err := s.ensureSearchSchema(context.Background()); err != nil {
		s.Close()
		return nil, ErrOpen
	}

	r, err := connect(dsn, true)
	if err != nil {
		s.Close()
		return nil, ErrOpen
	}
	s.reader = r

	if err := s.Probe(context.Background()); err != nil {
		s.Close()
		return nil, ErrOpen
	}
	s.startSched()
	return s, nil
}

func (s *Store) ensureNativeSchema(ctx context.Context) error {
	var n int
	if err := s.writer.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='receipts'`).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return ErrOlderSlice
	}
	var wm string
	if err := s.writer.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='watermark'`).Scan(&wm); err != nil {
		return err
	}
	id, err := strconv.ParseInt(wm, 10, 64)
	if err != nil || id < 0 {
		return err
	}
	return nil
}

func (s *Store) ensureSearchSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE INDEX IF NOT EXISTS idx_logs_time ON logs(workspace_id, source_id, event_time DESC, ingest_id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_ws_time ON logs(workspace_id, event_time DESC, ingest_id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_ws_ingest ON logs(workspace_id, ingest_id)`,
		`CREATE INDEX IF NOT EXISTS idx_logs_retention ON logs(received_at, ingest_id)`,
		logsImmutableTrigger,
		`INSERT OR IGNORE INTO metadata(key, value) VALUES('cursor_epoch', '1')`,
	}
	for _, stmt := range stmts {
		if _, err := s.writer.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	var n int
	if err := s.writer.QueryRowContext(ctx, `SELECT COUNT(*) FROM metadata WHERE key='cursor_mac'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		var buf [32]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return err
		}
		if _, err := s.writer.ExecContext(ctx, `INSERT INTO metadata(key, value) VALUES('cursor_mac', ?)`, hex.EncodeToString(buf[:])); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.close()
	})
	return s.closeErr
}

func (s *Store) close() error {
	if s.sched != nil {
		s.sched.stop()
	}
	var firstErr error
	if s.reader != nil {
		if err := s.reader.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.writer != nil {
		if err := s.writer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.lock != nil {
		if err := s.lock.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
