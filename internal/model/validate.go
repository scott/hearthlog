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
	b, err := decodeEnvelope(raw, SkeletonVersion, 1, 1)
	if err != nil {
		return b, err
	}
	if err := validateEvent(&b.Events[0], now); err != nil {
		return b, err
	}
	return b, nil
}

// ValidateNative parses a NATIVE-V1 batch without applying the event-time
// horizon. The writer applies AdmitEventTime only when no receipt exists.
func ValidateNative(raw []byte) (Batch, error) {
	b, err := decodeEnvelope(raw, NativeVersion, 1, MaxNativeEvents)
	if err != nil {
		return b, err
	}
	for i := range b.Events {
		if err := validateEventShape(&b.Events[i]); err != nil {
			return b, err
		}
	}
	return b, nil
}

func decodeEnvelope(raw []byte, wantVersion, minEvents, maxEvents int) (Batch, error) {
	var b Batch
	if len(raw) > MaxNativeBytes {
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
	if b.Version != wantVersion || !uuidRe.MatchString(b.BatchID) || len(b.Events) < minEvents || len(b.Events) > maxEvents {
		return b, ErrValidation
	}
	var outer struct {
		Version *int                         `json:"version"`
		Events  []map[string]json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil || len(outer.Events) != len(b.Events) {
		return b, ErrValidation
	}
	if outer.Version == nil || *outer.Version != wantVersion {
		return b, ErrValidation
	}
	seen := make(map[string]struct{}, len(b.Events))
	for i, m := range outer.Events {
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
		if _, dup := seen[b.Events[i].EventID]; dup {
			return b, ErrValidation
		}
		seen[b.Events[i].EventID] = struct{}{}
	}
	return b, nil
}
