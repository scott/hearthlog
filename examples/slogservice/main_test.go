package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"example.invalid/hearthlog/internal/emitter"
)

// fakeIngest is a minimal synthetic NATIVE-V1 receiver for the example.
func fakeIngest(t *testing.T, token string, status int) (*httptest.Server, *[][]byte) {
	var mu sync.Mutex
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		var b struct {
			BatchID string            `json:"batch_id"`
			Events  []json.RawMessage `json:"events"`
		}
		_ = json.Unmarshal(body, &b)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"version": 1, "batch_id": b.BatchID, "accepted": len(b.Events)})
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

func writeFile(t *testing.T, name, content string) string {
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExampleEnabledDeliversAndReports(t *testing.T) {
	srv, bodies := fakeIngest(t, "synthetic-token-a", http.StatusOK)
	var out, errOut bytes.Buffer
	code := run([]string{"-endpoint", srv.URL + emitter.NativePath, "-token-file", writeFile(t, "tok", "synthetic-token-a\n"), "-events", "30"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out.String(), errOut.String())
	}
	var res map[string]any
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res["acked"] != float64(31) || res["sdk_version"] != emitter.SDKVersion || res["shutdown_unsent"] != float64(0) {
		t.Fatalf("result %v", res)
	}
	if !strings.Contains(errOut.String(), "synthetic request handled") {
		t.Fatal("prior local logging lost")
	}
	if strings.Contains(string(bytes.Join(*bodies, nil)), "synthetic-token-a") {
		t.Fatal("token on wire body")
	}
}

func TestExampleDisabledKeepsLocalLogging(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"-events", "3"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), `"remote":"disabled"`) || strings.Count(errOut.String(), "synthetic request handled") != 3 {
		t.Fatalf("out=%s err=%s", out.String(), errOut.String())
	}
}

// TestExampleRevokedTokenStopsAndCounts: after rotation the old token gets
// 401; the SDK stops sending, counts the loss and local logging continues.
func TestExampleRevokedTokenStopsAndCounts(t *testing.T) {
	srv, _ := fakeIngest(t, "synthetic-token-new", http.StatusOK)
	var out, errOut bytes.Buffer
	code := run([]string{"-endpoint", srv.URL + emitter.NativePath, "-token-file", writeFile(t, "tok", "synthetic-token-old"), "-events", "5"}, &out, &errOut)
	if code != 1 || !strings.Contains(out.String(), `"unauthorized"`) || !strings.Contains(errOut.String(), "synthetic request handled") {
		t.Fatalf("exit %d out=%s", code, out.String())
	}
}

func TestExampleBadConfigFallsBackToLocal(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"-endpoint", "http://198.51.100.7:1/api/v1/batches", "-token-file", writeFile(t, "tok", "x"), "-events", "1"}, &out, &errOut)
	if code != 0 || !strings.Contains(errOut.String(), "remote logging disabled") || !strings.Contains(out.String(), "disabled") {
		t.Fatalf("exit %d out=%s err=%s", code, out.String(), errOut.String())
	}
}
