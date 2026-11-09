package server

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"example.invalid/hearthlog/internal/model"
	"example.invalid/hearthlog/internal/storage/sqlite"
)

const (
	copyLiveOff       = "Live is off."
	copyLiveOn        = "Live: Receiving"
	copyLivePaused    = "Live: Paused"
	copyLiveGap       = "Live: Gap detected"
	copyLiveDisc      = "Live: Disconnected"
	copyLiveThrottled = "Live: Throttled"
	copyLiveInvalid   = "Live: Query invalid"
	copyLiveError     = "Live: Error"
	copyLiveNoJS      = "Live requires JavaScript. Use Refresh for historical search."

	liveSlotMax = 2
	liveSlotTTL = 15 * time.Second
)

var liveClientRe = regexp.MustCompile(`^[a-z0-9]{8,32}$`)

type liveHub struct {
	mu sync.Mutex
	m  map[string]map[string]time.Time
}

func (h *liveHub) allow(ws, client string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.m == nil {
		h.m = map[string]map[string]time.Time{}
	}
	slots := h.m[ws]
	if slots == nil {
		slots = map[string]time.Time{}
		h.m[ws] = slots
	}
	for id, seen := range slots {
		if now.Sub(seen) > liveSlotTTL {
			delete(slots, id)
		}
	}
	if _, ok := slots[client]; ok {
		slots[client] = now
		return true
	}
	if len(slots) >= liveSlotMax {
		return false
	}
	slots[client] = now
	return true
}

func (s *Server) livePOST(w http.ResponseWriter, r *http.Request) {
	csrf, ok := s.liveAuth(w, r)
	if !ok {
		return
	}
	intent := r.PostFormValue("intent")
	switch intent {
	case "start", "poll", "resume":
	default:
		s.liveFail(w, r, http.StatusBadRequest, "invalid", copyLiveInvalid)
		return
	}
	cur, err := liveCursorFromForm(r)
	if err != nil {
		s.liveFail(w, r, http.StatusBadRequest, "invalid", copyLiveInvalid)
		return
	}
	gen := r.PostFormValue("live_gen")
	if gen == "" {
		gen = "1"
	}
	if !validLiveGen(gen) {
		s.liveFail(w, r, http.StatusBadRequest, "invalid", copyLiveInvalid)
		return
	}
	client := r.PostFormValue("client")
	if !liveClientRe.MatchString(client) {
		s.liveFail(w, r, http.StatusBadRequest, "invalid", copyLiveInvalid)
		return
	}
	f, err := parseUI(r)
	if err != nil {
		s.liveFail(w, r, http.StatusBadRequest, "invalid", copyLiveInvalid)
		return
	}
	filter, err := s.liveFilter(f)
	if err != nil {
		code := "invalid"
		status := http.StatusBadRequest
		if errors.Is(err, model.ErrUnsupportedPath) {
			code = "invalid"
		}
		s.liveFail(w, r, status, code, copyLiveInvalid)
		return
	}
	if !s.live.allow(demoWorkspace, client, s.now()) {
		w.Header().Set("Retry-After", "5")
		s.liveFail(w, r, http.StatusTooManyRequests, "throttled", copyLiveThrottled)
		return
	}
	switch intent {
	case "start":
		s.liveStart(w, r, csrf, gen)
	case "resume":
		s.livePoll(w, r, csrf, gen, cur, filter, true)
	default:
		s.livePoll(w, r, csrf, gen, cur, filter, false)
	}
}

func (s *Server) liveStart(w http.ResponseWriter, r *http.Request, csrf, gen string) {
	cur, _, err := s.store.LiveTail(r.Context(), demoScope())
	if err != nil {
		s.writeLiveStoreErr(w, r, err)
		return
	}
	v := s.blankView(r, csrf)
	v.LiveReceived = strconv.FormatInt(cur.ReceivedAt, 10)
	v.LiveIngest = strconv.FormatInt(cur.IngestID, 10)
	v.Generation = gen
	v.Sequence = "1"
	if r.Header.Get("HX-Request") != "true" {
		v.LiveStatus = copyLiveNoJS
		v.LiveState = "off"
		v.Status = copyLiveNoJS
		s.render(w, r, "page", v, http.StatusOK)
		return
	}
	v.LiveStatus = copyLiveOn
	v.LiveState = "receiving"
	s.writeLive(w, r, "live-ack", v, http.StatusOK)
}

func (s *Server) livePoll(w http.ResponseWriter, r *http.Request, csrf, gen string, cur sqlite.LiveCursor, filter sqlite.LiveFilter, resume bool) {
	page, err := s.store.Poll(r.Context(), demoScope(), cur, filter, resume)
	if err != nil {
		s.writeLiveStoreErr(w, r, err)
		return
	}
	if page.Gap {
		s.writeLiveGap(w, r, csrf, page)
		return
	}
	v := s.blankView(r, csrf)
	v.Rows = page.Rows
	v.LiveReceived = strconv.FormatInt(page.Cursor.ReceivedAt, 10)
	v.LiveIngest = strconv.FormatInt(page.Cursor.IngestID, 10)
	v.LiveIdle = page.Idle
	v.LiveState = "receiving"
	v.LiveStatus = copyLiveOn
	v.Generation = gen
	v.Sequence = "1"
	s.attachCatalog(r, &v)
	if r.Header.Get("HX-Request") != "true" {
		v.Status = copyLiveNoJS
		v.LiveStatus = copyLiveNoJS
		v.SuppressResults = true
		s.render(w, r, "page", v, http.StatusOK)
		return
	}
	s.writeLive(w, r, "live-batch", v, http.StatusOK)
}

func (s *Server) writeLiveStoreErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, sqlite.ErrQueryTimeout):
		w.Header().Set("Retry-After", "5")
		s.liveFail(w, r, http.StatusGatewayTimeout, "disconnected", copyLiveDisc)
	case errors.Is(err, sqlite.ErrTooBroad):
		s.liveFail(w, r, http.StatusUnprocessableEntity, "invalid", copyLiveInvalid)
	default:
		w.Header().Set("Retry-After", "5")
		s.liveFail(w, r, http.StatusServiceUnavailable, "throttled", copyLiveThrottled)
	}
}

func (s *Server) writeLiveGap(w http.ResponseWriter, r *http.Request, csrf string, page sqlite.LivePage) {
	from, to := gapWindow(page.GapFrom, page.GapTo, s.now())
	w.Header().Set("X-Live-Gap-From", from)
	w.Header().Set("X-Live-Gap-To", to)
	w.Header().Set("X-Live-Status", "gap")
	if r.Header.Get("HX-Request") != "true" {
		v := s.blankView(r, csrf)
		v.Status = copyLiveGap
		v.LiveStatus = copyLiveGap
		v.LiveState = "gap"
		v.Code = "gap"
		v.Preset = "custom"
		v.Query.From = from
		v.Query.To = to
		s.render(w, r, "page", v, http.StatusGone)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusGone)
	w.Write([]byte(`<p id="live-gap">Live: Gap detected</p>`))
}

func gapWindow(fromMicros, toMicros int64, now time.Time) (string, string) {
	end := now.UTC()
	if toMicros > 0 {
		end = time.UnixMicro(toMicros).UTC()
	}
	start := end.Add(-liveCatchupWindow)
	if fromMicros > 0 {
		start = time.UnixMicro(fromMicros).UTC()
	}
	if !start.Before(end) {
		start = end.Add(-time.Second)
	}
	if end.Sub(start) > model.MaxWindow {
		start = end.Add(-model.MaxWindow)
	}
	return start.Format(time.RFC3339Nano), end.Add(time.Second).Format(time.RFC3339Nano)
}

const liveCatchupWindow = 30 * time.Second

func (s *Server) writeLive(w http.ResponseWriter, r *http.Request, kind string, v view, status int) {
	w.Header().Set("X-Live-Gen", v.Generation)
	w.Header().Set("X-Live-Cursor-Received", v.LiveReceived)
	w.Header().Set("X-Live-Cursor-Id", v.LiveIngest)
	if v.LiveIdle {
		w.Header().Set("X-Live-Idle", "1")
	} else {
		w.Header().Set("X-Live-Idle", "0")
	}
	w.Header().Set("X-Live-Status", v.LiveState)
	s.render(w, r, kind, v, status)
}

func (s *Server) liveFail(w http.ResponseWriter, r *http.Request, status int, state, msg string) {
	w.Header().Set("X-Live-Status", state)
	w.Header().Set("Cache-Control", "no-store")
	if r != nil && r.Header.Get("HX-Request") != "true" && status == http.StatusBadRequest {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		w.Write([]byte(`<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>Hearthlog</title></head><body><main id="investigation"><p id="live-status">` + msg + `</p><p>` + copyLiveNoJS + `</p></main></body></html>`))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write([]byte(`<p id="live-status">` + msg + `</p>`))
}

func (s *Server) liveFilter(f uiForm) (sqlite.LiveFilter, error) {
	sev := 1
	if f.Severity != "" {
		n, err := strconv.Atoi(f.Severity)
		if err != nil {
			return sqlite.LiveFilter{}, errForm
		}
		sev = n
	}
	preds, err := f.searchPreds()
	if err != nil {
		return sqlite.LiveFilter{}, err
	}
	now := s.now()
	q := model.Query{
		Sources:     append([]string(nil), f.Sources...),
		From:        now.Add(-time.Minute).Format(time.RFC3339Nano),
		To:          now.Add(time.Minute).Format(time.RFC3339Nano),
		MinSeverity: sev,
		Terms:       f.searchTerms(),
		Predicates:  preds,
		Limit:       200,
	}
	if err := model.ValidateQuery(q); err != nil {
		return sqlite.LiveFilter{}, err
	}
	return sqlite.LiveFilter{
		Sources:     q.Sources,
		MinSeverity: q.MinSeverity,
		Terms:       q.Terms,
		Predicates:  q.Predicates,
	}, nil
}

func liveCursorFromForm(r *http.Request) (sqlite.LiveCursor, error) {
	recv, err := parseCursorField(r.PostFormValue("cursor_received"))
	if err != nil {
		return sqlite.LiveCursor{}, err
	}
	id, err := parseCursorField(r.PostFormValue("cursor_id"))
	if err != nil {
		return sqlite.LiveCursor{}, err
	}
	return sqlite.LiveCursor{ReceivedAt: recv, IngestID: id}, nil
}

func parseCursorField(s string) (int64, error) {
	if s == "" || s == "0" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || strconv.FormatInt(n, 10) != s {
		return 0, errForm
	}
	return n, nil
}

func validLiveGen(g string) bool {
	n, err := strconv.ParseUint(g, 10, 64)
	if err != nil || g[0] == '0' || n < 1 || n > 1000000000 {
		return false
	}
	return true
}

func (s *Server) liveAuth(w http.ResponseWriter, r *http.Request) (string, bool) {
	csrf, ok := s.readSession(r)
	if !ok {
		safeError(w, http.StatusUnauthorized, "unauthenticated", "Authentication required", false)
		return "", false
	}
	if r.Method != http.MethodPost {
		safeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", false)
		return "", false
	}
	if r.Header.Get("Origin") != s.origin {
		safeError(w, http.StatusForbidden, "forbidden", "Forbidden", false)
		return "", false
	}
	if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		safeError(w, http.StatusUnsupportedMediaType, "unsupported_media", "Unsupported media type", false)
		return "", false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		safeError(w, http.StatusBadRequest, "invalid_query", "Query validation failed", false)
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(csrf)) != 1 {
		safeError(w, http.StatusForbidden, "forbidden", "Forbidden", false)
		return "", false
	}
	allowed := map[string]bool{
		"csrf": true, "intent": true, "client": true, "live_gen": true,
		"cursor_received": true, "cursor_id": true, "severity": true,
		"source": true, "term": true, "path": true, "op": true, "ptype": true, "pval": true,
	}
	multi := map[string]bool{
		"term": true, "source": true, "path": true, "op": true, "ptype": true, "pval": true,
	}
	for k := range r.PostForm {
		if !allowed[k] {
			safeError(w, http.StatusBadRequest, "invalid_query", "Query validation failed", false)
			return "", false
		}
	}
	for k, vals := range r.PostForm {
		if len(vals) > 1 && !multi[k] {
			safeError(w, http.StatusBadRequest, "invalid_query", "Query validation failed", false)
			return "", false
		}
	}
	return csrf, true
}
