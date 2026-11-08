package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

var ErrValidation = errors.New("validation failed")
var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func ValidateBatch(raw []byte, now time.Time) (Batch, error) {
	var b Batch
	if len(raw) > 256*1024 {
		return b, ErrValidation
	}
	if err := ValidateJSON(raw); err != nil {
		return b, err
	}
	if err := checkFieldNames(raw); err != nil {
		return b, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return b, ErrValidation
	}
	if b.Version != SkeletonVersion || !uuidRe.MatchString(b.BatchID) || len(b.Events) != 1 {
		return b, ErrValidation
	}
	var outer struct {
		Version *int                         `json:"version"`
		Events  []map[string]json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil || len(outer.Events) != 1 {
		return b, ErrValidation
	}
	if outer.Version == nil || *outer.Version != SkeletonVersion {
		return b, ErrValidation
	}
	m := outer.Events[0]
	for _, k := range []string{"event_id", "event_time", "severity_number", "severity_text", "message", "attributes"} {
		if _, ok := m[k]; !ok || string(bytes.TrimSpace(m[k])) == "null" {
			return b, ErrValidation
		}
	}
	for _, k := range []string{"trace_id", "span_id"} {
		if v, ok := m[k]; ok && string(bytes.TrimSpace(v)) == "null" {
			return b, ErrValidation
		}
	}
	return b, validateEvent(&b.Events[0], now)
}
