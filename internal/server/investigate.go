package server

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"example.invalid/hearthlog/internal/model"
	"example.invalid/hearthlog/internal/storage/sqlite"
)

func (s *Server) investigateGET(w http.ResponseWriter, r *http.Request) {
	csrf, ok := s.readSession(r)
	if !ok {
		safeError(w, http.StatusUnauthorized, "unauthenticated", "Authentication required", false)
		return
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	v := view{
		CSRF:       csrf,
		Query:      model.Query{Sources: []string{"src_demo_api"}, From: now.Add(-15 * time.Minute).Format(time.RFC3339Nano), To: now.Add(time.Second).Format(time.RFC3339Nano), MinSeverity: 1, Limit: 200},
		Status:     "Ready to investigate",
		Generation: "1",
		Sequence:   "1",
	}
	s.render(w, r, "page", v, http.StatusOK)
}

func (s *Server) searchPOST(w http.ResponseWriter, r *http.Request) {
	csrf, ok := s.form(w, r)
	if !ok {
		return
	}
	q, err := parseQuery(r)
	if err != nil {
		safeError(w, http.StatusBadRequest, "invalid_query", "Query validation failed", false)
		return
	}
	rows, err := s.store.Search(r.Context(), model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, q)
	if err != nil {
		switch {
		case errors.Is(err, sqlite.ErrTooBroad):
			safeError(w, http.StatusUnprocessableEntity, "query_too_broad", "Query too broad", false)
		default:
			safeError(w, http.StatusServiceUnavailable, "storage_unavailable", "Storage unavailable", true)
		}
		return
	}
	v := view{
		CSRF:       csrf,
		Query:      q,
		Rows:       rows,
		Status:     "Results",
		Generation: r.PostFormValue("generation"),
		Sequence:   r.PostFormValue("sequence"),
		Selection:  "",
		OriginID:   r.PostFormValue("origin"),
	}
	if v.Generation == "" {
		v.Generation = "1"
	}
	if v.Sequence == "" {
		v.Sequence = "1"
	}
	s.render(w, r, "results", v, http.StatusOK)
}

func (s *Server) detailPOST(w http.ResponseWriter, r *http.Request) {
	csrf, ok := s.form(w, r)
	if !ok {
		return
	}
	q, err := parseQuery(r)
	if err != nil {
		safeError(w, http.StatusBadRequest, "invalid_query", "Query validation failed", false)
		return
	}
	idStr := r.PostFormValue("id")
	if idStr == "" {
		safeError(w, http.StatusBadRequest, "invalid_query", "Query validation failed", false)
		return
	}
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		safeError(w, http.StatusBadRequest, "invalid_query", "Query validation failed", false)
		return
	}
	rec, err := s.store.Detail(r.Context(), model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, strconv.FormatUint(id, 10))
	if err != nil {
		safeError(w, http.StatusNotFound, "not_found", "Not found", false)
		return
	}
	v := view{
		CSRF:       csrf,
		Query:      q,
		Detail:     &rec,
		Status:     "Detail",
		Generation: r.PostFormValue("generation"),
		Sequence:   r.PostFormValue("sequence"),
		Selection:  strconv.FormatUint(id, 10),
		OriginID:   strconv.FormatUint(id, 10),
	}
	if v.Generation == "" {
		v.Generation = "1"
	}
	if v.Sequence == "" {
		v.Sequence = "1"
	}
	s.render(w, r, "detail", v, http.StatusOK)
}
