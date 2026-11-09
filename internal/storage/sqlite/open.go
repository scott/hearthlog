package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
)

var ErrOpen = errors.New("open failed")

type Store struct {
	writer *sql.DB
	reader *sql.DB
	lock   *os.File
	admit  chan struct{}
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

	s := &Store{writer: w, lock: f, admit: make(chan struct{}, 64)}
	if err := s.migrate(context.Background()); err != nil {
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

	return s, nil
}

func (s *Store) Close() error {
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
