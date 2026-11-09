package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"example.invalid/hearthlog/internal/model"
	"example.invalid/hearthlog/internal/storage/sqlite"
)

const (
	copyReady        = "Ready to investigate"
	copyTooBroad     = "Query too broad. Narrow the time window, then run Search."
	copyExpired      = "Results expired. Rerun query."
	copyTimeout      = "Query timed out. Narrow the time window, then run Search."
	copyRowExpired   = "Row expired. Rerun query. This row is past the retention window."
	copyNotFound     = "Event not found"
	copyInvalid      = "Query is invalid"
	copyUnsupported  = "Attribute path not supported"
	copyNoEvents     = "No events in this window"
	copyNoMatches    = "No matching events"
	copyResults      = "Results"
	copyDetail       = "Event details"
	copySaved        = "View saved"
	copyDeleted      = "View deleted"
	copySaveRelative = "Saved views keep a relative range. Choose 5m, 15m, 1h, or 24h."
	copyViewLimit    = "Too many saved views. Delete one, then save again."
	copyBusy         = "Service busy. Wait, then run Search again."
	copySearching    = "Searching..."
)

var relativePresets = map[string]time.Duration{
	"5m":  5 * time.Minute,
	"15m": 15 * time.Minute,
	"1h":  time.Hour,
	"24h": 24 * time.Hour,
}

const busyPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="referrer" content="strict-origin">
<title>Hearthlog</title>
<link rel="stylesheet" href="/assets/style.css">
</head>
<body>
<main id="investigation" tabindex="-1">
<p id="status" aria-live="polite" tabindex="-1" data-code="busy">Service busy. Wait, then run Search again.</p>
<p><a href="/investigate">Back to investigate</a></p>
</main>
</body>
</html>
`

func (s *Server) now() time.Time {
	return s.store.Now().UTC().Truncate(time.Microsecond)
}

func demoScope() model.Scope {
	return model.Scope{WorkspaceID: demoWorkspace}
}

func (s *Server) investigateGET(w http.ResponseWriter, r *http.Request) {
	csrf, ok := s.readSession(r)
	if !ok {
		safeError(w, http.StatusUnauthorized, "unauthenticated", "Authentication required", false)
		return
	}
	v := s.blankView(r, csrf)
	if len(v.Sources) == 0 {
		v.Status = "No sources yet"
	}
	s.render(w, r, "page", v, http.StatusOK)
}

func (s *Server) searchPOST(w http.ResponseWriter, r *http.Request) {
	csrf, ok := s.form(w, r)
	if !ok {
		return
	}
	f, err := parseUI(r)
	if err != nil {
		s.fail(w, r, csrf, f, http.StatusBadRequest, "invalid_query", copyInvalid)
		return
	}
	switch {
	case f.Intent == "clear":
		v := s.blankView(r, csrf)
		s.render(w, r, "page", v, http.StatusOK)
		return
	case f.Intent == "add_term":
		if len(f.Terms) < model.MaxTerms {
			f.Terms = append(f.Terms, "")
		}
		s.renderEditor(w, r, csrf, f, copyReady)
		return
	case strings.HasPrefix(f.Intent, "remove_term:"):
		if i, ok := intentIndex(f.Intent); ok {
			f.Terms = dropIndex(f.Terms, i)
		}
		s.renderEditor(w, r, csrf, f, copyReady)
		return
	case f.Intent == "add_pred":
		if len(f.Preds) < model.MaxPredicates {
			f.Preds = append(f.Preds, predInput{Op: "eq", Type: "string"})
		}
		s.renderEditor(w, r, csrf, f, copyReady)
		return
	case strings.HasPrefix(f.Intent, "remove_pred:"):
		if i, ok := intentIndex(f.Intent); ok {
			f.Preds = dropPred(f.Preds, i)
		}
		s.renderEditor(w, r, csrf, f, copyReady)
		return
	case f.Intent == "delete_view":
		s.deleteView(w, r, csrf, f)
		return
	case f.Intent == "save_view":
		s.saveView(w, r, csrf, f)
		return
	case f.Intent == "open_view":
		s.openView(w, r, csrf, f)
		return
	default:
		s.runSearch(w, r, csrf, f)
	}
}

func (s *Server) detailPOST(w http.ResponseWriter, r *http.Request) {
	csrf, ok := s.form(w, r)
	if !ok {
		return
	}
	f, err := parseUI(r)
	if err != nil || f.ID == "" {
		s.fail(w, r, csrf, f, http.StatusBadRequest, "invalid_query", copyInvalid)
		return
	}
	q, err := s.queryFor(r, f)
	if err != nil {
		s.writeQueryError(w, r, csrf, f, err)
		return
	}
	rec, err := s.store.Detail(r.Context(), demoScope(), f.ID)
	if err != nil {
		if errors.Is(err, sqlite.ErrRowExpired) {
			s.fail(w, r, csrf, f, http.StatusNotFound, "row_expired", copyRowExpired)
			return
		}
		if errors.Is(err, sqlite.ErrNotFound) {
			s.fail(w, r, csrf, f, http.StatusNotFound, "not_found", copyNotFound)
			return
		}
		if errors.Is(err, sqlite.ErrQueryTimeout) {
			s.fail(w, r, csrf, f, http.StatusGatewayTimeout, "query_timeout", copyTimeout)
			return
		}
		safeError(w, http.StatusServiceUnavailable, "storage_unavailable", "Storage unavailable", true)
		return
	}
	v := s.viewFromQuery(r, csrf, f, q)
	v.Detail = &rec
	v.Attrs = walkAttrs(rec.Attributes)
	v.PageCursor = f.Cursor
	v.Status = copyDetail
	v.Code = "detail"
	v.Focus = "detail"
	v.Selection = f.ID
	v.OriginID = f.ID
	v.SuppressResults = false
	s.render(w, r, "detail", v, http.StatusOK)
}

func (s *Server) runSearch(w http.ResponseWriter, r *http.Request, csrf string, f uiForm) {
	q, err := s.queryFor(r, f)
	if err != nil {
		s.writeQueryError(w, r, csrf, f, err)
		return
	}
	page, err := s.store.ReadPage(r.Context(), demoScope(), q)
	if err != nil {
		if errors.Is(err, sqlite.ErrCursorExpired) {
			// The cursor carried the filters. Put them back so Search can rerun.
			f.From = q.From
			f.To = q.To
			f.Sources = append([]string(nil), q.Sources...)
			f.Severity = strconv.Itoa(q.MinSeverity)
			f.Terms = append([]string(nil), q.Terms...)
			f.Preds = inputsFromPreds(q.Predicates)
			f.Preset = "custom"
			f.Cursor = ""
		}
		s.writeQueryError(w, r, csrf, f, err)
		return
	}
	v := s.viewFromQuery(r, csrf, f, q)
	v.Rows = page.Rows
	v.NextCursor = page.Next
	v.PageCursor = f.Cursor
	v.Status = copyResults
	v.Code = "results"
	v.Focus = "results"
	v.OriginID = f.Origin
	if len(page.Rows) == 0 {
		has, herr := s.store.HasRetainedLogs(r.Context(), demoWorkspace)
		if herr != nil {
			safeError(w, http.StatusServiceUnavailable, "storage_unavailable", "Storage unavailable", true)
			return
		}
		if !has {
			v.NoEvents = true
			v.Status = copyNoEvents
		} else {
			v.Status = copyNoMatches
		}
	}
	s.render(w, r, "results", v, http.StatusOK)
}

func (s *Server) queryFor(r *http.Request, f uiForm) (model.Query, error) {
	if f.cursorOnly() {
		return s.store.FiltersFromCursor(r.Context(), demoScope(), f.Cursor)
	}
	return s.queryFromForm(f)
}

func (s *Server) queryFromForm(f uiForm) (model.Query, error) {
	sev := 1
	if f.Severity != "" {
		n, err := strconv.Atoi(f.Severity)
		if err != nil {
			return model.Query{}, errForm
		}
		sev = n
	}
	preds, err := f.searchPreds()
	if err != nil {
		return model.Query{}, err
	}
	from, to := f.From, f.To
	if d, ok := relativePresets[f.Preset]; ok && f.Cursor == "" {
		// A fresh relative search evaluates the window at this request.
		// Detail, back, and filter-on-value send preset=custom with the frozen window.
		now := s.now()
		from = now.Add(-d).Format(time.RFC3339Nano)
		to = now.Add(time.Second).Format(time.RFC3339Nano)
	} else if f.Preset != "" && f.Preset != "custom" {
		return model.Query{}, errForm
	}
	q := model.Query{
		Sources:     append([]string(nil), f.Sources...),
		From:        from,
		To:          to,
		MinSeverity: sev,
		Terms:       f.searchTerms(),
		Predicates:  preds,
		Limit:       200,
		Cursor:      f.Cursor,
	}
	if err := model.ValidateQuery(q); err != nil {
		return model.Query{}, err
	}
	return q, nil
}

func (s *Server) writeQueryError(w http.ResponseWriter, r *http.Request, csrf string, f uiForm, err error) {
	switch {
	case errors.Is(err, model.ErrUnsupportedPath):
		s.fail(w, r, csrf, f, http.StatusBadRequest, "unsupported_path", copyUnsupported)
	case errors.Is(err, sqlite.ErrTooBroad):
		s.fail(w, r, csrf, f, http.StatusUnprocessableEntity, "query_too_broad", copyTooBroad)
	case errors.Is(err, sqlite.ErrQueryTimeout):
		s.fail(w, r, csrf, f, http.StatusGatewayTimeout, "query_timeout", copyTimeout)
	case errors.Is(err, sqlite.ErrCursorExpired):
		f.Cursor = ""
		s.fail(w, r, csrf, f, http.StatusGone, "cursor_expired", copyExpired)
	case errors.Is(err, sqlite.ErrCursorScope):
		safeError(w, http.StatusForbidden, "forbidden", "Forbidden", false)
	case errors.Is(err, sqlite.ErrCursorInvalid), errors.Is(err, model.ErrValidation), errors.Is(err, errForm):
		f.Cursor = ""
		s.fail(w, r, csrf, f, http.StatusBadRequest, "invalid_query", copyInvalid)
	default:
		safeError(w, http.StatusServiceUnavailable, "storage_unavailable", "Storage unavailable", true)
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, csrf string, f uiForm, status int, code, msg string) {
	w.Header().Set("X-Notice", code)
	v := s.viewFromForm(r, csrf, f)
	v.Status = msg
	v.Code = code
	v.Focus = "status"
	v.SuppressResults = true
	v.NextCursor = ""
	v.PageCursor = ""
	v.Query.Cursor = ""
	if r.Header.Get("HX-Request") == "true" {
		writeStatusFragment(w, v, status)
		return
	}
	s.render(w, r, "page", v, status)
}

func writeStatusFragment(w http.ResponseWriter, v view, status int) {
	w.Header().Set("X-Notice", v.Code)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = tmpl.ExecuteTemplate(w, "status", v)
}

func (s *Server) renderEditor(w http.ResponseWriter, r *http.Request, csrf string, f uiForm, status string) {
	v := s.viewFromForm(r, csrf, f)
	v.Status = status
	v.Focus = "source"
	v.SuppressResults = true
	s.render(w, r, "page", v, http.StatusOK)
}

func (s *Server) saveView(w http.ResponseWriter, r *http.Request, csrf string, f uiForm) {
	if _, ok := relativePresets[f.Preset]; !ok {
		s.renderEditor(w, r, csrf, f, copySaveRelative)
		return
	}
	if !viewNameRe.MatchString(f.ViewName) {
		s.fail(w, r, csrf, f, http.StatusBadRequest, "invalid_query", copyInvalid)
		return
	}
	q, err := s.queryFromForm(f)
	if err != nil {
		s.writeQueryError(w, r, csrf, f, err)
		return
	}
	err = s.store.SaveView(r.Context(), demoWorkspace, sqlite.SavedView{
		Name:        f.ViewName,
		Preset:      f.Preset,
		MinSeverity: q.MinSeverity,
		Sources:     q.Sources,
		Terms:       q.Terms,
		Predicates:  q.Predicates,
	})
	if err != nil {
		if errors.Is(err, sqlite.ErrViewLimit) {
			s.renderEditor(w, r, csrf, f, copyViewLimit)
			return
		}
		safeError(w, http.StatusServiceUnavailable, "storage_unavailable", "Storage unavailable", true)
		return
	}
	s.renderEditor(w, r, csrf, f, copySaved)
}

func (s *Server) deleteView(w http.ResponseWriter, r *http.Request, csrf string, f uiForm) {
	if !viewNameRe.MatchString(f.ViewName) {
		s.fail(w, r, csrf, f, http.StatusBadRequest, "invalid_query", copyInvalid)
		return
	}
	if err := s.store.DeleteView(r.Context(), demoWorkspace, f.ViewName); err != nil {
		safeError(w, http.StatusServiceUnavailable, "storage_unavailable", "Storage unavailable", true)
		return
	}
	f.ViewName = ""
	s.renderEditor(w, r, csrf, f, copyDeleted)
}

func (s *Server) openView(w http.ResponseWriter, r *http.Request, csrf string, f uiForm) {
	if !viewNameRe.MatchString(f.ViewName) {
		s.fail(w, r, csrf, f, http.StatusBadRequest, "invalid_query", copyInvalid)
		return
	}
	views, err := s.store.ListViews(r.Context(), demoWorkspace)
	if err != nil {
		safeError(w, http.StatusServiceUnavailable, "storage_unavailable", "Storage unavailable", true)
		return
	}
	var found *sqlite.SavedView
	for i := range views {
		if views[i].Name == f.ViewName {
			found = &views[i]
			break
		}
	}
	if found == nil {
		s.fail(w, r, csrf, f, http.StatusBadRequest, "invalid_query", copyInvalid)
		return
	}
	f.Preset = found.Preset
	f.Sources = append([]string(nil), found.Sources...)
	f.Terms = append([]string(nil), found.Terms...)
	f.Preds = inputsFromPreds(found.Predicates)
	f.Severity = strconv.Itoa(found.MinSeverity)
	f.Cursor = ""
	f.From = ""
	f.To = ""
	s.runSearch(w, r, csrf, f)
}

func (s *Server) blankView(r *http.Request, csrf string) view {
	now := s.now()
	f := uiForm{
		Preset:   "15m",
		Severity: "1",
		Sources:  []string{"src_demo_api"},
		Terms:    []string{""},
		Preds:    []predInput{{Op: "eq", Type: "string"}},
	}
	q := model.Query{
		Sources:     []string{"src_demo_api"},
		From:        now.Add(-15 * time.Minute).Format(time.RFC3339Nano),
		To:          now.Add(time.Second).Format(time.RFC3339Nano),
		MinSeverity: 1,
		Limit:       200,
	}
	v := s.viewFromQuery(r, csrf, f, q)
	v.Status = copyReady
	v.Focus = "source"
	v.SuppressResults = true
	return v
}

func (s *Server) viewFromForm(r *http.Request, csrf string, f uiForm) view {
	sev := 1
	if n, err := strconv.Atoi(f.Severity); err == nil {
		sev = n
	}
	preset := f.Preset
	if preset == "" {
		preset = "custom"
	}
	from, to := f.From, f.To
	if d, ok := relativePresets[preset]; ok && from == "" && to == "" {
		now := s.now()
		from = now.Add(-d).Format(time.RFC3339Nano)
		to = now.Add(time.Second).Format(time.RFC3339Nano)
	}
	preds, _ := f.searchPreds()
	q := model.Query{
		Sources:     append([]string(nil), f.Sources...),
		From:        from,
		To:          to,
		MinSeverity: sev,
		Terms:       f.searchTerms(),
		Predicates:  preds,
		Limit:       200,
	}
	v := s.viewFromQuery(r, csrf, f, q)
	v.TermFields = append([]string(nil), f.Terms...)
	v.PredFields = append([]predInput(nil), f.Preds...)
	s.ensureSlots(&v)
	return v
}

func (s *Server) viewFromQuery(r *http.Request, csrf string, f uiForm, q model.Query) view {
	preset := f.Preset
	if preset == "" {
		preset = "custom"
	}
	v := view{
		CSRF:       csrf,
		Query:      q,
		Preset:     preset,
		Generation: f.Generation,
		Sequence:   f.Sequence,
		Status:     copyReady,
	}
	if v.Generation == "" {
		v.Generation = "1"
	}
	if v.Sequence == "" {
		v.Sequence = "1"
	}
	if q.From != "" && q.To != "" {
		v.Window = formatUTC(q.From) + " to " + formatUTC(q.To) + " UTC"
	}
	v.TermFields = append([]string(nil), q.Terms...)
	v.PredFields = inputsFromPreds(q.Predicates)
	s.ensureSlots(&v)
	s.attachCatalog(r, &v)
	return v
}

func (s *Server) ensureSlots(v *view) {
	if len(v.TermFields) == 0 {
		v.TermFields = []string{""}
	}
	if len(v.PredFields) == 0 {
		v.PredFields = []predInput{{Op: "eq", Type: "string"}}
	}
}

func (s *Server) attachCatalog(r *http.Request, v *view) {
	sources, err := s.store.ListSources(r.Context(), demoWorkspace)
	if err == nil {
		v.SourcesOK = true
		for _, src := range sources {
			v.Sources = append(v.Sources, sourceInfo{ID: src.ID, Service: src.Service, Environment: src.Environment})
		}
	}
	views, err := s.store.ListViews(r.Context(), demoWorkspace)
	if err == nil {
		for _, sv := range views {
			v.Saved = append(v.Saved, savedInfo{Name: sv.Name, Preset: sv.Preset})
		}
	}
}

func formatUTC(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

func intentIndex(intent string) (int, bool) {
	i := strings.LastIndex(intent, ":")
	if i < 0 || i+1 >= len(intent) {
		return 0, false
	}
	n, err := strconv.Atoi(intent[i+1:])
	if err != nil || n < 0 || n > 7 {
		return 0, false
	}
	return n, true
}

func dropIndex(in []string, i int) []string {
	if i < 0 || i >= len(in) {
		return in
	}
	out := append([]string(nil), in[:i]...)
	return append(out, in[i+1:]...)
}

func dropPred(in []predInput, i int) []predInput {
	if i < 0 || i >= len(in) {
		return in
	}
	out := append([]predInput(nil), in[:i]...)
	return append(out, in[i+1:]...)
}
