package server

import (
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
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

	liveSlotMax     = 2
	liveSlotTTL     = 15 * time.Second
	liveMinInterval = 200 * time.Millisecond
)

var liveClientRe = regexp.MustCompile(`^[a-z0-9]{8,32}$`)

// livePace is the clock for the per-slot minimum interval. It stays unset in
// a normal process, and paceNow then uses time.Now. Tests in this package
// store a func() time.Time. Slot expiry uses the store clock, not this one.
var livePace atomic.Value

// liveGate blocks inside a live request after the slot is marked busy.
// It stays nil in a normal process. Tests store a func() to hold one
// request in flight.
var liveGate atomic.Pointer[func()]

func paceNow() time.Time {
	if fn, ok := livePace.Load().(func() time.Time); ok && fn != nil {
		return fn()
	}
	return time.Now()
}

type liveSlot struct {
	session string
	seen    time.Time
	last    time.Time
	busy    bool
	behind  bool
	asOf    int64
}

type liveBegin struct {
	asOf   int64
	status int
}

type liveHub struct {
	mu sync.Mutex
	m  map[string]map[string]*liveSlot
}

func (h *liveHub) begin(ws, session, client string, now, pace time.Time, start bool) liveBegin {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.m == nil {
		h.m = map[string]map[string]*liveSlot{}
	}
	slots := h.m[ws]
	if slots == nil {
		slots = map[string]*liveSlot{}
		h.m[ws] = slots
	}
	// One process has one session. A replaced login must not keep the
	// previous session's slots, or the same id is forbidden and a new id
	// fills the cap until the TTL.
	for id, slot := range slots {
		if slot.session != session || (!slot.busy && now.Sub(slot.seen) > liveSlotTTL) {
			delete(slots, id)
		}
	}
	slot := slots[client]
	if slot == nil {
		if start {
			evictOwnSlot(slots, session)
		}
		if len(slots) >= liveSlotMax {
			return liveBegin{status: http.StatusTooManyRequests}
		}
		slot = &liveSlot{session: session}
		slots[client] = slot
	}
	if slot.busy {
		return liveBegin{status: http.StatusTooManyRequests}
	}
	if !start && !slot.behind && !slot.last.IsZero() && pace.Sub(slot.last) < liveMinInterval {
		return liveBegin{status: http.StatusTooManyRequests}
	}
	slot.busy = true
	slot.seen = now
	slot.behind = false
	if !start {
		slot.last = pace
	}
	return liveBegin{asOf: slot.asOf, status: http.StatusOK}
}

func evictOwnSlot(slots map[string]*liveSlot, session string) {
	if len(slots) < liveSlotMax {
		return
	}
	var oldestID string
	var oldest time.Time
	for id, slot := range slots {
		if slot.session != session || slot.busy {
			continue
		}
		if oldestID == "" || slot.seen.Before(oldest) {
			oldestID = id
			oldest = slot.seen
		}
	}
	if oldestID != "" {
		delete(slots, oldestID)
	}
}

func (h *liveHub) finish(ws, session, client string, behind bool, asOf int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	slots := h.m[ws]
	if slots == nil {
		return
	}
	slot := slots[client]
	if slot == nil || slot.session != session {
		return
	}
	slot.busy = false
	slot.behind = behind
	if asOf > slot.asOf {
		slot.asOf = asOf
	}
}

func (h *liveHub) stop(ws, session, client string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	slots := h.m[ws]
	if slots == nil {
		return
	}
	slot := slots[client]
	if slot == nil || slot.session != session {
		return
	}
	delete(slots, client)
}

func (s *Server) sessionKey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var zero [32]byte
	if s.sessionHash == zero {
		return ""
	}
	return hex.EncodeToString(s.sessionHash[:])
}

func (s *Server) livePOST(w http.ResponseWriter, r *http.Request) {
	csrf, ok := s.liveAuth(w, r)
	if !ok {
		return
	}
	intent := r.PostFormValue("intent")
	switch intent {
	case "start", "poll", "resume", "stop":
	default:
		s.liveFail(w, r, http.StatusBadRequest, "invalid", copyLiveInvalid)
		return
	}
	if intent == "stop" {
		client := r.PostFormValue("client")
		if !liveClientRe.MatchString(client) {
			s.liveFail(w, r, http.StatusBadRequest, "invalid", copyLiveInvalid)
			return
		}
		s.live.stop(demoWorkspace, s.sessionKey(), client)
		w.WriteHeader(http.StatusNoContent)
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
	clientAsOf, err := parseCursorField(r.PostFormValue("as_of"))
	if err != nil {
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
	session := s.sessionKey()
	begun := s.live.begin(demoWorkspace, session, client, s.now(), paceNow(), intent == "start")
	if begun.status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "5")
		s.liveFail(w, r, http.StatusTooManyRequests, "throttled", copyLiveThrottled)
		return
	}
	if begun.status != http.StatusOK {
		s.liveFail(w, r, begun.status, "invalid", copyLiveInvalid)
		return
	}
	var behind bool
	var asOfOut int64
	defer func() { s.live.finish(demoWorkspace, session, client, behind, asOfOut) }()
	if gate := liveGate.Load(); gate != nil && *gate != nil {
		(*gate)()
	}
	asOf := begun.asOf
	if asOf == 0 {
		asOf = clientAsOf
	}
	nowU := s.now().UTC().UnixMicro()
	if asOf > nowU {
		asOf = nowU
	}
	switch intent {
	case "start":
		asOfOut = s.liveStart(w, r, csrf, gen)
	case "resume":
		behind, asOfOut = s.livePoll(w, r, csrf, gen, cur, filter, asOf)
	default:
		behind, asOfOut = s.livePoll(w, r, csrf, gen, cur, filter, asOf)
	}
}

func (s *Server) liveStart(w http.ResponseWriter, r *http.Request, csrf, gen string) int64 {
	asOf := s.now().UTC().UnixMicro()
	cur, _, err := s.store.LiveTail(r.Context(), demoScope())
	if err != nil {
		s.writeLiveStoreErr(w, r, err)
		return 0
	}
	v := s.blankView(r, csrf)
	v.LiveReceived = strconv.FormatInt(cur.ReceivedAt, 10)
	v.LiveIngest = strconv.FormatInt(cur.IngestID, 10)
	v.LiveAsOf = strconv.FormatInt(asOf, 10)
	v.LiveCaught = true
	v.Generation = gen
	v.Sequence = "1"
	if r.Header.Get("HX-Request") != "true" {
		v.LiveStatus = copyLiveNoJS
		v.LiveState = "off"
		v.Status = copyLiveNoJS
		s.render(w, r, "page", v, http.StatusOK)
		return asOf
	}
	v.LiveStatus = copyLiveOn
	v.LiveState = "receiving"
	s.writeLive(w, r, "live-ack", v, http.StatusOK)
	return asOf
}

func (s *Server) livePoll(w http.ResponseWriter, r *http.Request, csrf, gen string, cur sqlite.LiveCursor, filter sqlite.LiveFilter, asOf int64) (bool, int64) {
	page, err := s.store.Poll(r.Context(), demoScope(), cur, filter, asOf)
	if err != nil {
		s.writeLiveStoreErr(w, r, err)
		return false, 0
	}
	if page.Gap {
		s.writeLiveGap(w, r, csrf, page)
		return false, 0
	}
	v := s.blankView(r, csrf)
	v.Rows = page.Rows
	v.LiveReceived = strconv.FormatInt(page.Cursor.ReceivedAt, 10)
	v.LiveIngest = strconv.FormatInt(page.Cursor.IngestID, 10)
	v.LiveAsOf = strconv.FormatInt(page.AsOf, 10)
	v.LiveCaught = page.CaughtUp
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
		return !page.CaughtUp, page.AsOf
	}
	s.writeLive(w, r, "live-batch", v, http.StatusOK)
	return !page.CaughtUp, page.AsOf
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
	if v.LiveCaught {
		w.Header().Set("X-Live-Caught", "1")
	} else {
		w.Header().Set("X-Live-Caught", "0")
	}
	if v.LiveAsOf != "" {
		w.Header().Set("X-Live-As-Of", v.LiveAsOf)
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
		"cursor_received": true, "cursor_id": true, "as_of": true, "severity": true,
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
