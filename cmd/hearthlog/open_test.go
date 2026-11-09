package main

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"example.invalid/hearthlog/internal/storage/sqlite"
)

func TestOlderSliceOpenMessage(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logOpenFailure(logger, sqlite.ErrOlderSlice)
	got := buf.String()
	if !strings.Contains(got, "database is from an older slice and cannot be opened; start the service with a new -state directory") {
		t.Fatalf("log: %s", got)
	}
	buf.Reset()
	logOpenFailure(logger, errors.New("other"))
	if strings.Contains(buf.String(), "older slice") {
		t.Fatalf("unrelated open: %s", buf.String())
	}
}
