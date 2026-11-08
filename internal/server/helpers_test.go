package server

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
	"example.invalid/hearthlog/internal/storage/sqlite"
)

func newTestApp(t *testing.T, taps ...func([]byte)) (*sqlite.Store, *httptest.Server, string, string, string) {
	t.Helper()

	readTok := randHex(t, 32)
	writeTok := randHex(t, 32)

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	dbPath := filepath.Join(dir, "test.db")

	store, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unexpected request to unstarted server")
	}))
	origin := "http://" + ts.Listener.Addr().String()

	srv, err := New(store, readTok, writeTok, origin)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/experimental/skeleton/batches" {
			if r.Body != nil {
				body, err := io.ReadAll(io.LimitReader(r.Body, 256*1024))
				if err == nil {
					for _, tap := range taps {
						if tap != nil {
							cp := make([]byte, len(body))
							copy(cp, body)
							tap(cp)
						}
					}
					r.Body = io.NopCloser(bytes.NewReader(body))
				}
			}
		}
		srv.ServeHTTP(w, r)
	})
	ts.Start()
	t.Cleanup(ts.Close)

	return store, ts, readTok, writeTok, dbPath
}

func randHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

func login(t *testing.T, ts *httptest.Server, readTok string) (*http.Client, string) {
	t.Helper()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	resp, err := loginPost(client, ts.URL+"/login", "application/x-www-form-urlencoded",
		strings.NewReader("token="+readTok))
	if err != nil {
		t.Fatalf("login post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status: %d", resp.StatusCode)
	}

	resp2, err := client.Get(ts.URL + "/investigate")
	if err != nil {
		t.Fatalf("investigate get: %v", err)
	}
	body, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("investigate status: %d", resp2.StatusCode)
	}

	re := regexp.MustCompile(`name="csrf" value="([0-9a-f]{64})"`)
	m := re.FindSubmatch(body)
	if m == nil {
		t.Fatal("csrf not found in page")
	}
	return client, string(m[1])
}

func postForm(t *testing.T, client *http.Client, origin, path string, values url.Values, hx bool) (int, http.Header, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, origin+path, strings.NewReader(values.Encode()))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", origin)
	if hx {
		req.Header.Set("HX-Request", "true")
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, body
}

func loginPost(client *http.Client, target, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Origin", req.URL.Scheme+"://"+req.URL.Host)
	return client.Do(req)
}

func validBatch(t *testing.T, n int) []byte {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	events := make([]model.Event, n)
	for i := range events {
		events[i] = model.Event{
			EventID:        fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1),
			EventTime:      now.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano),
			SeverityNumber: 9,
			SeverityText:   "INFO",
			Message:        fmt.Sprintf("synthetic event %d", i),
			Attributes:     json.RawMessage(`{"task":{"attempt":1},"ok":true}`),
		}
	}
	b := model.Batch{Version: 0, BatchID: "00000000-0000-4000-8000-000000000001", Events: events}
	raw, _ := json.Marshal(b)
	return raw
}
