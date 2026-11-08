package server

import (
	"bytes"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"example.invalid/hearthlog/internal/model"
	"example.invalid/hearthlog/internal/storage/sqlite"
)

func checkCanarySurfaces(t *testing.T, store *sqlite.Store, ts *httptest.Server, readTok, dbPath, canary string, q model.Query) {
	t.Helper()

	rows, err := store.Search(t.Context(), model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, q)
	if err != nil {
		t.Fatalf("canary search: %v", err)
	}
	if len(rows) != 100 {
		t.Fatalf("canary rows: got %d want 100", len(rows))
	}

	for i, r := range rows {
		if strings.Contains(r.Message, canary) {
			t.Fatalf("row %d message contains canary", i)
		}
		if bytes.Contains(r.Attributes, []byte(canary)) {
			t.Fatalf("row %d attributes contain canary", i)
		}
	}

	client, csrf := login(t, ts, readTok)

	positive := "slice02 synthetic event"
	paths := []string{"/investigate/search", "/investigate/detail"}
	for _, path := range paths {
		vals := resultForm(csrf, q)
		if path == "/investigate/detail" {
			vals.Set("id", rows[0].IngestID)
		}
		for _, hx := range []bool{false, true} {
			code, _, body := postForm(t, client, "http://"+ts.Listener.Addr().String(), path, vals, hx)
			if code != 200 {
				t.Fatalf("path %s hx=%v status: %d", path, hx, code)
			}
			if !bytes.Contains(body, []byte(positive)) {
				t.Fatalf("path %s hx=%v missing positive marker", path, hx)
			}
			if bytes.Contains(body, []byte(canary)) {
				t.Fatalf("path %s hx=%v contains canary", path, hx)
			}
		}
	}

	dbData, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read main db: %v", err)
	}
	if bytes.Contains(dbData, []byte(canary)) {
		t.Fatal("main db contains canary")
	}
	foundPositive := bytes.Contains(dbData, []byte(positive))

	for _, suffix := range []string{"-wal", "-shm"} {
		f := dbPath + suffix
		data, err := os.ReadFile(f)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read %s: %v", suffix, err)
		}
		if bytes.Contains(data, []byte(canary)) {
			t.Fatalf("file %s contains canary", suffix)
		}
		if bytes.Contains(data, []byte(positive)) {
			foundPositive = true
		}
	}
	if !foundPositive {
		t.Fatal("combined main db/wal missing positive marker")
	}
}
