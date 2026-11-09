package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func liveVals(csrf, client, intent, recv, id string) url.Values {
	v := url.Values{}
	v.Set("csrf", csrf)
	v.Set("client", client)
	v.Set("live_gen", "1")
	v.Set("intent", intent)
	v.Set("cursor_received", recv)
	v.Set("cursor_id", id)
	v.Set("severity", "1")
	return v
}

func TestLiveNoJSRefresh(t *testing.T) {
	store, ts, readTok, _, _ := newTestApp(t)
	origin := "http://" + ts.Listener.Addr().String()
	S := time.Date(2026, 6, 15, 12, 0, 30, 0, time.UTC)
	store.SetClock(func() time.Time { return S })
	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}
	if _, err := store.Commit(context.Background(), scope, oneEvent(t, 70, S, "synthetic refresh row", `{"k":1}`)); err != nil {
		t.Fatal(err)
	}
	client, csrf := login(t, ts, readTok)
	resp, err := client.Get(origin + "/investigate")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(raw)
	if resp.StatusCode != 200 {
		t.Fatalf("get %d", resp.StatusCode)
	}
	if !strings.Contains(page, "Live requires JavaScript. Use Refresh for historical search.") {
		t.Fatal("missing no-js live copy")
	}
	if !strings.Contains(page, `id="btn-refresh"`) {
		t.Fatal("missing refresh control")
	}
	if strings.Contains(page, "hx-trigger") {
		t.Fatal("initial page polls without a live start")
	}
	vals := url.Values{}
	vals.Set("csrf", csrf)
	vals.Set("preset", "15m")
	vals.Set("severity", "1")
	vals.Set("intent", "search")
	vals.Set("generation", "1")
	vals.Set("sequence", "1")
	code, _, body := postForm(t, client, origin, "/investigate/search", vals, false)
	if code != 200 || !strings.Contains(string(body), "synthetic refresh row") || !strings.Contains(string(body), "<html") {
		t.Fatalf("refresh %d %s", code, body)
	}
	code, _, body = postForm(t, client, origin, "/investigate/live", liveVals(csrf, "clientnojs01", "start", "0", "0"), false)
	if code != 200 || !strings.Contains(string(body), "Live requires JavaScript. Use Refresh for historical search.") {
		t.Fatalf("no-js start %d %s", code, body)
	}
	if strings.Contains(string(body), "hx-trigger") {
		t.Fatal("no-js start armed a poll")
	}
}

func TestLivePollHTTP(t *testing.T) {
	store, ts, readTok, _, _ := newTestApp(t)
	origin := "http://" + ts.Listener.Addr().String()
	S := time.Date(2026, 6, 15, 12, 0, 30, 0, time.UTC)
	store.SetClock(func() time.Time { return S })
	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}
	hostile := `synthetic hostile <script>alert(1)</script> <div hx-get="/evil">x</div>`
	if _, err := store.Commit(context.Background(), scope, oneEvent(t, 71, S, hostile, `{"k":1}`)); err != nil {
		t.Fatal(err)
	}
	var inUse int
	beforeRender.Store(func() { inUse = store.ReaderInUse() })
	t.Cleanup(func() { beforeRender.Store(func() {}) })
	client, csrf := login(t, ts, readTok)

	code, hdr, body := postForm(t, client, origin, "/investigate/live", liveVals(csrf, "clientpoll01", "poll", "0", "0"), true)
	if code != 200 {
		t.Fatalf("poll %d %s", code, body)
	}
	if inUse != 0 {
		t.Fatalf("reader still open during render: %d", inUse)
	}
	text := string(body)
	if strings.Contains(text, "<script") {
		t.Fatalf("hostile markup was not escaped: %s", text)
	}
	if !strings.Contains(text, "&lt;script&gt;") || !strings.Contains(text, "&lt;div hx-get=") {
		t.Fatalf("escaped row missing: %s", text)
	}
	if hdr.Get("X-Live-Cursor-Id") == "" || hdr.Get("X-Live-Cursor-Received") == "" {
		t.Fatalf("cursor headers %v", hdr)
	}

	code, hdr, body = postForm(t, client, origin, "/investigate/live", liveVals(csrf, "clientstart1", "start", "0", "0"), true)
	if code != 200 || !strings.Contains(string(body), "Live: Receiving") {
		t.Fatalf("start %d %s", code, body)
	}
	recv := hdr.Get("X-Live-Cursor-Received")
	id := hdr.Get("X-Live-Cursor-Id")
	if _, err := store.Commit(context.Background(), scope, oneEvent(t, 72, S, "synthetic after tail", `{"k":1}`)); err != nil {
		t.Fatal(err)
	}
	code, _, body = postForm(t, client, origin, "/investigate/live", liveVals(csrf, "clientstart1", "poll", recv, id), true)
	if code != 200 || !strings.Contains(string(body), "synthetic after tail") || strings.Contains(string(body), "synthetic hostile") {
		t.Fatalf("tail poll %d %s", code, body)
	}

	code, hdr, body = postForm(t, client, origin, "/investigate/live", liveVals(csrf, "clientthird1", "start", "0", "0"), true)
	if code != http.StatusTooManyRequests || hdr.Get("Retry-After") != "5" || !strings.Contains(string(body), "Live: Throttled") {
		t.Fatalf("third client %d %s %s", code, hdr.Get("Retry-After"), body)
	}
	store.SetClock(func() time.Time { return S.Add(16 * time.Second) })
	code, _, _ = postForm(t, client, origin, "/investigate/live", liveVals(csrf, "clientthird1", "start", "0", "0"), true)
	if code != 200 {
		t.Fatalf("slot expired %d", code)
	}

	store.SetClock(func() time.Time { return S.Add(40 * time.Second) })
	if _, err := store.Commit(context.Background(), scope, oneEvent(t, 73, S.Add(40*time.Second), "synthetic too late", `{"k":1}`)); err != nil {
		t.Fatal(err)
	}
	late := liveVals(csrf, "clientgap001", "resume", recv, id)
	code, hdr, body = postForm(t, client, origin, "/investigate/live", late, true)
	if code != http.StatusGone || !strings.Contains(string(body), "Live: Gap detected") || hdr.Get("X-Live-Gap-From") == "" || hdr.Get("X-Live-Gap-To") == "" {
		t.Fatalf("gap %d %s %s", code, hdr.Get("X-Live-Gap-From"), body)
	}

	second, _ := login(t, ts, readTok)
	code, _, _ = postForm(t, client, origin, "/investigate/live", liveVals(csrf, "clientpoll01", "poll", "0", "0"), true)
	if code != http.StatusUnauthorized {
		t.Fatalf("revoked session %d", code)
	}
	if second == nil {
		t.Fatal("second login")
	}
}

func TestLivePollPagesTiedRows(t *testing.T) {
	store, ts, readTok, _, _ := newTestApp(t)
	origin := "http://" + ts.Listener.Addr().String()
	S := time.Date(2026, 6, 15, 12, 0, 30, 0, time.UTC)
	store.SetClock(func() time.Time { return S })
	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}
	for i, n := range []int{200, 1} {
		batch := nativeEvents(t, 80+i, n, S, "synthetic page tie", `{"k":1}`)
		raw, err := json.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CommitNative(context.Background(), scope, batch, len(raw)); err != nil {
			t.Fatal(err)
		}
	}
	client, csrf := login(t, ts, readTok)
	seen := map[string]int{}
	recv, id := "0", "0"
	idRe := regexp.MustCompile(`data-ingest-id="([0-9]+)"`)
	for page := 0; page < 4; page++ {
		code, hdr, body := postForm(t, client, origin, "/investigate/live", liveVals(csrf, "clientpage01", "poll", recv, id), true)
		if code != 200 {
			t.Fatalf("page %d: %d %s", page, code, body)
		}
		for _, m := range idRe.FindAllSubmatch(body, -1) {
			seen[string(m[1])]++
		}
		nextR, nextID := hdr.Get("X-Live-Cursor-Received"), hdr.Get("X-Live-Cursor-Id")
		if nextR == recv && nextID == id {
			break
		}
		recv, id = nextR, nextID
		if hdr.Get("X-Live-Idle") == "1" {
			break
		}
	}
	if len(seen) != 201 {
		t.Fatalf("tied pages returned %d rows", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("ingest %s repeated %d", id, n)
		}
	}
}
