package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestNativeFormPolicyRoundTrip(t *testing.T) {
	store, ts, readTok, writeTok, _ := newTestApp(t)
	_ = store

	now := time.Now().UTC().Truncate(time.Microsecond)
	batch := validBatch(t, 1)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/experimental/skeleton/batches", strings.NewReader(string(batch)))
	req.Header.Set("Authorization", "Bearer "+writeTok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ingest status: %d", resp.StatusCode)
	}

	client, csrf := login(t, ts, readTok)
	origin := "http://" + ts.Listener.Addr().String()

	from := now.Add(-time.Minute).Format(time.RFC3339Nano)
	to := now.Add(time.Minute).Format(time.RFC3339Nano)

	vals := url.Values{}
	vals.Set("csrf", csrf)
	vals.Set("source", "src_demo_api")
	vals.Set("from", from)
	vals.Set("to", to)
	vals.Set("severity", "1")
	vals.Set("generation", "2")
	vals.Set("sequence", "2")
	vals.Set("term", "synthetic")

	code, hdr, body := postForm(t, client, origin, "/investigate/search", vals, false)
	if code != http.StatusOK {
		t.Fatalf("search status: %d", code)
	}
	assertPagePolicy(t, hdr, body)
	assertNativeForm(t, body, "/investigate/detail")

	vals.Set("id", "1")
	code, hdr, body = postForm(t, client, origin, "/investigate/detail", vals, false)
	if code != http.StatusOK {
		t.Fatalf("detail status: %d", code)
	}
	assertPagePolicy(t, hdr, body)
	assertNativeForm(t, body, "/investigate/search")

	vals.Del("id")
	vals.Set("origin", "1")
	code, hdr, body = postForm(t, client, origin, "/investigate/search", vals, false)
	if code != http.StatusOK {
		t.Fatalf("back search status: %d", code)
	}
	assertPagePolicy(t, hdr, body)
	assertNativeForm(t, body, "/investigate/detail")
}

func assertPagePolicy(t *testing.T, hdr http.Header, body []byte) {
	t.Helper()
	if got := hdr.Get("Referrer-Policy"); got != "strict-origin" {
		t.Errorf("Referrer-Policy header = %q, want strict-origin", got)
	}
	if !strings.Contains(string(body), `<meta name="referrer" content="strict-origin">`) {
		t.Error("missing meta referrer strict-origin")
	}
	if strings.Contains(string(body), `<meta name="referrer" content="no-referrer">`) {
		t.Error("unexpected no-referrer meta")
	}
	if !strings.Contains(string(body), "<!DOCTYPE html>") {
		t.Error("missing DOCTYPE")
	}
}

func assertNativeForm(t *testing.T, body []byte, action string) {
	t.Helper()
	if !strings.Contains(string(body), `action="`+action+`"`) {
		t.Errorf("missing form action %s", action)
	}
}
