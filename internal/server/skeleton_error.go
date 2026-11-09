package server

import (
	"encoding/json"
	"net/http"

	"example.invalid/hearthlog/internal/model"
)

func skeletonError(w http.ResponseWriter, code int, errCode, msg string, retryable bool) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"version":  model.SkeletonVersion,
		"protocol": model.SkeletonProtocol,
		"error": map[string]any{
			"code":      errCode,
			"message":   msg,
			"retryable": retryable,
		},
	})
}
