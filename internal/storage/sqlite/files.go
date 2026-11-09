package sqlite

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
)

var ErrOpenFailed = errors.New("open failed")

func prepareFile(path string) (*os.File, string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, "", ErrOpenFailed
	}

	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, "", ErrOpenFailed
	}

	info, err := os.Stat(dir)
	if err != nil {
		return nil, "", ErrOpenFailed
	}
	if info.IsDir() {
		perm := info.Mode().Perm()
		if perm&0077 != 0 {
			return nil, "", ErrOpenFailed
		}
	}

	lockPath := abs + ".lock"
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, "", ErrOpenFailed
	}
	if err := os.Chmod(lockPath, 0600); err != nil {
		lf.Close()
		return nil, "", ErrOpenFailed
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lf.Close()
		return nil, "", ErrOpenFailed
	}

	db, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		lf.Close()
		return nil, "", ErrOpenFailed
	}
	if err := os.Chmod(abs, 0600); err != nil {
		db.Close()
		lf.Close()
		return nil, "", ErrOpenFailed
	}
	db.Close()

	u := url.URL{Scheme: "file", Path: abs}
	dsn := u.String()
	return lf, dsn, nil
}
