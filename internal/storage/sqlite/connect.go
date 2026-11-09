package sqlite

import (
	"context"
	"database/sql"
	"net/url"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func connect(dsn string, read bool) (*sql.DB, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, err
	}

	params := u.Query()
	params.Set("_journal_mode", "WAL")
	params.Set("_synchronous", "FULL")
	params.Set("_foreign_keys", "on")
	params.Set("_busy_timeout", "250")
	params.Set("_auto_vacuum", "incremental")
	params.Set("_locking_mode", "normal")
	params.Set("_txlock", "immediate")

	if read {
		params.Set("mode", "ro")
		params.Set("_query_only", "on")
		params.Del("_auto_vacuum")
		params.Del("_journal_mode")
	}

	u.RawQuery = params.Encode()

	db, err := sql.Open(sqliteDriver, u.String())
	if err != nil {
		return nil, err
	}

	if read {
		db.SetMaxOpenConns(maxReaders)
		db.SetMaxIdleConns(maxReaders)
	} else {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}
