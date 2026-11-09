package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"example.invalid/hearthlog/internal/model"
)

var errForm = errors.New("form validation failed")

func (s *Server) form(w http.ResponseWriter, r *http.Request) (string, bool) {
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
		"csrf": true, "source": true, "from": true, "to": true,
		"severity": true, "term": true, "id": true, "generation": true,
		"sequence": true, "origin": true, "cursor": true,
		"preset": true, "intent": true, "view": true,
		"path": true, "op": true, "ptype": true, "pval": true,
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
	if intent := r.PostFormValue("intent"); intent != "" && !intentRe.MatchString(intent) {
		safeError(w, http.StatusBadRequest, "invalid_query", "Query validation failed", false)
		return "", false
	}

	if g := r.PostFormValue("generation"); g != "" {
		if n, err := strconv.ParseUint(g, 10, 64); err != nil || g[0] == '0' || g == "0" || n > 1000000000 {
			safeError(w, http.StatusBadRequest, "invalid_query", "Query validation failed", false)
			return "", false
		}
	}
	if sq := r.PostFormValue("sequence"); sq != "" {
		if n, err := strconv.ParseUint(sq, 10, 64); err != nil || sq[0] == '0' || sq == "0" || n > 1000000000 {
			safeError(w, http.StatusBadRequest, "invalid_query", "Query validation failed", false)
			return "", false
		}
	}
	if id := r.PostFormValue("id"); id != "" {
		if n, err := strconv.ParseUint(id, 10, 64); err != nil || id[0] == '0' || id == "0" || n > 9223372036854775807 {
			safeError(w, http.StatusBadRequest, "invalid_query", "Query validation failed", false)
			return "", false
		}
	}
	if o := r.PostFormValue("origin"); o != "" {
		if n, err := strconv.ParseUint(o, 10, 64); err != nil || o[0] == '0' || o == "0" || n > 9223372036854775807 {
			safeError(w, http.StatusBadRequest, "invalid_query", "Query validation failed", false)
			return "", false
		}
	}

	return csrf, true
}

func parseQuery(r *http.Request) (model.Query, error) {
	q := model.Query{Limit: 200}

	if src := r.PostFormValue("source"); src != "" {
		q.Sources = []string{src}
	}
	q.From = r.PostFormValue("from")
	q.To = r.PostFormValue("to")

	if sev := r.PostFormValue("severity"); sev != "" {
		n, err := strconv.Atoi(sev)
		if err != nil {
			return q, errForm
		}
		q.MinSeverity = n
	} else {
		q.MinSeverity = 1
	}

	terms := r.PostForm["term"]
	for _, t := range terms {
		if t != "" {
			q.Terms = append(q.Terms, t)
		}
	}

	if lim := r.PostFormValue("limit"); lim != "" {
		n, err := strconv.Atoi(lim)
		if err != nil {
			return q, errForm
		}
		q.Limit = n
	}

	q.Cursor = r.PostFormValue("cursor")

	if err := model.ValidateQuery(q); err != nil {
		return q, err
	}
	return q, nil
}

var intentRe = regexp.MustCompile(`^(search|add_term|add_pred|clear|save_view|open_view|delete_view|remove_term:[0-7]|remove_pred:[0-7])$`)

var viewNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// predInput is one attribute row on the form, before it becomes a predicate.
type predInput struct {
	Path  string
	Op    string
	Type  string
	Value string
}

// uiForm is the posted investigation form, including empty rows kept for redisplay.
type uiForm struct {
	Preset     string
	Sources    []string
	From       string
	To         string
	Severity   string
	Terms      []string
	Preds      []predInput
	Cursor     string
	ViewName   string
	Intent     string
	ID         string
	Origin     string
	Generation string
	Sequence   string
}

func parseUI(r *http.Request) (uiForm, error) {
	f := uiForm{
		Preset:     r.PostFormValue("preset"),
		From:       r.PostFormValue("from"),
		To:         r.PostFormValue("to"),
		Severity:   r.PostFormValue("severity"),
		Cursor:     r.PostFormValue("cursor"),
		ViewName:   r.PostFormValue("view"),
		Intent:     r.PostFormValue("intent"),
		ID:         r.PostFormValue("id"),
		Origin:     r.PostFormValue("origin"),
		Generation: r.PostFormValue("generation"),
		Sequence:   r.PostFormValue("sequence"),
	}
	for _, src := range r.PostForm["source"] {
		if src != "" {
			f.Sources = append(f.Sources, src)
		}
	}
	f.Terms = append([]string(nil), r.PostForm["term"]...)
	paths := r.PostForm["path"]
	ops := r.PostForm["op"]
	types := r.PostForm["ptype"]
	vals := r.PostForm["pval"]
	if len(ops) != len(paths) || len(types) != len(paths) || len(vals) != len(paths) {
		return f, errForm
	}
	if len(paths) > model.MaxPredicates || len(f.Terms) > model.MaxTerms {
		return f, errForm
	}
	for i := range paths {
		f.Preds = append(f.Preds, predInput{Path: paths[i], Op: ops[i], Type: types[i], Value: vals[i]})
	}
	return f, nil
}

func (f uiForm) cursorOnly() bool {
	return f.Cursor != "" && f.From == "" && f.To == ""
}

func (f uiForm) searchTerms() []string {
	var out []string
	for _, t := range f.Terms {
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func (f uiForm) searchPreds() ([]model.Predicate, error) {
	var out []model.Predicate
	for _, p := range f.Preds {
		if strings.TrimSpace(p.Path) == "" {
			continue
		}
		pred, err := predFromInput(p)
		if err != nil {
			return nil, err
		}
		out = append(out, pred)
	}
	return out, nil
}

func predFromInput(p predInput) (model.Predicate, error) {
	segs := strings.Split(p.Path, ".")
	pred := model.Predicate{Path: segs, Op: p.Op}
	if p.Op == "missing" {
		return pred, nil
	}
	if p.Op != "eq" {
		return pred, errForm
	}
	pred.Type = p.Type
	switch p.Type {
	case "null":
		pred.Value = []byte("null")
	case "boolean":
		if p.Value != "true" && p.Value != "false" {
			return pred, errForm
		}
		pred.Value = []byte(p.Value)
	case "integer", "real":
		if p.Value == "" {
			return pred, errForm
		}
		pred.Value = []byte(strings.TrimSpace(p.Value))
	case "string":
		raw, err := json.Marshal(p.Value)
		if err != nil {
			return pred, errForm
		}
		pred.Value = raw
	default:
		return pred, errForm
	}
	return pred, nil
}

func inputsFromPreds(preds []model.Predicate) []predInput {
	out := make([]predInput, 0, len(preds))
	for _, p := range preds {
		in := predInput{Path: strings.Join(p.Path, "."), Op: p.Op, Type: p.Type}
		switch p.Type {
		case "string":
			var s string
			if err := json.Unmarshal(p.Value, &s); err == nil {
				in.Value = s
			}
		case "null":
		default:
			in.Value = strings.TrimSpace(string(p.Value))
		}
		out = append(out, in)
	}
	return out
}
