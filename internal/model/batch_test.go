package model

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestValidateBatch(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	base := Event{
		EventID:        "00000000-0000-4000-8000-000000000001",
		EventTime:      now.Format(time.RFC3339Nano),
		SeverityNumber: 9,
		SeverityText:   "INFO",
		Message:        "",
		Attributes:     json.RawMessage(`{"a":1}`),
	}
	valid, _ := json.Marshal(Batch{Version: 0, BatchID: "00000000-0000-4000-8000-000000000002", Events: []Event{base}})
	if _, err := ValidateBatch(valid, now); err != nil {
		t.Fatalf("valid: %v", err)
	}

	type tc struct {
		name string
		mod  func(map[string]any) map[string]any
	}
	cases := []tc{
		{"missingmsg", func(m map[string]any) map[string]any {
			e := m["events"].([]any)[0].(map[string]any)
			delete(e, "message")
			return m
		}},
		{"nullmsg", func(m map[string]any) map[string]any {
			e := m["events"].([]any)[0].(map[string]any)
			e["message"] = nil
			return m
		}},
		{"unknownroot", func(m map[string]any) map[string]any {
			m["extra"] = 1
			return m
		}},
		{"unknownver", func(m map[string]any) map[string]any {
			m["Version"] = 1
			return m
		}},
		{"unknownmsg", func(m map[string]any) map[string]any {
			e := m["events"].([]any)[0].(map[string]any)
			e["Message"] = "x"
			return m
		}},
		{"reservedattr", func(m map[string]any) map[string]any {
			e := m["events"].([]any)[0].(map[string]any)
			e["attributes"] = map[string]any{"workspace_id": 1}
			return m
		}},
		{"7frac", func(m map[string]any) map[string]any {
			e := m["events"].([]any)[0].(map[string]any)
			e["event_time"] = now.Format("2006-01-02T15:04:05.0000000Z")
			return m
		}},
		{"sevmm", func(m map[string]any) map[string]any {
			e := m["events"].([]any)[0].(map[string]any)
			e["severity_text"] = "WARN"
			return m
		}},
		{"nulltrace", func(m map[string]any) map[string]any {
			e := m["events"].([]any)[0].(map[string]any)
			e["trace_id"] = nil
			return m
		}},
		{"multi", func(m map[string]any) map[string]any {
			evs := m["events"].([]any)
			evs = append(evs, evs[0])
			m["events"] = evs
			return m
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal(valid, &m); err != nil {
				t.Fatal(err)
			}
			m = c.mod(m)
			out, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid(out) {
				t.Fatal("invalid json")
			}
			if bytes.Equal(out, valid) {
				t.Fatal("no change")
			}
			if _, err := ValidateBatch(out, now); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
