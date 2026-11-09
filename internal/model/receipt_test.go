package model

import (
	"strings"
	"testing"
	"time"
)

func sampleReceipt() NativeReceipt {
	recv := time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)
	return NativeReceipt{
		Version:       NativeVersion,
		BatchID:       "00000000-0000-4000-8000-000000000001",
		Accepted:      1,
		FirstIngestID: "1",
		LastIngestID:  "1",
		ReceivedAt:    FormatReceiptTime(recv),
		ReplayUntil:   FormatReceiptTime(recv.Add(48 * time.Hour)),
	}
}

func TestEncodeNativeReceiptTypes(t *testing.T) {
	body, err := EncodeNativeReceipt(sampleReceipt())
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseNativeReceipt(body)
	if err != nil {
		t.Fatalf("roundtrip: %v body=%s", err, body)
	}
	if got != sampleReceipt() {
		t.Fatalf("roundtrip mismatch %+v", got)
	}
	if strings.Contains(string(body), `"first_ingest_id":1`) || strings.Contains(string(body), `"replay_until":1`) {
		t.Fatalf("ingest id or replay_until encoded as a number: %s", body)
	}

	cases := []struct {
		name string
		mod  func(NativeReceipt) NativeReceipt
	}{
		{"missing_first", func(r NativeReceipt) NativeReceipt { r.FirstIngestID = ""; return r }},
		{"first_not_decimal", func(r NativeReceipt) NativeReceipt { r.FirstIngestID = "01"; return r }},
		{"last_text", func(r NativeReceipt) NativeReceipt { r.LastIngestID = "abc"; return r }},
		{"first_after_last", func(r NativeReceipt) NativeReceipt { r.FirstIngestID = "3"; r.LastIngestID = "2"; return r }},
		{"replay_not_time", func(r NativeReceipt) NativeReceipt { r.ReplayUntil = "tomorrow"; return r }},
		{"replay_empty", func(r NativeReceipt) NativeReceipt { r.ReplayUntil = ""; return r }},
		{"received_empty", func(r NativeReceipt) NativeReceipt { r.ReceivedAt = ""; return r }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := EncodeNativeReceipt(tc.mod(sampleReceipt())); err == nil {
				t.Fatal("expected encode to reject")
			}
		})
	}
}

func TestParseNativeReceiptRejectsWrongTypes(t *testing.T) {
	ok, err := EncodeNativeReceipt(sampleReceipt())
	if err != nil {
		t.Fatal(err)
	}
	base := string(ok)
	cases := []struct {
		name string
		body string
	}{
		{"first_number", strings.Replace(base, `"first_ingest_id":"1"`, `"first_ingest_id":1`, 1)},
		{"last_number", strings.Replace(base, `"last_ingest_id":"1"`, `"last_ingest_id":1`, 1)},
		{"replay_number", strings.Replace(base, `"replay_until":"2026-01-04T03:04:06Z"`, `"replay_until":1735923846`, 1)},
		{"first_null", strings.Replace(base, `"first_ingest_id":"1"`, `"first_ingest_id":null`, 1)},
		{"replay_bool", strings.Replace(base, `"replay_until":"2026-01-04T03:04:06Z"`, `"replay_until":true`, 1)},
		{"missing_replay", strings.Replace(base, `,"replay_until":"2026-01-04T03:04:06Z"`, ``, 1)},
		{"missing_first", strings.Replace(base, `"first_ingest_id":"1",`, ``, 1)},
		{"missing_last", strings.Replace(base, `"last_ingest_id":"1",`, ``, 1)},
		{"extra_field", strings.Replace(base, `{`, `{"protocol":"native",`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseNativeReceipt([]byte(tc.body)); err == nil {
				t.Fatalf("accepted %s", tc.body)
			}
		})
	}
}
