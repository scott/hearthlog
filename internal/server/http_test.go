package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func TestIngestAuth(t *testing.T) {
	_, ts, readTok, writeTok, _ := newTestApp(t)

	// Unknown token -> 401
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/experimental/skeleton/batches", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+randHex(t, 32))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown token status: %d", resp.StatusCode)
	}

	// Read token on write endpoint -> 401
	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/experimental/skeleton/batches", strings.NewReader("{}"))
	req2.Header.Set("Authorization", "Bearer "+readTok)
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("read token status: %d", resp2.StatusCode)
	}

	// Valid write -> 200, commit before ack
	batch := validBatch(t, 1)
	req3, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/experimental/skeleton/batches", strings.NewReader(string(batch)))
	req3.Header.Set("Authorization", "Bearer "+writeTok)
	req3.Header.Set("Content-Type", "application/json")
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	body, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("valid write status: %d body: %s", resp3.StatusCode, body)
	}
	var receipt model.Receipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		t.Fatalf("unmarshal receipt: %v", err)
	}
	if receipt.Accepted != 1 {
		t.Fatalf("accepted: %d", receipt.Accepted)
	}
}

func TestReadAuth(t *testing.T) {
	_, ts, readTok, _, _ := newTestApp(t)

	// Unauthenticated GET /investigate -> 401
	resp, err := http.Get(ts.URL + "/investigate")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth status: %d", resp.StatusCode)
	}

	// Writer token login -> 401
	_, ts2, _, writeTok, _ := newTestApp(t)
	resp2, err := loginPost(http.DefaultClient, ts2.URL+"/login", "application/x-www-form-urlencoded", strings.NewReader("token="+writeTok))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("writer login status: %d", resp2.StatusCode)
	}

	// Unknown reader -> 401
	resp3, err := loginPost(http.DefaultClient, ts.URL+"/login", "application/x-www-form-urlencoded", strings.NewReader("token="+randHex(t, 32)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown reader status: %d", resp3.StatusCode)
	}

	_ = readTok
}

func TestDBFailure(t *testing.T) {
	store, ts, _, writeTok, _ := newTestApp(t)
	store.Close()

	batch := validBatch(t, 1)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/experimental/skeleton/batches", strings.NewReader(string(batch)))
	req.Header.Set("Authorization", "Bearer "+writeTok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("closed db status: %d", resp.StatusCode)
	}
}

func TestSearchFullPage(t *testing.T) {
	_, ts, readTok, writeTok, _ := newTestApp(t)

	batch := validBatch(t, 1)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/experimental/skeleton/batches", strings.NewReader(string(batch)))
	req.Header.Set("Authorization", "Bearer "+writeTok)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()

	client, csrf := login(t, ts, readTok)
	now := time.Now().UTC().Truncate(time.Microsecond)
	vals := url.Values{
		"csrf":       {csrf},
		"source":     {"src_demo_api"},
		"from":       {now.Add(-time.Minute).Format(time.RFC3339Nano)},
		"to":         {now.Add(time.Minute).Format(time.RFC3339Nano)},
		"severity":   {"1"},
		"term":       {"synthetic"},
		"generation": {"1"},
		"sequence":   {"1"},
	}
	code, hdr, body := postForm(t, client, "http://"+ts.Listener.Addr().String(), "/investigate/search", vals, false)
	if code != http.StatusOK {
		t.Fatalf("search status: %d", code)
	}
	if hdr.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("content type: %s", hdr.Get("Content-Type"))
	}
	if !strings.Contains(string(body), "<!DOCTYPE html>") {
		t.Fatal("not full page")
	}
}
