package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

var ErrOpen = errors.New("open failed")

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
		return ErrVersionMismatch
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
