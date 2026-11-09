package server

import (
	"context"
	"net/http"
	"time"
)

// maxConcurrent is the HTTP in-flight limit. It is a separate layer from the
// writer queue, which admits at most 64 jobs and 16MiB. Ingest takes an HTTP
// slot and then a writer-queue slot. A full HTTP limit returns 503 before
// writer admission runs. Searches do not enter the writer queue.
const maxConcurrent = 64

const httpTimeout = 5 * time.Second

var sem64 = make(chan struct{}, maxConcurrent)

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "HX-Request")
	w.Header().Set("Referrer-Policy", "strict-origin")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")

	if r.Host != s.host {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.URL.RawQuery != "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	select {
	case sem64 <- struct{}{}:
		defer func() { <-sem64 }()
	default:
		if r.Method == http.MethodPost && r.URL.Path == "/api/experimental/skeleton/batches" {
			w.Header().Set("Retry-After", "5")
			skeletonError(w, http.StatusServiceUnavailable, "busy", "Service busy", true)
			return
		}
		w.Header().Set("Retry-After", "5")
		safeError(w, http.StatusServiceUnavailable, "busy", "Service busy", true)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), httpTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/health":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	case r.Method == http.MethodGet && r.URL.Path == "/login":
		s.login(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/login":
		s.login(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/investigate":
		s.investigateGET(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/investigate/search":
		s.searchPOST(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/investigate/detail":
		s.detailPOST(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/skeleton/batches":
		s.ingest(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/batches":
		s.nativeIngest(w, r)
	case r.Method == http.MethodGet && len(r.URL.Path) > 8 && r.URL.Path[:8] == "/assets/":
		s.assets(w, r)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}
