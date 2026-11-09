package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestValidateNativeBounds(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	one := nativeRaw(t, 1, now)
	b, err := ValidateNative(one)
	if err != nil {
		t.Fatal(err)
	}
	if err := AdmitEventTime(b.Events[0].EventTimeMicros, now); err != nil {
		t.Fatal(err)
	}
	// Structural validation does not apply the horizon. An old stamp still parses.
	old := strings.Replace(string(one), now.Format(time.RFC3339), "2020-01-02T03:04:05Z", 1)
	if _, err := ValidateNative([]byte(old)); err != nil {
		t.Fatalf("shape should parse outside the horizon: %v", err)
	}
	skel := strings.Replace(string(one), `"version":1`, `"version":0`, 1)
	if _, err := ValidateBatch([]byte(skel), now); err != nil {
		t.Fatalf("skeleton batch: %v", err)
	}
	oldSkel := strings.Replace(skel, now.Format(time.RFC3339), "2020-01-02T03:04:05Z", 1)
	if _, err := ValidateBatch([]byte(oldSkel), now); err == nil {
		t.Fatal("skeleton validation must still apply the horizon")
	}

	if _, err := ValidateNative(nativeRaw(t, MaxNativeEvents+1, now)); err == nil {
		t.Fatal("201 events accepted")
	}
	dup := strings.Replace(string(nativeRaw(t, 2, now)), "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000001", 1)
	if _, err := ValidateNative([]byte(dup)); err == nil {
		t.Fatal("duplicate event id accepted")
	}
	if _, err := ValidateNative([]byte(`{"version":1,"batch_id":"00000000-0000-4000-8000-0000000000aa","events":[]}`)); err == nil {
		t.Fatal("empty events accepted")
	}
	ver := strings.Replace(string(one), `"version":1`, `"version":0`, 1)
	if _, err := ValidateNative([]byte(ver)); err == nil {
		t.Fatal("version 0 accepted on native")
	}
}

func nativeRaw(t *testing.T, n int, when time.Time) []byte {
	t.Helper()
	if n == 0 {
		return []byte(`{"version":1,"batch_id":"00000000-0000-4000-8000-0000000000aa","events":[]}`)
	}
	events := make([]Event, n)
	for i := range events {
		events[i] = Event{
			EventID:        eventID(i + 1),
			EventTime:      when.Format(time.RFC3339),
			SeverityNumber: 9,
			SeverityText:   "INFO",
			Message:        "synthetic",
			Attributes:     []byte(`{"a":1}`),
		}
	}
	raw, err := json.Marshal(Batch{Version: NativeVersion, BatchID: "00000000-0000-4000-8000-0000000000aa", Events: events})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func eventID(i int) string {
	const hexdigits = "0123456789abcdef"
	var tail [12]byte
	for k := 11; k >= 0; k-- {
		tail[k] = hexdigits[i%16]
		i /= 16
	}
	return "00000000-0000-4000-8000-" + string(tail[:])
}
