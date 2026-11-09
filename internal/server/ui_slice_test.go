package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func TestUIGuidanceStates(t *testing.T) {
	store, ts, readTok, _, _ := newTestApp(t)
	origin := "http://" + ts.Listener.Addr().String()
	client, csrf := login(t, ts, readTok)
	frozen := time.Date(2026, 4, 2, 15, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return frozen })

	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}
	for i := 0; i < 2; i++ {
		b := oneEvent(t, i+1, frozen.Add(-time.Minute), "cursor-page synthetic", `{"k":1}`)
		if _, err := store.Commit(context.Background(), scope, b); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	q := model.Query{
		Sources: []string{"src_demo_api"}, From: frozen.Add(-time.Hour).Format(time.RFC3339Nano),
		To: frozen.Add(time.Second).Format(time.RFC3339Nano), MinSeverity: 1, Limit: 1,
	}
	page, err := store.ReadPage(context.Background(), scope, q)
	if err != nil || page.Next == "" || len(page.Rows) != 1 {
		t.Fatalf("page: %v rows=%d next=%t", err, len(page.Rows), page.Next != "")
	}

	store.SetClock(func() time.Time { return frozen.Add(15*time.Minute + time.Second) })
	vals := url.Values{}
	vals.Set("csrf", csrf)
	vals.Set("cursor", page.Next)
	vals.Set("generation", "2")
	vals.Set("sequence", "2")
	code, _, body := postForm(t, client, origin, "/investigate/search", vals, false)
	if code != http.StatusGone {
		t.Fatalf("expired status %d body %s", code, body)
	}
	if !strings.Contains(string(body), "Results expired") || !strings.Contains(string(body), "Rerun query") {
		t.Fatalf("expired copy: %s", body)
	}
	if strings.Contains(string(body), "No matching events") || strings.Contains(string(body), page.Next) {
		t.Fatal("expired page kept the cursor or claimed zero matches")
	}
	if !strings.Contains(string(body), q.From) {
		t.Fatal("expired page dropped the frozen window")
	}

	req := httptest.NewRequest(http.MethodPost, origin+"/investigate/search", strings.NewReader(resultForm(csrf, q).Encode()))
	req.Host = req.URL.Host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", origin)
	for _, c := range client.Jar.Cookies(req.URL) {
		req.AddCookie(c)
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	ts.Config.Handler.ServeHTTP(rec, req)
	timeoutBody := rec.Body.String()
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("timeout status %d body %s", rec.Code, timeoutBody)
	}
	if !strings.Contains(timeoutBody, "Query timed out") || !strings.Contains(timeoutBody, "Narrow the time window") {
		t.Fatalf("timeout copy: %s", timeoutBody)
	}
	if strings.Contains(timeoutBody, "No matching events") {
		t.Fatal("timeout claimed zero matches")
	}
	if strings.Contains(timeoutBody, "No sources yet") {
		t.Fatal("timeout page claimed the catalog was empty")
	}
}

func TestQueryTooBroadCopy(t *testing.T) {
	store, ts, readTok, _, _ := newTestApp(t)
	origin := "http://" + ts.Listener.Addr().String()
	frozen := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return frozen })
	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}
	client, csrf := login(t, ts, readTok)
	vals := resultForm(csrf, model.Query{
		Sources: []string{"src_demo_api"}, From: frozen.Add(-6 * 24 * time.Hour).Format(time.RFC3339Nano),
		To: frozen.Add(time.Second).Format(time.RFC3339Nano), MinSeverity: 1,
		Terms: []string{"commonwide"}, Limit: 200,
	})
	vals.Add("path", "task.attempt")
	vals.Add("op", "eq")
	vals.Add("ptype", "integer")
	vals.Add("pval", "999")
	var code int
	var body []byte
	seeded := 0
	for round := 0; round < 6 && code != http.StatusUnprocessableEntity; round++ {
		for n := 0; n < 25; n++ {
			id := round*25 + n + 1
			when := frozen.Add(-6 * 24 * time.Hour).Add(time.Duration(id) * time.Second)
			b := nativeEvents(t, id, 200, when, "commonwide synthetic", `{"task":{"attempt":1}}`)
			raw, _ := json.Marshal(b)
			if _, err := store.CommitNative(context.Background(), scope, b, len(raw)); err != nil {
				t.Fatalf("batch %d: %v", id, err)
			}
			seeded += 200
		}
		code, _, body = postForm(t, client, origin, "/investigate/search", vals, true)
	}
	t.Logf("seeded=%d status=%d", seeded, code)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("broad status %d body %s", code, body)
	}
	text := string(body)
	if !strings.Contains(text, "Query too broad") || !strings.Contains(text, "Narrow the time window") {
		t.Fatalf("broad copy: %s", text)
	}
	if strings.Contains(text, "No matching events") || strings.Contains(text, "commonwide synthetic") {
		t.Fatal("broad response looked like a result page")
	}
}

func TestRowExpiredVersusMissing(t *testing.T) {
	store, ts, readTok, _, _ := newTestApp(t)
	origin := "http://" + ts.Listener.Addr().String()
	S := time.Date(2026, 6, 15, 12, 0, 30, 0, time.UTC)
	commitAt := S.Truncate(time.Minute).Add(-7 * 24 * time.Hour).Add(30 * time.Second)
	store.SetClock(func() time.Time { return commitAt })
	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}
	b := oneEvent(t, 40, commitAt, "aging-row synthetic", `{"k":1}`)
	if _, err := store.Commit(context.Background(), scope, b); err != nil {
		t.Fatal(err)
	}
	other := model.Scope{WorkspaceID: "ws_demo_b", SourceID: "src_demo_worker"}
	store.SetClock(func() time.Time { return S })
	b2 := oneEvent(t, 41, S.Add(-time.Minute), "other-workspace synthetic", `{"k":1}`)
	if _, err := store.Commit(context.Background(), other, b2); err != nil {
		t.Fatal(err)
	}
	client, csrf := login(t, ts, readTok)
	q := model.Query{
		Sources: []string{"src_demo_api"}, From: commitAt.Format(time.RFC3339Nano),
		To: commitAt.Add(7 * 24 * time.Hour).Format(time.RFC3339Nano), MinSeverity: 1, Limit: 200,
	}
	code, _, body := postForm(t, client, origin, "/investigate/search", resultForm(csrf, q), false)
	if code != http.StatusOK || !strings.Contains(string(body), "aging-row synthetic") {
		t.Fatalf("search status %d", code)
	}
	dvals := resultForm(csrf, q)
	dvals.Set("id", "1")
	code, _, body = postForm(t, client, origin, "/investigate/detail", dvals, false)
	if code != http.StatusOK || !strings.Contains(string(body), "aging-row synthetic") {
		t.Fatalf("detail before expiry %d %s", code, body)
	}

	store.SetClock(func() time.Time { return S.Add(2 * time.Minute) })
	code, _, body = postForm(t, client, origin, "/investigate/detail", dvals, false)
	if code != http.StatusNotFound {
		t.Fatalf("expired detail status %d", code)
	}
	if !strings.Contains(string(body), "Row expired") || !strings.Contains(string(body), "Rerun query") {
		t.Fatalf("expired copy: %s", body)
	}
	if strings.Contains(string(body), "Event not found") || strings.Contains(string(body), "aging-row synthetic") {
		t.Fatal("expired row rendered as a generic miss or leaked the message")
	}

	missing := resultForm(csrf, q)
	missing.Set("id", "99")
	code, _, body = postForm(t, client, origin, "/investigate/detail", missing, false)
	if code != http.StatusNotFound || !strings.Contains(string(body), "Event not found") || strings.Contains(string(body), "Row expired") {
		t.Fatalf("missing detail %d %s", code, body)
	}
	cross := resultForm(csrf, q)
	cross.Set("id", "2")
	code, _, body = postForm(t, client, origin, "/investigate/detail", cross, false)
	if code != http.StatusNotFound || !strings.Contains(string(body), "Event not found") || strings.Contains(string(body), "Row expired") || strings.Contains(string(body), "other-workspace") {
		t.Fatalf("cross workspace %d %s", code, body)
	}
}

func TestFormCapKeepsCursorAndFiltersApart(t *testing.T) {
	store, ts, readTok, _, _ := newTestApp(t)
	origin := "http://" + ts.Listener.Addr().String()
	frozen := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return frozen })
	scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}

	var cursor string
	for n := 40; n <= 220; n += 20 {
		val := strings.Repeat("b", n)
		q := maxFilterQuery(t, frozen, val)
		if err := model.ValidateQuery(q); err != nil {
			break
		}
		attrs := predAttrs(val)
		if len(attrs) > 8000 {
			break
		}
		for i := 0; i < 3; i++ {
			msg := strings.Repeat("a", 128) + " cap-row"
			b := oneEvent(t, 1000+n*10+i, frozen.Add(-time.Minute), msg, attrs)
			if _, err := store.Commit(context.Background(), scope, b); err != nil {
				t.Fatal(err)
			}
		}
		pageQ := q
		pageQ.Limit = 1
		page, err := store.ReadPage(context.Background(), scope, pageQ)
		if err != nil || page.Next == "" {
			t.Fatalf("n=%d page: %v next=%t", n, err, page.Next != "")
		}
		filters := filterValues("cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", q).Encode()
		only := url.Values{}
		only.Set("csrf", "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
		only.Set("cursor", page.Next)
		only.Set("preset", "custom")
		only.Set("generation", "2")
		only.Set("sequence", "2")
		both := filterValues("cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", q)
		both.Set("cursor", page.Next)
		if len(filters) <= 4096 && len(only.Encode()) <= 4096 && len(both.Encode()) > 4096 {
			cursor = page.Next
			break
		}
	}
	if cursor == "" {
		t.Fatal("no filter size put the cursor plus filters over 4096 while each part fit")
	}
	client, csrf := login(t, ts, readTok)
	only := url.Values{}
	only.Set("csrf", csrf)
	only.Set("cursor", cursor)
	only.Set("preset", "custom")
	only.Set("generation", "2")
	only.Set("sequence", "2")
	if len(only.Encode()) > 4096 {
		t.Fatalf("cursor form %d", len(only.Encode()))
	}
	code, _, body := postForm(t, client, origin, "/investigate/search", only, false)
	if code != http.StatusOK || !strings.Contains(string(body), "cap-row") {
		t.Fatalf("cursor page %d %s", code, body)
	}
	page := string(body)
	start := strings.Index(page, `id="btn-older"`)
	if start < 0 {
		t.Fatal("older control missing")
	}
	formStart := strings.LastIndex(page[:start], "<form")
	formEnd := strings.Index(page[start:], "</form>")
	if formStart < 0 || formEnd < 0 {
		t.Fatal("older form missing")
	}
	older := page[formStart : start+formEnd]
	if strings.Contains(older, `name="term"`) || strings.Contains(older, `name="path"`) || strings.Contains(older, `name="from"`) {
		t.Fatal("older form repeats filters")
	}

	over := strings.Repeat("x", 4097)
	req, err := http.NewRequest(http.MethodPost, origin+"/investigate/search", strings.NewReader(over))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", origin)
	for _, c := range client.Jar.Cookies(req.URL) {
		req.AddCookie(c)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("over-cap status %d", resp.StatusCode)
	}
}

func TestTermsPredicatesAndHostileDetail(t *testing.T) {
	store, ts, readTok, _, _ := newTestApp(t)
	origin := "http://" + ts.Listener.Addr().String()
	now := time.Now().UTC().Truncate(time.Microsecond)
	attrs := `{"task":{"attempt":1},"note":"done","real":1.0,"string":"1","boolean":true,"present":null,"exact_large_integer":9007199254740993,"tags":["demo"],"x.y":"value_with_dot","hostile_markup":"<div hx-get=\"/evil\">data</div>"}`
	b := oneEvent(t, 80, now, "alpha beta synthetic", attrs)
	if _, err := store.Commit(context.Background(), model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, b); err != nil {
		t.Fatal(err)
	}
	client, csrf := login(t, ts, readTok)
	vals := url.Values{}
	vals.Set("csrf", csrf)
	vals.Set("source", "src_demo_api")
	vals.Set("from", now.Add(-time.Minute).Format(time.RFC3339Nano))
	vals.Set("to", now.Add(time.Minute).Format(time.RFC3339Nano))
	vals.Set("severity", "1")
	vals.Add("term", "alpha")
	vals.Add("term", "beta")
	vals.Add("path", "task.attempt")
	vals.Add("op", "eq")
	vals.Add("ptype", "integer")
	vals.Add("pval", "1")
	vals.Set("generation", "2")
	vals.Set("sequence", "2")
	code, _, body := postForm(t, client, origin, "/investigate/search", vals, false)
	if code != http.StatusOK || !strings.Contains(string(body), "alpha beta synthetic") {
		t.Fatalf("search %d %s", code, body)
	}
	vals.Set("id", "1")
	code, _, body = postForm(t, client, origin, "/investigate/detail", vals, false)
	text := string(body)
	if code != http.StatusOK {
		t.Fatalf("detail %d", code)
	}
	for _, want := range []string{"integer", "real", "boolean", "null", "string", "9007199254740993", "Unsupported path", "Show full", "Copy JSON", "Event details"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(text, `<div hx-get`) || strings.Contains(text, "<script>") {
		t.Fatal("hostile markup was not escaped")
	}
	if !strings.Contains(text, `&lt;div hx-get=`) {
		t.Fatal("escaped hostile markup missing")
	}
	vals.Add("path", "note")
	vals.Add("op", "eq")
	vals.Add("ptype", "string")
	vals.Add("pval", "done")
	vals.Set("preset", "custom")
	vals.Del("id")
	code, _, body = postForm(t, client, origin, "/investigate/search", vals, false)
	if code != http.StatusOK || !strings.Contains(string(body), "alpha beta synthetic") {
		t.Fatalf("filter %d", code)
	}
}

func TestSavedViewReevaluatesRelativeWindow(t *testing.T) {
	store, ts, readTok, _, _ := newTestApp(t)
	origin := "http://" + ts.Listener.Addr().String()
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return t0 })
	client, csrf := login(t, ts, readTok)
	vals := url.Values{}
	vals.Set("csrf", csrf)
	vals.Set("preset", "15m")
	vals.Set("severity", "9")
	vals.Set("source", "src_demo_api")
	vals.Set("term", "synthetic")
	vals.Set("view", "morning")
	vals.Set("intent", "save_view")
	vals.Set("generation", "1")
	vals.Set("sequence", "1")
	code, _, body := postForm(t, client, origin, "/investigate/search", vals, false)
	if code != http.StatusOK || !strings.Contains(string(body), "View saved") {
		t.Fatalf("save %d %s", code, body)
	}
	t1 := t0.Add(2 * time.Hour)
	store.SetClock(func() time.Time { return t1 })
	open := url.Values{}
	open.Set("csrf", csrf)
	open.Set("view", "morning")
	open.Set("intent", "open_view")
	open.Set("generation", "1")
	open.Set("sequence", "1")
	code, _, body = postForm(t, client, origin, "/investigate/search", open, false)
	text := string(body)
	if code != http.StatusOK {
		t.Fatalf("open %d %s", code, text)
	}
	want := t1.Add(-15 * time.Minute).Format(time.RFC3339Nano)
	old := t0.Add(-15 * time.Minute).Format(time.RFC3339Nano)
	if !strings.Contains(text, want) {
		t.Fatalf("opened view did not move the window to %s", want)
	}
	if strings.Contains(text, old) {
		t.Fatal("opened view kept the original absolute window")
	}
}

func TestAddTermControl(t *testing.T) {
	_, ts, readTok, _, _ := newTestApp(t)
	origin := "http://" + ts.Listener.Addr().String()
	client, csrf := login(t, ts, readTok)
	vals := url.Values{}
	vals.Set("csrf", csrf)
	vals.Set("preset", "15m")
	vals.Set("severity", "1")
	vals.Set("source", "src_demo_api")
	vals.Add("term", "alpha")
	vals.Set("intent", "add_term")
	vals.Set("generation", "1")
	vals.Set("sequence", "1")
	code, _, body := postForm(t, client, origin, "/investigate/search", vals, false)
	if code != http.StatusOK || !strings.Contains(string(body), `id="msg-term-2"`) {
		t.Fatalf("add term %d", code)
	}
}

func oneEvent(t *testing.T, n int, when time.Time, message, attrs string) model.Batch {
	t.Helper()
	b := model.Batch{
		Version: 0,
		BatchID: fmt.Sprintf("%08x-0000-4000-8000-%012d", n, n),
		Events: []model.Event{{
			EventID:        fmt.Sprintf("%08x-0000-4000-8000-%012d", n+200, n),
			EventTime:      when.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano),
			SeverityNumber: 9,
			SeverityText:   "INFO",
			Message:        message,
			Attributes:     json.RawMessage(attrs),
		}},
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	out, err := model.ValidateBatch(raw, when.UTC())
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	return out
}

func nativeEvents(t *testing.T, batchN, count int, when time.Time, message, attrs string) model.Batch {
	t.Helper()
	events := make([]model.Event, count)
	when = when.UTC().Truncate(time.Microsecond)
	for i := range events {
		events[i] = model.Event{
			EventID:        fmt.Sprintf("%08x-0000-4000-8000-%012x", batchN, i+1),
			EventTime:      when.Format(time.RFC3339Nano),
			SeverityNumber: 9,
			SeverityText:   "INFO",
			Message:        message,
			Attributes:     json.RawMessage(attrs),
		}
	}
	raw, err := json.Marshal(model.Batch{
		Version: model.NativeVersion,
		BatchID: fmt.Sprintf("%08x-0000-4000-8000-%012x", batchN+100, 1),
		Events:  events,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := model.ValidateNative(raw)
	if err != nil {
		t.Fatalf("native: %v", err)
	}
	return out
}

func maxFilterQuery(t *testing.T, frozen time.Time, val string) model.Query {
	t.Helper()
	term := strings.Repeat("a", 128)
	terms := make([]string, 8)
	for i := range terms {
		terms[i] = term
	}
	raw, err := json.Marshal(val)
	if err != nil {
		t.Fatal(err)
	}
	preds := make([]model.Predicate, 8)
	for i := range preds {
		preds[i] = model.Predicate{Path: []string{fmt.Sprintf("p%d", i)}, Op: "eq", Type: "string", Value: raw}
	}
	return model.Query{
		Sources: []string{"src_demo_api"}, From: frozen.Add(-time.Hour).Format(time.RFC3339Nano),
		To: frozen.Add(time.Second).Format(time.RFC3339Nano), MinSeverity: 1,
		Terms: terms, Predicates: preds, Limit: 200,
	}
}

func predAttrs(val string) string {
	m := map[string]string{}
	for i := 0; i < 8; i++ {
		m[fmt.Sprintf("p%d", i)] = val
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func filterValues(csrf string, q model.Query) url.Values {
	v := url.Values{}
	v.Set("csrf", csrf)
	v.Set("preset", "custom")
	v.Set("from", q.From)
	v.Set("to", q.To)
	v.Set("severity", "1")
	v.Set("generation", "2")
	v.Set("sequence", "2")
	for _, s := range q.Sources {
		v.Add("source", s)
	}
	for _, term := range q.Terms {
		v.Add("term", term)
	}
	for _, p := range q.Predicates {
		var s string
		_ = json.Unmarshal(p.Value, &s)
		v.Add("path", strings.Join(p.Path, "."))
		v.Add("op", p.Op)
		v.Add("ptype", p.Type)
		v.Add("pval", s)
	}
	return v
}
