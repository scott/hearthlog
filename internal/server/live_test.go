package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
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

	code, hdr, body = postForm(t, client, origin, "/investigate/live", liveVals(csrf, "clientthird1", "poll", "0", "0"), true)
	if code != http.StatusTooManyRequests || hdr.Get("Retry-After") != "5" || !strings.Contains(string(body), "Live: Throttled") {
		t.Fatalf("third client %d %s %s", code, hdr.Get("Retry-After"), body)
	}
	store.SetClock(func() time.Time { return S.Add(16 * time.Second) })
	code, _, _ = postForm(t, client, origin, "/investigate/live", liveVals(csrf, "clientthird1", "poll", "0", "0"), true)
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
	var paceTick atomic.Int64
	paceBase := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	livePace.Store(func() time.Time {
		return paceBase.Add(time.Duration(paceTick.Add(1)) * time.Second)
	})
	t.Cleanup(func() { livePace.Store(func() time.Time { return time.Now() }) })
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

func TestLiveReloadDoesNotLockOut(t *testing.T) {
	_, ts, readTok, _, _ := newTestApp(t)
	origin := "http://" + ts.Listener.Addr().String()
	client, csrf := login(t, ts, readTok)
	ids := []string{"reload0001", "reload0002", "reload0003"}
	for _, id := range ids {
		code, _, body := postForm(t, client, origin, "/investigate/live", liveVals(csrf, id, "start", "0", "0"), true)
		if code != 200 || !strings.Contains(string(body), "Live: Receiving") {
			t.Fatalf("reload %s %d %s", id, code, body)
		}
	}
	for i := 0; i < 3; i++ {
		code, _, body := postForm(t, client, origin, "/investigate/live", liveVals(csrf, "reloadsame1", "start", "0", "0"), true)
		if code != 200 {
			t.Fatalf("same id reload %d %d %s", i, code, body)
		}
	}
	stop := liveVals(csrf, "reloadsame1", "stop", "0", "0")
	code, _, _ := postForm(t, client, origin, "/investigate/live", stop, true)
	if code != http.StatusNoContent {
		t.Fatalf("stop %d", code)
	}
	code, _, body := postForm(t, client, origin, "/investigate/live", liveVals(csrf, "reload0004", "start", "0", "0"), true)
	if code != 200 {
		t.Fatalf("start after stop %d %s", code, body)
	}
}

func TestLiveReplacedSessionStarts(t *testing.T) {
	_, ts, readTok, _, _ := newTestApp(t)
	origin := "http://" + ts.Listener.Addr().String()
	first, csrf1 := login(t, ts, readTok)
	for _, id := range []string{"oldsession1", "oldsession2"} {
		code, _, body := postForm(t, first, origin, "/investigate/live", liveVals(csrf1, id, "start", "0", "0"), true)
		if code != 200 {
			t.Fatalf("first %s %d %s", id, code, body)
		}
	}
	second, csrf2 := login(t, ts, readTok)
	code, _, body := postForm(t, second, origin, "/investigate/live", liveVals(csrf2, "oldsession1", "start", "0", "0"), true)
	if code != 200 || !strings.Contains(string(body), "Live: Receiving") {
		t.Fatalf("same id after replace %d %s", code, body)
	}
	code, _, body = postForm(t, second, origin, "/investigate/live", liveVals(csrf2, "freshid001", "start", "0", "0"), true)
	if code != 200 || !strings.Contains(string(body), "Live: Receiving") {
		t.Fatalf("fresh id after replace %d %s", code, body)
	}
}

func TestLiveMinIntervalAndInFlight(t *testing.T) {
	store, ts, readTok, _, _ := newTestApp(t)
	origin := "http://" + ts.Listener.Addr().String()
	S := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return S })
	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}
	for i, n := range []int{200, 1} {
		batch := nativeEvents(t, 60+i, n, S, "synthetic pace", `{"k":1}`)
		raw, err := json.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CommitNative(context.Background(), scope, batch, len(raw)); err != nil {
			t.Fatal(err)
		}
	}
	client, csrf := login(t, ts, readTok)
	pace := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	livePace.Store(func() time.Time { return pace })
	t.Cleanup(func() { livePace.Store(func() time.Time { return time.Now() }) })

	code, hdr, body := postForm(t, client, origin, "/investigate/live", liveVals(csrf, "paceclient1", "poll", "0", "0"), true)
	if code != 200 || hdr.Get("X-Live-Caught") != "0" {
		t.Fatalf("first %d caught %s %s", code, hdr.Get("X-Live-Caught"), body)
	}
	recv, id := hdr.Get("X-Live-Cursor-Received"), hdr.Get("X-Live-Cursor-Id")
	code, hdr, body = postForm(t, client, origin, "/investigate/live", liveVals(csrf, "paceclient1", "poll", recv, id), true)
	if code != 200 || hdr.Get("X-Live-Caught") != "1" {
		t.Fatalf("behind follow-up %d caught %s %s", code, hdr.Get("X-Live-Caught"), body)
	}
	code, hdr, _ = postForm(t, client, origin, "/investigate/live", liveVals(csrf, "paceclient1", "poll", hdr.Get("X-Live-Cursor-Received"), hdr.Get("X-Live-Cursor-Id")), true)
	if code != http.StatusTooManyRequests || hdr.Get("Retry-After") != "5" {
		t.Fatalf("interval %d %s", code, hdr.Get("Retry-After"))
	}
	pace = pace.Add(time.Second)
	code, _, _ = postForm(t, client, origin, "/investigate/live", liveVals(csrf, "paceclient1", "poll", recv, id), true)
	if code != 200 {
		t.Fatalf("after interval %d", code)
	}

	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	var closeOnce sync.Once
	letGo := func() { closeOnce.Do(func() { close(release) }) }
	gate := func() {
		once.Do(func() { close(entered) })
		<-release
	}
	liveGate.Store(&gate)
	t.Cleanup(func() {
		letGo()
		liveGate.Store(nil)
	})
	var okN, limited, done atomic.Int32
	for i := 0; i < 16; i++ {
		go func() {
			c, _, _ := postForm(t, client, origin, "/investigate/live", liveVals(csrf, "flightclient", "poll", "0", "0"), true)
			if c == 200 {
				okN.Add(1)
			}
			if c == http.StatusTooManyRequests {
				limited.Add(1)
			}
			done.Add(1)
		}()
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request did not start")
	}
	deadline := time.Now().Add(5 * time.Second)
	for done.Load() < 15 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	letGo()
	deadline = time.Now().Add(5 * time.Second)
	for done.Load() < 16 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if okN.Load() != 1 || limited.Load() != 15 {
		t.Fatalf("in flight ok=%d limited=%d done=%d", okN.Load(), limited.Load(), done.Load())
	}
}
