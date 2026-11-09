package model

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

func TestCanonicalAttributeExample(t *testing.T) {
	got, err := CanonicalAttributes(json.RawMessage(`{"b":true,"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `["object",[["a",["int","1"]],["b",["bool",true]]]]`
	if string(got) != want {
		t.Fatalf("canonical attributes:\n got %s\nwant %s", got, want)
	}

	reordered, err := CanonicalAttributes(json.RawMessage(`{"a":1,"b":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, reordered) {
		t.Fatalf("key order changed digest bytes: %s vs %s", got, reordered)
	}

	real, err := CanonicalAttributes(json.RawMessage(`{"n":1.0}`))
	if err != nil {
		t.Fatal(err)
	}
	realExp, err := CanonicalAttributes(json.RawMessage(`{"n":1e0}`))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(real, realExp) {
		t.Fatalf("1.0 vs 1e0: %s vs %s", real, realExp)
	}
	if !bytes.Contains(real, []byte(`["real","3ff0000000000000"]`)) {
		t.Fatalf("float bits: %s", real)
	}

	negZero, err := CanonicalAttributes(json.RawMessage(`{"n":-0}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(negZero) != `["object",[["n",["int","0"]]]]` {
		t.Fatalf("-0 int: %s", negZero)
	}
	negReal, err := CanonicalAttributes(json.RawMessage(`{"n":-0.0}`))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(negReal, []byte(`["real","8000000000000000"]`)) {
		t.Fatalf("-0.0 bits: %s", negReal)
	}

	quoted, err := CanonicalAttributes(json.RawMessage("{\"m\":\"say \\\"hi\\\"\\n<script>\"}"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(quoted, []byte(`say \"hi\"\u000a<script>`)) {
		t.Fatalf("escaping: %s", quoted)
	}
}

func TestCanonicalHashIdentity(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
	base := nativeBatch(t, when, "2026-01-02T03:04:05.123456Z")
	sum1, canon, err := CanonicalDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	// Offset spelling of the same microsecond must not change the digest.
	shifted := nativeBatch(t, when, "2026-01-02T04:04:05.123456+01:00")
	sum2, _, err := CanonicalDigest(shifted)
	if err != nil {
		t.Fatal(err)
	}
	if sum1 != sum2 {
		t.Fatal("timezone spelling changed the digest")
	}

	// Whitespace and attribute key order are not part of the identity.
	raw := []byte(`{"version":1,"batch_id":"00000000-0000-4000-8000-000000000001","events":[{"event_id":"00000000-0000-4000-8000-000000000002","event_time":"2026-01-02T03:04:05.123456Z","severity_number":9,"severity_text":"INFO","message":"synthetic task completed","attributes":{"b":true,"a":1}}]}`)
	spaced := []byte("{\n \"version\": 1, \"events\": [{\n\"attributes\": {\"a\": 1, \"b\": true}, \"message\": \"synthetic task completed\", \"severity_text\": \"INFO\", \"severity_number\": 9, \"event_time\": \"2026-01-02T03:04:05.123456Z\", \"event_id\": \"00000000-0000-4000-8000-000000000002\"}], \"batch_id\": \"00000000-0000-4000-8000-000000000001\"}")
	b1, err := ValidateNative(raw)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := ValidateNative(spaced)
	if err != nil {
		t.Fatal(err)
	}
	h1, _, err := CanonicalDigest(b1)
	if err != nil {
		t.Fatal(err)
	}
	h2, _, err := CanonicalDigest(b2)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 || h1 != sum1 {
		t.Fatalf("identity drift %x %x %x", h1, h2, sum1)
	}

	swapped := base
	swapped.Events = append([]Event{}, base.Events...)
	extra := base.Events[0]
	extra.EventID = "00000000-0000-4000-8000-000000000003"
	swapped.Events = []Event{extra, base.Events[0]}
	h3, _, err := CanonicalDigest(swapped)
	if err != nil {
		t.Fatal(err)
	}
	rev := swapped
	rev.Events = []Event{swapped.Events[1], swapped.Events[0]}
	h4, _, err := CanonicalDigest(rev)
	if err != nil {
		t.Fatal(err)
	}
	if h3 == h4 {
		t.Fatal("event order did not change the digest")
	}

	// Golden for the single-event vector: version 1, batch
	// 00000000-0000-4000-8000-000000000001, one INFO event at
	// 2026-01-02T03:04:05.123456Z, attributes {"a":1,"b":true}.
	const golden = "8223eca21e15a356cd5e19e1345a8dfec914d8599a59cc79558a8b4f2f5f80cf"
	if got := hex.EncodeToString(sum1[:]); got != golden {
		t.Fatalf("canonical sha256 %s\nbody %s", got, canon)
	}
}

func nativeBatch(t *testing.T, when time.Time, stamp string) Batch {
	t.Helper()
	ev := Event{
		EventID:        "00000000-0000-4000-8000-000000000002",
		EventTime:      stamp,
		SeverityNumber: 9,
		SeverityText:   "INFO",
		Message:        "synthetic task completed",
		Attributes:     json.RawMessage(`{"b":true,"a":1}`),
	}
	raw, err := json.Marshal(Batch{
		Version: NativeVersion,
		BatchID: "00000000-0000-4000-8000-000000000001",
		Events:  []Event{ev},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Keep the caller's stamp if Marshal rewrote the time.
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	events := m["events"].([]any)
	events[0].(map[string]any)["event_time"] = stamp
	raw, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ValidateNative(raw)
	if err != nil {
		t.Fatalf("validate %s: %v", stamp, err)
	}
	if b.Events[0].EventTimeMicros != when.UnixMicro() {
		t.Fatalf("micros %d want %d", b.Events[0].EventTimeMicros, when.UnixMicro())
	}
	return b
}
