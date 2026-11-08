package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/emitter"
	"example.invalid/hearthlog/internal/model"
	"example.invalid/hearthlog/internal/storage/sqlite"
)

func TestPipeline100(t *testing.T) {
	canary := "canary_" + randHex(t, 16)
	var mu sync.Mutex
	var wireBodies [][]byte

	var diagnostics bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&diagnostics, nil)))
	defer func() { slog.SetDefault(old) }()

	store, ts, readTok, writeTok, dbPath := newTestApp(t, func(b []byte) {
		mu.Lock()
		cp := make([]byte, len(b))
		copy(cp, b)
		wireBodies = append(wireBodies, cp)
		mu.Unlock()
	})

	// Positive control: canary is non-empty and self-contained
	if !bytes.Contains([]byte(canary), []byte(canary)) {
		t.Fatal("positive control failed")
	}

	h, err := emitter.New(ts.URL+"/api/experimental/skeleton/batches", writeTok, []string{canary})
	if err != nil {
		t.Fatalf("emitter.New: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	for i := 0; i < 100; i++ {
		slog.New(h).Info(fmt.Sprintf("slice02 synthetic event %d %s", i, canary),
			"ordinal", int64(i),
			"large", int64(9007199254740993),
			"real", 1.0,
			"bool", true,
			"password", canary,
		)
	}
	slog.Info("slice02 diagnostics marker")

	flushCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := h.Flush(flushCtx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := h.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Assert wire bodies: exactly 100, none contain canary
	mu.Lock()
	if len(wireBodies) != 100 {
		t.Fatalf("wire bodies: got %d want 100", len(wireBodies))
	}
	for i, b := range wireBodies {
		if bytes.Contains(b, []byte(canary)) {
			t.Fatalf("wire body %d contains canary", i)
		}
	}
	mu.Unlock()

	// Search: 100 rows, typed JSON correct
	q := model.Query{
		Sources:     []string{"src_demo_api"},
		From:        now.Add(-time.Minute).Format(time.RFC3339Nano),
		To:          now.Add(time.Minute).Format(time.RFC3339Nano),
		MinSeverity: 1,
		Terms:       []string{"synthetic"},
		Limit:       200,
	}
	rows, err := store.Search(context.Background(), model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, q)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(rows) != 100 {
		t.Fatalf("rows: got %d want 100", len(rows))
	}

	// Verify typed JSON on first row
	dec := json.NewDecoder(bytes.NewReader(rows[0].Attributes))
	dec.UseNumber()
	var attrs map[string]any
	if err := dec.Decode(&attrs); err != nil {
		t.Fatalf("unmarshal attrs: %v", err)
	}
	if v, ok := attrs["large"]; !ok || v != json.Number("9007199254740993") {
		t.Fatalf("large: %v", v)
	}
	if v, ok := attrs["real"]; !ok || v != json.Number("1.0") {
		t.Fatalf("real: %v", v)
	}
	if v, ok := attrs["bool"]; !ok || v != true {
		t.Fatalf("bool: %v", v)
	}
	if _, ok := attrs["password"]; ok {
		t.Fatal("password key present in stored attrs")
	}

	// FTS canary search returns 0
	qCanary := model.Query{
		Sources:     []string{"src_demo_api"},
		From:        now.Add(-time.Minute).Format(time.RFC3339Nano),
		To:          now.Add(time.Minute).Format(time.RFC3339Nano),
		MinSeverity: 1,
		Terms:       []string{canary},
		Limit:       200,
	}
	rowsC, err := store.Search(context.Background(), model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, qCanary)
	if err != nil {
		t.Fatalf("canary search: %v", err)
	}
	if len(rowsC) != 0 {
		t.Fatalf("canary FTS: got %d want 0", len(rowsC))
	}

	checkCanarySurfaces(t, store, ts, readTok, dbPath, canary, q)

	// Restart: close, reopen same path, assert 100
	store.Close()
	store2, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()

	rows2, err := store2.Search(context.Background(), model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, q)
	if err != nil {
		t.Fatalf("restart search: %v", err)
	}
	if len(rows2) != 100 {
		t.Fatalf("restart rows: got %d want 100", len(rows2))
	}

	diagBytes := diagnostics.Bytes()
	if !bytes.Contains(diagBytes, []byte("slice02 diagnostics marker")) {
		t.Fatal("diagnostics missing positive marker")
	}
	if bytes.Contains(diagBytes, []byte(canary)) {
		t.Fatal("diagnostics contains canary")
	}
}
