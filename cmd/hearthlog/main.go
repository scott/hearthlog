package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"example.invalid/hearthlog/internal/server"
	"example.invalid/hearthlog/internal/storage/sqlite"
)

func main() {
	stateDir := flag.String("state", "", "absolute private 0700 state directory outside repo")
	listen := flag.String("listen", "127.0.0.1:8787", "loopback listen address")
	flag.Parse()

	if *stateDir == "" {
		fatal("state directory required")
	}
	abs, err := filepath.Abs(*stateDir)
	if err != nil {
		fatal("state path")
	}
	if !filepath.IsAbs(*stateDir) {
		fatal("state must be absolute")
	}
	if fi, err := os.Lstat(abs); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			fatal("state invalid")
		}
		if fi.Mode().Perm() != 0o700 {
			fatal("state perms")
		}
	} else if !os.IsNotExist(err) {
		fatal("state stat")
	} else if err := os.MkdirAll(abs, 0o700); err != nil {
		fatal("state mkdir")
	}

	host, port, err := net.SplitHostPort(*listen)
	if err != nil {
		fatal("listen format")
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		fatal("listen must be loopback")
	}
	if port == "" {
		fatal("listen port")
	}

	readTok, err := loadOrCreateToken(filepath.Join(abs, "read.token"))
	if err != nil {
		fatal("read token")
	}
	writeTok, err := loadOrCreateToken(filepath.Join(abs, "write.token"))
	if err != nil {
		fatal("write token")
	}

	dbPath := filepath.Join(abs, "hearthlog.db")
	store, err := sqlite.Open(dbPath)
	if err != nil {
		fatal("db open")
	}
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := store.Probe(ctx); err != nil {
		fatal("db probe")
	}

	srv, err := server.New(store, readTok, writeTok, "http://"+*listen)
	if err != nil {
		fatal("server init")
	}

	httpSrv := &http.Server{
		Addr:              *listen,
		Handler:           srv,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		ReadHeaderTimeout: 2 * time.Second,
		IdleTimeout:       15 * time.Second,
		MaxHeaderBytes:    16384,
	}

	go func() {
		slog.Info("ready", "origin", *listen)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal("http serve")
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		fatal("shutdown")
	}
}

func loadOrCreateToken(path string) (string, error) {
	fi, err := os.Lstat(path)
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			return "", errors.New("token file invalid")
		}
		if fi.Mode().Perm() != 0o600 {
			return "", errors.New("token file perms")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if len(data) != 64 {
			return "", errors.New("token length")
		}
		if _, err := hex.DecodeString(string(data)); err != nil {
			return "", errors.New("token hex")
		}
		return string(data), nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(tok); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return tok, nil
}

func fatal(msg string) {
	slog.Error("fatal", "msg", msg)
	os.Exit(1)
}
