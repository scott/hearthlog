package model

import (
	"encoding/json"
)

func checkFieldNames(raw []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return ErrValidation
	}

	allowedRoot := map[string]struct{}{
		"version": {}, "batch_id": {}, "events": {},
	}
	for k := range root {
		if _, ok := allowedRoot[k]; !ok {
			return ErrValidation
		}
	}
	for _, req := range []string{"version", "batch_id", "events"} {
		if _, ok := root[req]; !ok {
			return ErrValidation
		}
	}

	var evs []map[string]json.RawMessage
	if err := json.Unmarshal(root["events"], &evs); err != nil {
		return ErrValidation
	}
	if len(evs) == 0 {
		return ErrValidation
	}

	allowedEvent := map[string]struct{}{
		"event_id": {}, "event_time": {}, "severity_number": {},
		"severity_text": {}, "message": {}, "attributes": {},
		"trace_id": {}, "span_id": {},
	}
	for _, ev := range evs {
		for k := range ev {
			if _, ok := allowedEvent[k]; !ok {
				return ErrValidation
			}
		}
		for _, req := range []string{"event_id", "event_time", "severity_number", "severity_text", "message", "attributes"} {
			if _, ok := ev[req]; !ok {
				return ErrValidation
			}
		}
	}
	return nil
}
