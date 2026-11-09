package server

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestJSONCharsetAccepted(t *testing.T) {
	_, ts, _, writeTok, _ := newTestApp(t)
	native := nativeBody(t, "00000000-0000-4000-8000-0000000000c1", 1, "charset synthetic")
	skeleton := validBatch(t, 1)

	cases := []struct {
		name  string
		path  string
		body  []byte
		ctype string
		want  int
	}{
		{"native charset", "/api/v1/batches", native, "application/json; charset=utf-8", http.StatusOK},
		{"native charset case", "/api/v1/batches", nativeBody(t, "00000000-0000-4000-8000-0000000000c2", 1, "charset synthetic"), "Application/JSON; charset=UTF-8", http.StatusOK},
		{"native plain", "/api/v1/batches", native, "text/plain", http.StatusUnsupportedMediaType},
		{"native xml", "/api/v1/batches", native, "application/xml", http.StatusUnsupportedMediaType},
		{"skeleton charset", "/api/experimental/skeleton/batches", skeleton, "application/json; charset=utf-8", http.StatusOK},
		{"skeleton plain", "/api/experimental/skeleton/batches", skeleton, "text/plain; charset=utf-8", http.StatusUnsupportedMediaType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, ts.URL+tc.path, bytes.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", tc.ctype)
			req.Header.Set("Authorization", "Bearer "+writeTok)
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status %d want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

// The HTTP concurrency cap and the writer queue are separate. This test fills
// only the HTTP cap; the writer queue is empty and still never sees the body.
func TestHTTPLimitSeparateFromWriterQueue(t *testing.T) {
	if httpTimeout != 5*time.Second {
		t.Fatal(httpTimeout)
	}
	store, ts, _, writeTok, _ := newTestApp(t)
	n := 0
	for n < maxConcurrent {
		select {
		case sem64 <- struct{}{}:
			n++
		default:
			t.Fatalf("semaphore filled %d", n)
		}
	}
	t.Cleanup(func() {
		for i := 0; i < n; i++ {
			<-sem64
		}
	})
	body := nativeBody(t, "00000000-0000-4000-8000-0000000000d1", 1, "limit synthetic")
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/batches", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+writeTok)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") != "5" {
		t.Fatalf("retry-after %q", resp.Header.Get("Retry-After"))
	}
	wm, err := store.Watermark(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if wm != 0 {
		t.Fatalf("writer accepted a request the HTTP limit rejected: watermark %d", wm)
	}
}

func TestRowsClosedBeforeRender(t *testing.T) {
	store, ts, readTok, _, _ := newTestApp(t)
	q := seedResultBytes(t, store, 1, "closed synthetic boundedmarker")
	client, csrf := login(t, ts, readTok)
	var inUse int
	beforeRender.Store(func() { inUse = store.ReaderInUse() })
	t.Cleanup(func() { beforeRender.Store(func() {}) })
	code, _, body := postForm(t, client, ts.URL, "/investigate/search", resultForm(csrf, q), false)
	if code != http.StatusOK || !strings.Contains(string(body), "closed synthetic") {
		t.Fatalf("status %d", code)
	}
	if inUse != 0 {
		t.Fatalf("reader still checked out at render: %d", inUse)
	}
}

func TestInvalidCursorRejected(t *testing.T) {
	store, ts, readTok, _, _ := newTestApp(t)
	q := seedResultBytes(t, store, 1, "cursor synthetic")
	client, csrf := login(t, ts, readTok)
	vals := resultForm(csrf, q)
	vals.Set("cursor", "tampered.cursor")
	code, _, body := postForm(t, client, ts.URL, "/investigate/search", vals, false)
	if code != http.StatusBadRequest || !bytes.Contains(body, []byte("Query is invalid")) {
		t.Fatalf("status %d body %s", code, body)
	}
	if bytes.Contains(body, []byte("tampered.cursor")) {
		t.Fatal("invalid cursor echoed")
	}
}
