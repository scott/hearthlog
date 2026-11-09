package server

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"

	"example.invalid/hearthlog/internal/model"
	"example.invalid/hearthlog/internal/storage/sqlite"
)

const demoWorkspace = "ws_demo_a"
const demoSource = "src_demo_api"

func (s *Server) nativeIngest(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if len(auth) < 8 || auth[:7] != "Bearer " || !s.verifyWrite(auth[7:]) {
		safeError(w, http.StatusUnauthorized, "unauthenticated", "Authentication required", false)
		return
	}
	if !jsonMedia(r.Header.Get("Content-Type")) {
		safeError(w, http.StatusUnsupportedMediaType, "unsupported_media", "Unsupported media type", false)
		return
	}
	body, err := readNativeBody(r)
	if err != nil {
		switch {
		case errors.Is(err, errPayloadTooLarge):
			safeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "Payload too large", false)
		case errors.Is(err, errUnsupportedMedia):
			safeError(w, http.StatusUnsupportedMediaType, "unsupported_media", "Unsupported media type", false)
		default:
			safeError(w, http.StatusBadRequest, "invalid_event", "Event validation failed", false)
		}
		return
	}
	batch, err := model.ValidateNative(body)
	if err != nil {
		safeError(w, http.StatusBadRequest, "invalid_event", "Event validation failed", false)
		return
	}
	result, err := s.store.CommitNative(r.Context(), model.Scope{WorkspaceID: demoWorkspace, SourceID: demoSource}, batch, len(body))
	if err != nil {
		switch {
		case errors.Is(err, sqlite.ErrBusy):
			w.Header().Set("Retry-After", "5")
			safeError(w, http.StatusServiceUnavailable, "busy", "Service busy", true)
		case errors.Is(err, sqlite.ErrConflict):
			safeError(w, http.StatusConflict, "batch_conflict", "Batch conflict", false)
		case errors.Is(err, model.ErrValidation):
			safeError(w, http.StatusBadRequest, "invalid_event", "Event validation failed", false)
		default:
			w.Header().Set("Retry-After", "5")
			safeError(w, http.StatusServiceUnavailable, "storage_unavailable", "Storage unavailable", true)
		}
		return
	}
	if _, err := model.ParseNativeReceipt(result.Body); err != nil {
		w.Header().Set("Retry-After", "5")
		safeError(w, http.StatusServiceUnavailable, "storage_unavailable", "Storage unavailable", true)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Body)
}

var (
	errPayloadTooLarge  = errors.New("payload too large")
	errUnsupportedMedia = errors.New("unsupported media")
	errBadBody          = errors.New("bad body")
)

func readNativeBody(r *http.Request) ([]byte, error) {
	enc := r.Header.Get("Content-Encoding")
	switch enc {
	case "", "identity":
		r.Body = http.MaxBytesReader(nil, r.Body, model.MaxNativeBytes)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			var mb *http.MaxBytesError
			if errors.As(err, &mb) {
				return nil, errPayloadTooLarge
			}
			return nil, errBadBody
		}
		if len(body) > model.MaxNativeBytes {
			return nil, errPayloadTooLarge
		}
		return body, nil
	case "gzip":
		r.Body = http.MaxBytesReader(nil, r.Body, model.MaxNativeBytes)
		compressed, err := io.ReadAll(r.Body)
		if err != nil {
			var mb *http.MaxBytesError
			if errors.As(err, &mb) {
				return nil, errPayloadTooLarge
			}
			return nil, errBadBody
		}
		if len(compressed) > model.MaxNativeBytes {
			return nil, errPayloadTooLarge
		}
		zr, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			return nil, errBadBody
		}
		defer zr.Close()
		decoded, err := io.ReadAll(io.LimitReader(zr, model.MaxNativeBytes+1))
		if err != nil {
			return nil, errBadBody
		}
		if len(decoded) > model.MaxNativeBytes {
			return nil, errPayloadTooLarge
		}
		return decoded, nil
	default:
		return nil, errUnsupportedMedia
	}
}
