package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var ErrProbe = errors.New("probe failed")

func checkPragmas(ctx context.Context, db *sql.DB, isReader bool) (map[string]string, error) {
	out := make(map[string]string)
	checks := []struct{ q, k, want string }{
		{"PRAGMA journal_mode", "journal_mode", "wal"},
		{"PRAGMA synchronous", "synchronous", "2"},
		{"PRAGMA foreign_keys", "foreign_keys", "1"},
		{"PRAGMA busy_timeout", "busy_timeout", "250"},
		{"PRAGMA locking_mode", "locking_mode", "normal"},
		{"PRAGMA auto_vacuum", "auto_vacuum", "2"},
		{"PRAGMA wal_autocheckpoint", "wal_autocheckpoint", "1000"},
	}
	for _, c := range checks {
		var val string
		if err := db.QueryRowContext(ctx, c.q).Scan(&val); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrProbe, c.k, err)
		}
		if val != c.want {
			return nil, fmt.Errorf("%w: %s expected %s got %s", ErrProbe, c.k, c.want, val)
		}
		out[c.k] = val
	}
	if isReader {
		var ro int
		if err := db.QueryRowContext(ctx, "PRAGMA query_only").Scan(&ro); err != nil {
			return nil, fmt.Errorf("%w: query_only: %v", ErrProbe, err)
		}
		if ro != 1 {
			return nil, fmt.Errorf("%w: reader query_only expected 1 got %d", ErrProbe, ro)
		}
		out["query_only"] = "1"
		if _, err := db.ExecContext(ctx, "UPDATE metadata SET value=value WHERE key='watermark'"); err == nil {
			return nil, fmt.Errorf("%w: reader allowed write", ErrProbe)
		}
	} else {
		var ro int
		if err := db.QueryRowContext(ctx, "PRAGMA query_only").Scan(&ro); err != nil {
			return nil, fmt.Errorf("%w: query_only: %v", ErrProbe, err)
		}
		if ro != 0 {
			return nil, fmt.Errorf("%w: writer query_only expected 0 got %d", ErrProbe, ro)
		}
		out["query_only"] = "0"
	}
	return out, nil
}
