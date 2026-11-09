package server

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func TestFragments(t *testing.T) {
	store, ts, readTok, writeTok, _ := newTestApp(t)
	defer store.Close()

	raw := validBatch(t, 1)
	var b model.Batch
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	b.Events[0].Message = "hostile <script>alert(1)</script>"
	b.Events[0].Attributes = json.RawMessage(`{"real":1.0,"large":9007199254740993,"flag":true,"none":null}`)
	body, _ := json.Marshal(b)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/experimental/skeleton/batches", strings.NewReader(string(body)))
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
	now := time.Now().UTC().Truncate(time.Second)

	vals := url.Values{}
	vals.Set("source", "src_demo_api")
	vals.Set("from", now.Add(-time.Minute).Format(time.RFC3339))
	vals.Set("to", now.Add(time.Minute).Format(time.RFC3339))
	vals.Set("csrf", csrf)
	vals.Set("severity", "1")
	vals.Set("generation", "2")
	vals.Set("sequence", "2")
	vals.Set("term", "hostile")

	code, _, fullBody := postForm(t, client, origin, "/investigate/search", vals, false)
	if code != http.StatusOK {
		t.Fatalf("full search: %d", code)
	}
	full := string(fullBody)
	if !strings.HasPrefix(strings.TrimSpace(full), "<!DOCTYPE") {
		t.Fatal("missing doctype")
	}
	if c := strings.Count(full, `id="results-region"`); c != 1 {
		t.Fatalf("results count: %d", c)
	}
	if c := strings.Count(full, `id="detail-region"`); c != 1 {
		t.Fatalf("detail count: %d", c)
	}

	code, hdr, fragBody := postForm(t, client, origin, "/investigate/search", vals, true)
	if code != http.StatusOK {
		t.Fatalf("hx search: %d", code)
	}
	frag := string(fragBody)
	if strings.Contains(frag, "<!DOCTYPE") {
		t.Fatal("hx has doctype")
	}
	if hdr.Get("X-Fragment-Kind") != "results" {
		t.Fatalf("kind: %s", hdr.Get("X-Fragment-Kind"))
	}
	if hdr.Get("X-Generation") != "2" || hdr.Get("X-Sequence") != "2" {
		t.Fatalf("gen/seq: %s/%s", hdr.Get("X-Generation"), hdr.Get("X-Sequence"))
	}
	if strings.Contains(frag, "<script>") {
		t.Fatal("raw script present")
	}
	if !strings.Contains(frag, html.EscapeString("<script>alert(1)</script>")) {
		t.Fatal("escaped script missing")
	}

	dvals := url.Values{}
	dvals.Set("id", "1")
	dvals.Set("source", "src_demo_api")
	dvals.Set("from", now.Add(-time.Minute).Format(time.RFC3339))
	dvals.Set("to", now.Add(time.Minute).Format(time.RFC3339))
	dvals.Set("csrf", csrf)
	dvals.Set("severity", "1")
	dvals.Set("generation", "2")
	dvals.Set("sequence", "2")
	dvals.Set("term", "hostile")

	code, hdr, detBody := postForm(t, client, origin, "/investigate/detail", dvals, true)
	if code != http.StatusOK {
		t.Fatalf("hx detail: %d", code)
	}
	det := string(detBody)
	if hdr.Get("X-Fragment-Kind") != "detail" {
		t.Fatalf("detail kind: %s", hdr.Get("X-Fragment-Kind"))
	}
	if hdr.Get("X-Selection") != "1" || hdr.Get("X-Origin") != "1" {
		t.Fatalf("sel/origin: %s/%s", hdr.Get("X-Selection"), hdr.Get("X-Origin"))
	}
	unesc := html.UnescapeString(det)
	for _, want := range []string{`"real":1.0`, `"large":9007199254740993`, `"flag":true`, `"none":null`} {
		if !strings.Contains(unesc, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if !strings.Contains(unesc, fmt.Sprintf(`%s`, now.Add(-time.Minute).Format(time.RFC3339))) {
		t.Fatal("frozen from missing")
	}
	if !strings.Contains(unesc, fmt.Sprintf(`%s`, now.Add(time.Minute).Format(time.RFC3339))) {
		t.Fatal("frozen to missing")
	}
}
