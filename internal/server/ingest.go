package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"example.invalid/hearthlog/internal/model"
	"example.invalid/hearthlog/internal/storage/sqlite"
)

func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		skeletonError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", false)
		return
	}

	auth := r.Header.Get("Authorization")
	if len(auth) < 8 || auth[:7] != "Bearer " {
		skeletonError(w, http.StatusUnauthorized, "unauthenticated", "Authentication required", false)
		return
	}
	if !s.verifyWrite(auth[7:]) {
		skeletonError(w, http.StatusUnauthorized, "unauthenticated", "Authentication required", false)
		return
	}

	if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Content-Encoding") != "" {
		skeletonError(w, http.StatusUnsupportedMediaType, "unsupported_media", "Unsupported media type", false)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			skeletonError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "Payload too large", false)
			return
		}
		skeletonError(w, http.StatusBadRequest, "invalid_event", "Event validation failed", false)
		return
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	batch, err := model.ValidateBatch(raw, now)
	if err != nil {
		skeletonError(w, http.StatusBadRequest, "invalid_event", "Event validation failed", false)
		return
	}

	receipt, err := s.store.Commit(r.Context(), model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, batch)
	if err != nil {
		switch {
		case errors.Is(err, sqlite.ErrBusy):
			w.Header().Set("Retry-After", "5")
			skeletonError(w, http.StatusServiceUnavailable, "busy", "Service busy", true)
		case errors.Is(err, sqlite.ErrReplayUnsupported):
			skeletonError(w, http.StatusNotImplemented, "replay_not_supported", "Replay not supported", false)
		default:
			skeletonError(w, http.StatusServiceUnavailable, "storage_unavailable", "Storage unavailable", true)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(receipt)
}

func safeError(w http.ResponseWriter, code int, errCode, msg string, retryable bool) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"version": 1,
		"error": map[string]any{
			"code":      errCode,
			"message":   msg,
			"retryable": retryable,
		},
	})
}
