package server

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"

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
	}
	for k := range r.PostForm {
		if !allowed[k] {
			safeError(w, http.StatusBadRequest, "invalid_query", "Query validation failed", false)
			return "", false
		}
	}

	for k, vals := range r.PostForm {
		if len(vals) > 1 && k != "term" {
			safeError(w, http.StatusBadRequest, "invalid_query", "Query validation failed", false)
			return "", false
		}
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
