package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func TestClientContracts(t *testing.T) {
	css, err := embedFS.ReadFile("web/style.css")
	if err != nil {
		t.Fatal(err)
	}
	sheet := string(css)
	if !strings.Contains(sheet, "button{cursor:pointer;background:#1565c0;color:#fff;border:none}") {
		t.Fatal("button fill must be #1565c0 with white text")
	}
	if !strings.Contains(sheet, "button:hover{background:#0d47a1}") {
		t.Fatal("button hover must stay a solid color")
	}
	if !strings.Contains(sheet, "button:disabled{background:#d7deea;color:#1a1a2e}") {
		t.Fatal("disabled button must use a solid pair")
	}
	if strings.Contains(sheet, "button:disabled{opacity") || strings.Contains(sheet, "button:hover{opacity") {
		t.Fatal("button contrast must not depend on opacity")
	}
	if strings.Contains(sheet, "--accent:#1565c0") {
		t.Fatal("link accent must stay the lighter blue")
	}
	if !strings.Contains(sheet, "#btn-search-gap{color:#f4f7ff;") {
		t.Fatal("search gap link must use the light text color")
	}
	if !strings.Contains(sheet, "#btn-search-gap[hidden]{display:none}") {
		t.Fatal("search gap link must stay undisplayed while hidden")
	}

	store, ts, readTok, _, _ := newTestApp(t)
	origin := "http://" + ts.Listener.Addr().String()
	S := time.Date(2026, 6, 15, 12, 0, 30, 0, time.UTC)
	commitAt := S.Truncate(time.Minute).Add(-7 * 24 * time.Hour).Add(30 * time.Second)
	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}
	store.SetClock(func() time.Time { return commitAt })
	if _, err := store.Commit(context.Background(), scope, oneEvent(t, 40, commitAt, "synthetic aging row", `{"k":1}`)); err != nil {
		t.Fatal(err)
	}
	store.SetClock(func() time.Time { return S })
	if _, err := store.Commit(context.Background(), scope, oneEvent(t, 41, S.Add(-time.Minute), "synthetic checkout accepted", `{"task":{"attempt":1}}`)); err != nil {
		t.Fatal(err)
	}
	client, csrf := login(t, ts, readTok)

	resp, err := client.Get(origin + "/investigate")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(page)
	if resp.StatusCode != 200 {
		t.Fatalf("investigate %d", resp.StatusCode)
	}
	if !strings.Contains(body, `"attributesToSettle":["class"]`) {
		t.Fatal("htmx config must settle class only")
	}
	form := body[strings.Index(body, `id="filters"`):]
	form = form[:strings.Index(form, "</form>")]
	buttonAt := strings.Index(form, "<button")
	if buttonAt < 0 {
		t.Fatal("filters form has no button")
	}
	openTag := form[buttonAt : buttonAt+strings.Index(form[buttonAt:], ">")]
	for _, want := range []string{
		`id="btn-search-implicit"`,
		`name="intent"`,
		`value="search"`,
		`tabindex="-1"`,
		`hx-post="/investigate/search"`,
		`hx-target="#results-region"`,
		`hx-swap="outerHTML"`,
	} {
		if !strings.Contains(openTag, want) {
			t.Fatalf("default submit missing %s in %s", want, openTag)
		}
	}
	if strings.Index(form, `id="btn-search-implicit"`) > strings.Index(form, `id="btn-remove-term-1"`) {
		t.Fatal("Remove term is still the default submit")
	}

	q := model.Query{
		Sources: []string{"src_demo_api"}, From: S.Add(-15 * time.Minute).Format(time.RFC3339Nano),
		To: S.Add(time.Second).Format(time.RFC3339Nano), MinSeverity: 1,
		Terms: []string{"checkout"}, Limit: 200,
	}
	code, _, searched := postForm(t, client, origin, "/investigate/search", resultForm(csrf, q), false)
	if code != 200 {
		t.Fatalf("search %d", code)
	}
	text := string(searched)
	if !strings.Contains(text, `id="results-heading" tabindex="-1" autofocus`) {
		t.Fatal("a fresh search should autofocus the results heading")
	}
	if strings.Contains(text, `autofocus>Open details`) {
		t.Fatal("a fresh search autofocused an open button")
	}
	openRe := regexp.MustCompile(`id="(event-\d+-open)"`)
	m := openRe.FindStringSubmatch(text)
	if m == nil {
		t.Fatal("search did not render an open button")
	}
	withOrigin := resultForm(csrf, q)
	withOrigin.Set("origin", strings.TrimSuffix(strings.TrimPrefix(m[1], "event-"), "-open"))
	code, _, back := postForm(t, client, origin, "/investigate/search", withOrigin, false)
	if code != 200 {
		t.Fatalf("back search %d", code)
	}
	backText := string(back)
	if strings.Contains(backText, `id="results-heading" tabindex="-1" autofocus`) {
		t.Fatal("back still autofocuses the heading")
	}
	if !strings.Contains(backText, `id="`+m[1]+`" autofocus`) {
		t.Fatalf("back did not autofocus %s", m[1])
	}

	bad := resultForm(csrf, q)
	bad.Set("preset", "nope")
	bad.Del("from")
	bad.Del("to")
	code, hdr, notice := postForm(t, client, origin, "/investigate/search", bad, true)
	if code != 400 || hdr.Get("X-Notice") != "invalid_query" || !strings.Contains(string(notice), "Query is invalid") {
		t.Fatalf("invalid notice %d %s %s", code, hdr.Get("X-Notice"), notice)
	}

	agingQ := model.Query{
		Sources: []string{"src_demo_api"}, From: commitAt.Format(time.RFC3339Nano),
		To: commitAt.Add(7 * 24 * time.Hour).Format(time.RFC3339Nano), MinSeverity: 1,
		Terms: []string{"aging"}, Limit: 200,
	}
	code, _, agingBody := postForm(t, client, origin, "/investigate/search", resultForm(csrf, agingQ), false)
	if code != 200 || !strings.Contains(string(agingBody), "synthetic aging row") {
		t.Fatalf("aging search %d", code)
	}
	am := openRe.FindStringSubmatch(string(agingBody))
	if am == nil {
		t.Fatal("aging row has no open button")
	}
	store.SetClock(func() time.Time { return S.Add(2 * time.Minute) })
	detail := resultForm(csrf, agingQ)
	detail.Set("id", strings.TrimSuffix(strings.TrimPrefix(am[1], "event-"), "-open"))
	code, hdr, rowBody := postForm(t, client, origin, "/investigate/detail", detail, true)
	if code != 404 || hdr.Get("X-Notice") != "row_expired" || !strings.Contains(string(rowBody), "Row expired. Rerun query. This row is past the retention window.") {
		t.Fatalf("row notice %d %s %s", code, hdr.Get("X-Notice"), rowBody)
	}
	if strings.Contains(string(rowBody), "Event not found") {
		t.Fatal("row expired used the missing-event copy")
	}
}

func chromePath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("CHROME_PATH"); p != "" {
		return p
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	t.Fatal("chrome not found; set CHROME_PATH")
	return ""
}

func TestBrowserE2E(t *testing.T) {
	chrome := chromePath(t)
	store, ts, readTok, _, _ := newTestApp(t)
	origin := "http://" + ts.Listener.Addr().String()
	S := time.Date(2026, 6, 15, 12, 0, 30, 0, time.UTC)
	commitAt := S.Truncate(time.Minute).Add(-7 * 24 * time.Hour).Add(30 * time.Second)
	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}

	store.SetClock(func() time.Time { return commitAt })
	if _, err := store.Commit(context.Background(), scope, oneEvent(t, 40, commitAt, "synthetic aging row", `{"k":1}`)); err != nil {
		t.Fatal(err)
	}
	store.SetClock(func() time.Time { return S })
	when := S.Add(-time.Minute)
	if _, err := store.Commit(context.Background(), scope, oneEvent(t, 41, when, "synthetic checkout accepted", `{"task":{"attempt":1},"note":"done"}`)); err != nil {
		t.Fatal(err)
	}
	hostile := `synthetic hostile <script>alert(1)</script> <div hx-get="/evil">x</div>`
	if _, err := store.Commit(context.Background(), scope, oneEvent(t, 42, when, hostile, `{"k":1}`)); err != nil {
		t.Fatal(err)
	}
	filler := nativeEvents(t, 7, 200, when, "synthetic filler", `{"k":1}`)
	raw, err := json.Marshal(filler)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitNative(context.Background(), scope, filler, len(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(context.Background(), scope, oneEvent(t, 43, when, "synthetic filler", `{"k":1}`)); err != nil {
		t.Fatal(err)
	}
	for id := 1; id <= 50; id++ {
		batch := nativeEvents(t, 100+id, 200, when, "commonwide synthetic", `{"task":{"attempt":1}}`)
		raw, err = json.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CommitNative(context.Background(), scope, batch, len(raw)); err != nil {
			t.Fatalf("batch %d: %v", id, err)
		}
	}

	setClock := func(at time.Time) {
		frozen := at
		store.SetClock(func() time.Time { return frozen })
	}
	setClock(S)

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	limit := 3 * time.Minute
	if os.Getenv("E2E_SHOT_DIR") != "" {
		limit = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join(filepath.Dir(file), "browser_e2e.js"))
	cmd.Env = append(os.Environ(),
		"BASE="+origin,
		"READ_TOKEN="+readTok,
		"CHROME="+chrome,
		"AGING_FROM="+commitAt.Format(time.RFC3339Nano),
		"AGING_TO="+commitAt.Add(7*24*time.Hour).Format(time.RFC3339Nano),
	)
	if dir := os.Getenv("E2E_SHOT_DIR"); dir != "" {
		cmd.Env = append(cmd.Env, "E2E_SHOT_DIR="+dir)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		dumped := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if dumped || time.Since(time.Unix(0, last.Load())) < 20*time.Second {
					continue
				}
				dumped = true
				buf := make([]byte, 1<<20)
				n := runtime.Stack(buf, true)
				t.Logf("GOROUTINES after a quiet step\n%s", buf[:n])
			}
		}
	}()
	passed := false
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		line := sc.Text()
		last.Store(time.Now().UnixNano())
		t.Log(line)
		switch line {
		case "NEED_CLOCK row":
			setClock(S.Add(2 * time.Minute))
			if _, err := io.WriteString(stdin, "ok\n"); err != nil {
				t.Fatal(err)
			}
		case "NEED_CLOCK cursor":
			setClock(S.Add(2*time.Minute + 16*time.Minute))
			if _, err := io.WriteString(stdin, "ok\n"); err != nil {
				t.Fatal(err)
			}
		case "NEED_CLOCK reset":
			setClock(S)
			if _, err := io.WriteString(stdin, "ok\n"); err != nil {
				t.Fatal(err)
			}
		case "NEED_LIVE":
			for i := 0; i < 12; i++ {
				if _, err := store.Commit(context.Background(), scope, oneEvent(t, 9000+i, S, "synthetic live arrived", `{"k":1}`)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := io.WriteString(stdin, "ok\n"); err != nil {
				t.Fatal(err)
			}
		case "NEED_LIVE_HOLD":
			if _, err := store.Commit(context.Background(), scope, oneEvent(t, 9100, S, "synthetic live while paused", `{"k":1}`)); err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(stdin, "ok\n"); err != nil {
				t.Fatal(err)
			}
		case "NEED_GAP":
			for b := 0; b < 25; b++ {
				batch := nativeEvents(t, 9200+b, 200, S, "synthetic gap row", `{"k":1}`)
				raw, err := json.Marshal(batch)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.CommitNative(context.Background(), scope, batch, len(raw)); err != nil {
					t.Fatalf("gap batch %d: %v", b, err)
				}
			}
			if _, err := store.Commit(context.Background(), scope, oneEvent(t, 9299, S, "synthetic gap row", `{"k":1}`)); err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(stdin, "ok\n"); err != nil {
				t.Fatal(err)
			}
		case "PASS":
			passed = true
		}
		if strings.HasPrefix(line, "FAIL") {
			t.Errorf("%s", line)
		}
	}
	waitErr := cmd.Wait()
	if !passed || waitErr != nil {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Logf("GOROUTINES\n%s", buf[:n])
		t.Fatalf("browser e2e failed: %v\n%s", waitErr, stderr.String())
	}
}

func TestLiveBrowserP95(t *testing.T) {
	chrome := chromePath(t)
	store, ts, readTok, _, _ := newTestApp(t)
	origin := "http://" + ts.Listener.Addr().String()
	S := time.Date(2026, 6, 15, 12, 0, 30, 0, time.UTC)
	store.SetClock(func() time.Time { return S })
	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}
	if _, err := store.Commit(context.Background(), scope, oneEvent(t, 80, S, "synthetic latency seed", `{"k":1}`)); err != nil {
		t.Fatal(err)
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join(filepath.Dir(file), "browser_e2e.js"))
	cmd.Env = append(os.Environ(),
		"BASE="+origin,
		"READ_TOKEN="+readTok,
		"CHROME="+chrome,
		"E2E_LATENCY=1",
		"AGING_FROM="+S.Format(time.RFC3339Nano),
		"AGING_TO="+S.Add(time.Minute).Format(time.RFC3339Nano),
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var p95 string
	passed := false
	n := 0
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		line := sc.Text()
		t.Log(line)
		switch {
		case strings.HasPrefix(line, "NEED_LATENCY "):
			msg := strings.TrimPrefix(line, "NEED_LATENCY ")
			n++
			if _, err := store.Commit(context.Background(), scope, oneEvent(t, 8000+n, S, msg, `{"k":1}`)); err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(stdin, "ok\n"); err != nil {
				t.Fatal(err)
			}
		case strings.HasPrefix(line, "LATENCY_P95_MS "):
			p95 = strings.TrimPrefix(line, "LATENCY_P95_MS ")
		case line == "PASS":
			passed = true
		}
		if strings.HasPrefix(line, "FAIL") {
			t.Errorf("%s", line)
		}
	}
	waitErr := cmd.Wait()
	if !passed || waitErr != nil {
		t.Fatalf("browser latency failed: %v\n%s", waitErr, stderr.String())
	}
	ms, err := time.ParseDuration(p95 + "ms")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("browser_idle_p95=%s", ms)
	if ms >= 2*time.Second {
		t.Fatalf("browser p95 %s", ms)
	}
}
