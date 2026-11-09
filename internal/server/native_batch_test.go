package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
	_ "github.com/mattn/go-sqlite3"
)

func nativeBody(t *testing.T, batchID string, n int, msg string) []byte {
	t.Helper()
	when := time.Now().UTC().Truncate(time.Second)
	events := make([]model.Event, n)
	for i := range events {
		events[i] = model.Event{
			EventID:        eventSerial(i + 1),
			EventTime:      when.Format(time.RFC3339),
			SeverityNumber: 9,
			SeverityText:   "INFO",
			Message:        msg,
			Attributes:     json.RawMessage(`{"a":1,"b":true}`),
		}
	}
	raw, err := json.Marshal(model.Batch{Version: model.NativeVersion, BatchID: batchID, Events: events})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func eventSerial(i int) string {
	const hexdigits = "0123456789abcdef"
	var tail [12]byte
	for k := 11; k >= 0; k-- {
		tail[k] = hexdigits[i%16]
		i /= 16
	}
	return "00000000-0000-4000-8000-" + string(tail[:])
}

func postNative(t *testing.T, tsURL, token string, body []byte, enc string, client *http.Client) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, tsURL+"/api/v1/batches", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if enc != "" {
		req.Header.Set("Content-Encoding", enc)
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, got
}

func TestNativeBatchReplayAndConflict(t *testing.T) {
	store, ts, _, writeTok, _ := newTestApp(t)
	raw := nativeBody(t, "00000000-0000-4000-8000-0000000000aa", 3, "native synthetic")
	status, _, body := postNative(t, ts.URL, writeTok, raw, "", ts.Client())
	if status != http.StatusOK {
		t.Fatalf("status %d body %s", status, body)
	}
	rec, err := model.ParseNativeReceipt(body)
	if err != nil {
		t.Fatalf("receipt: %v body %s", err, body)
	}
	if rec.Accepted != 3 || rec.BatchID != "00000000-0000-4000-8000-0000000000aa" {
		t.Fatalf("receipt %+v", rec)
	}
	if bytes.Contains(body, []byte(`"first_ingest_id":`)) && bytes.Contains(body, []byte(`"first_ingest_id":1`)) {
		t.Fatalf("first_ingest_id encoded as a number: %s", body)
	}
	status, _, again := postNative(t, ts.URL, writeTok, raw, "identity", ts.Client())
	if status != http.StatusOK {
		t.Fatalf("replay %d %s", status, again)
	}
	if !bytes.Equal(body, again) {
		t.Fatalf("replay body changed\n%s\n%s", body, again)
	}
	var changed map[string]any
	if err := json.Unmarshal(raw, &changed); err != nil {
		t.Fatal(err)
	}
	evs := changed["events"].([]any)
	evs[0].(map[string]any)["message"] = "changed synthetic"
	alt, _ := json.Marshal(changed)
	status, _, conflict := postNative(t, ts.URL, writeTok, alt, "", ts.Client())
	if status != http.StatusConflict || !bytes.Contains(conflict, []byte(`"batch_conflict"`)) {
		t.Fatalf("conflict %d %s", status, conflict)
	}
	if logs := countLogs(t, store); logs != 3 {
		t.Fatalf("logs %d", logs)
	}
}

func TestNativeReceiptOnWireTypes(t *testing.T) {
	_, ts, _, writeTok, _ := newTestApp(t)
	raw := nativeBody(t, "00000000-0000-4000-8000-0000000000ab", 1, "typed receipt")
	status, _, body := postNative(t, ts.URL, writeTok, raw, "", ts.Client())
	if status != http.StatusOK {
		t.Fatalf("status %d %s", status, body)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"first_ingest_id", "last_ingest_id", "replay_until"} {
		v := bytes.TrimSpace(fields[k])
		if len(v) == 0 || v[0] != '"' {
			t.Fatalf("%s is not a JSON string: %s", k, fields[k])
		}
	}
}

func TestNativeGzipAndOversize(t *testing.T) {
	store, ts, _, writeTok, _ := newTestApp(t)
	raw := nativeBody(t, "00000000-0000-4000-8000-0000000000ac", 1, "gzip synthetic")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	status, _, body := postNative(t, ts.URL, writeTok, buf.Bytes(), "gzip", ts.Client())
	if status != http.StatusOK {
		t.Fatalf("gzip %d %s", status, body)
	}
	if _, err := model.ParseNativeReceipt(body); err != nil {
		t.Fatal(err)
	}

	status, _, body = postNative(t, ts.URL, writeTok, []byte("not-gzip"), "gzip", ts.Client())
	if status != http.StatusBadRequest {
		t.Fatalf("bad gzip %d %s", status, body)
	}
	status, _, body = postNative(t, ts.URL, writeTok, raw, "br", ts.Client())
	if status != http.StatusUnsupportedMediaType {
		t.Fatalf("encoding %d %s", status, body)
	}

	over := bytes.Repeat([]byte("x"), model.MaxNativeBytes+1)
	status, _, body = postNative(t, ts.URL, writeTok, over, "", ts.Client())
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("raw oversize %d %s", status, body)
	}
	var big bytes.Buffer
	zw = gzip.NewWriter(&big)
	_, _ = zw.Write(over)
	_ = zw.Close()
	status, _, body = postNative(t, ts.URL, writeTok, big.Bytes(), "gzip", ts.Client())
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("decompressed oversize %d %s", status, body)
	}
	fat := bytes.Repeat([]byte{0x1f}, model.MaxNativeBytes+1)
	status, _, body = postNative(t, ts.URL, writeTok, fat, "gzip", ts.Client())
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("compressed oversize %d %s", status, body)
	}
	if logs := countLogs(t, store); logs != 1 {
		t.Fatalf("rejected batches wrote logs=%d", logs)
	}
}

func TestNativeUnauthenticated(t *testing.T) {
	_, ts, _, _, _ := newTestApp(t)
	raw := nativeBody(t, "00000000-0000-4000-8000-0000000000ad", 1, "no token")
	status, _, body := postNative(t, ts.URL, "short", raw, "", ts.Client())
	if status != http.StatusUnauthorized {
		t.Fatalf("status %d %s", status, body)
	}
}

func TestNativeBusy503(t *testing.T) {
	_, ts, _, writeTok, dbPath := newTestApp(t)
	u := url.URL{Scheme: "file", Path: dbPath, RawQuery: "_busy_timeout=0&_txlock=immediate"}
	ext, err := sql.Open("sqlite3", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Close()
	if _, err := ext.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer ext.Exec("ROLLBACK")
	raw := nativeBody(t, "00000000-0000-4000-8000-0000000000ae", 1, "busy synthetic")
	client := &http.Client{Timeout: 8 * time.Second}
	status, hdr, body := postNative(t, ts.URL, writeTok, raw, "", client)
	if status != http.StatusServiceUnavailable || hdr.Get("Retry-After") != "5" {
		t.Fatalf("busy %d retry-after %q body %s", status, hdr.Get("Retry-After"), body)
	}
	if !bytes.Contains(body, []byte(`"busy"`)) && !bytes.Contains(body, []byte(`"storage_unavailable"`)) {
		t.Fatalf("body %s", body)
	}
}

func countLogs(t *testing.T, store interface {
	Search(context.Context, model.Scope, model.Query) ([]model.Record, error)
}) int {
	t.Helper()
	now := time.Now().UTC()
	rows, err := store.Search(context.Background(), model.Scope{WorkspaceID: "ws_demo_a"}, model.Query{
		Sources:     []string{"src_demo_api"},
		From:        now.Add(-time.Hour).Format(time.RFC3339),
		To:          now.Add(time.Hour).Format(time.RFC3339),
		MinSeverity: 1,
		Limit:       200,
	})
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}
