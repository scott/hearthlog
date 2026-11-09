package sqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func diagnoseOpen(t *testing.T, path string, origErr error) {
	t.Logf("open failed: %v", origErr)
	f, dsn, err := prepareFile(path)
	if err != nil {
		t.Logf("stage prepareFile: %v", err)
		return
	}
	defer f.Close()
	w, err := connect(dsn, false)
	if err != nil {
		t.Logf("stage connect writer: %v", err)
		return
	}
	w.Close()
	r, err := connect(dsn, true)
	if err != nil {
		t.Logf("stage connect reader: %v", err)
		return
	}
	r.Close()
	t.Logf("all connect stages succeeded; failure likely in migrate or probe")
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestPersistence(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "state")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	pi, _ := os.Stat(parent)
	ci, _ := os.Stat(child)
	t.Logf("parent mode=%o child mode=%o", pi.Mode().Perm(), ci.Mode().Perm())
	dbPath := filepath.Join(child, "test.db")

	store, err := Open(dbPath)
	if err != nil {
		diagnoseOpen(t, dbPath, err)
		t.Fatalf("open: %v", err)
	}

	report, err := store.Report(context.Background())
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	b, _ := json.Marshal(report)
	slog.Info("probe", "report", string(b))

	if _, err := Open(dbPath); err == nil {
		t.Fatal("second open should fail")
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	marker := "slice02sentinel"
	attrs := `{"real":1.0,"int":9007199254740993,"null":null,"bool":true,"str":"hostile <script>"}`

	for i := 0; i < 100; i++ {
		batchID := fmt.Sprintf("%08x-0000-4000-8000-%012d", i, i)
		eventID := fmt.Sprintf("%08x-0000-4000-8000-%012d", i+1000, i)
		msg := fmt.Sprintf("%s event %d", marker, i)

		event := model.Event{
			EventID:        eventID,
			EventTime:      now.Format(time.RFC3339Nano),
			SeverityNumber: 9,
			SeverityText:   "INFO",
			Message:        msg,
			Attributes:     json.RawMessage(attrs),
		}

		raw, _ := json.Marshal(model.Batch{Version: 0, BatchID: batchID, Events: []model.Event{event}})
		validated, err := model.ValidateBatch(raw, now)
		if err != nil {
			t.Fatalf("validate %d: %v", i, err)
		}

		if _, err := store.Commit(context.Background(), model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, validated); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}

	q := model.Query{
		Sources:     []string{"src_demo_api"},
		From:        now.Add(-time.Hour).Format(time.RFC3339),
		To:          now.Add(time.Hour).Format(time.RFC3339),
		MinSeverity: 1,
		Terms:       []string{marker},
		Limit:       200,
	}

	records, err := store.Search(context.Background(), model.Scope{WorkspaceID: "ws_demo_a"}, q)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(records) != 100 {
		t.Fatalf("expected 100, got %d", len(records))
	}

	q.Terms = []string{"nonexistentterm"}
	records, err = store.Search(context.Background(), model.Scope{WorkspaceID: "ws_demo_a"}, q)
	if err != nil {
		t.Fatalf("search nomatch: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected 0, got %d", len(records))
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	store2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()

	q.Terms = []string{marker}
	records, err = store2.Search(context.Background(), model.Scope{WorkspaceID: "ws_demo_a"}, q)
	if err != nil {
		t.Fatalf("search after restart: %v", err)
	}
	if len(records) != 100 {
		t.Fatalf("expected 100 after restart, got %d", len(records))
	}

	detail, err := store2.Detail(context.Background(), model.Scope{WorkspaceID: "ws_demo_a"}, "1")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	decExp := json.NewDecoder(bytes.NewReader([]byte(attrs)))
	decExp.UseNumber()
	var expAny any
	if err := decExp.Decode(&expAny); err != nil {
		t.Fatalf("decode expected attrs: %v", err)
	}
	decAct := json.NewDecoder(bytes.NewReader(detail.Attributes))
	decAct.UseNumber()
	var actAny any
	if err := decAct.Decode(&actAny); err != nil {
		t.Fatalf("decode actual attrs: %v", err)
	}
	if !reflect.DeepEqual(expAny, actAny) {
		t.Fatalf("attrs mismatch: expected %v, got %v", expAny, actAny)
	}

	if _, err := store2.reader.ExecContext(context.Background(), "UPDATE metadata SET value='x' WHERE key='watermark'"); err == nil {
		t.Fatal("reader write should fail")
	}
}
