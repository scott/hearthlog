package model

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"time"
)

// NativeReceipt is the NATIVE-V1 success body. Ingest IDs are decimal strings.
// replay_until is received_at plus 48h, formatted in UTC.
type NativeReceipt struct {
	Version       int
	BatchID       string
	Accepted      int
	FirstIngestID string
	LastIngestID  string
	ReceivedAt    string
	ReplayUntil   string
}

var ingestIDRe = regexp.MustCompile(`^[1-9][0-9]*$`)

// FormatReceiptTime renders a UTC timestamp with fractional seconds only when
// the value is not a whole second.
func FormatReceiptTime(t time.Time) string {
	t = t.UTC()
	if t.Nanosecond() == 0 {
		return t.Format("2006-01-02T15:04:05Z")
	}
	return t.Format(time.RFC3339Nano)
}

// EncodeNativeReceipt writes a receipt whose ingest IDs and replay_until are
// JSON strings. Wrong or missing fields return an error and no bytes.
func EncodeNativeReceipt(r NativeReceipt) ([]byte, error) {
	if r.Version != NativeVersion || !uuidRe.MatchString(r.BatchID) || r.Accepted < 1 {
		return nil, ErrValidation
	}
	if !ingestIDRe.MatchString(r.FirstIngestID) || !ingestIDRe.MatchString(r.LastIngestID) {
		return nil, ErrValidation
	}
	fi, err1 := strconv.ParseInt(r.FirstIngestID, 10, 64)
	li, err2 := strconv.ParseInt(r.LastIngestID, 10, 64)
	if err1 != nil || err2 != nil || fi > li {
		return nil, ErrValidation
	}
	if err := parseReceiptStamp(r.ReceivedAt); err != nil {
		return nil, err
	}
	if err := parseReceiptStamp(r.ReplayUntil); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString(`{"version":1,"batch_id":`)
	writeJSONString(&buf, r.BatchID)
	buf.WriteString(`,"accepted":`)
	buf.WriteString(strconv.Itoa(r.Accepted))
	buf.WriteString(`,"first_ingest_id":`)
	writeJSONString(&buf, r.FirstIngestID)
	buf.WriteString(`,"last_ingest_id":`)
	writeJSONString(&buf, r.LastIngestID)
	buf.WriteString(`,"received_at":`)
	writeJSONString(&buf, r.ReceivedAt)
	buf.WriteString(`,"replay_until":`)
	writeJSONString(&buf, r.ReplayUntil)
	buf.WriteByte('}')
	if _, err := ParseNativeReceipt(buf.Bytes()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ParseNativeReceipt accepts only the native receipt object. first_ingest_id,
// last_ingest_id and replay_until must be JSON strings; numbers, nulls and
// missing fields are rejected. Duplicate keys are rejected.
func ParseNativeReceipt(raw []byte) (NativeReceipt, error) {
	var zero NativeReceipt
	if len(raw) == 0 || len(raw) > 4096 {
		return zero, ErrValidation
	}
	if err := ValidateJSON(raw); err != nil {
		return zero, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return zero, ErrValidation
	}
	required := []string{
		"version", "batch_id", "accepted",
		"first_ingest_id", "last_ingest_id", "received_at", "replay_until",
	}
	if len(fields) != len(required) {
		return zero, ErrValidation
	}
	for _, k := range required {
		if len(fields[k]) == 0 {
			return zero, ErrValidation
		}
	}
	ver, err := jsonUint(fields["version"])
	if err != nil || ver != NativeVersion {
		return zero, ErrValidation
	}
	accepted, err := jsonUint(fields["accepted"])
	if err != nil || accepted < 1 {
		return zero, ErrValidation
	}
	batchID, err := jsonString(fields["batch_id"])
	if err != nil || !uuidRe.MatchString(batchID) {
		return zero, ErrValidation
	}
	first, err := jsonString(fields["first_ingest_id"])
	if err != nil || !ingestIDRe.MatchString(first) {
		return zero, ErrValidation
	}
	last, err := jsonString(fields["last_ingest_id"])
	if err != nil || !ingestIDRe.MatchString(last) {
		return zero, ErrValidation
	}
	fi, err1 := strconv.ParseInt(first, 10, 64)
	li, err2 := strconv.ParseInt(last, 10, 64)
	if err1 != nil || err2 != nil || fi > li {
		return zero, ErrValidation
	}
	received, err := jsonString(fields["received_at"])
	if err != nil || parseReceiptStamp(received) != nil {
		return zero, ErrValidation
	}
	until, err := jsonString(fields["replay_until"])
	if err != nil || parseReceiptStamp(until) != nil {
		return zero, ErrValidation
	}
	return NativeReceipt{
		Version:       ver,
		BatchID:       batchID,
		Accepted:      accepted,
		FirstIngestID: first,
		LastIngestID:  last,
		ReceivedAt:    received,
		ReplayUntil:   until,
	}, nil
}

func jsonString(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return "", ErrValidation
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", ErrValidation
	}
	return s, nil
}

func jsonUint(raw json.RawMessage) (int, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] == '"' {
		return 0, ErrValidation
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || n < 0 || n > int64(^uint(0)>>1) {
		return 0, ErrValidation
	}
	return int(n), nil
}

func parseReceiptStamp(s string) error {
	if !timeRe.MatchString(s) {
		return ErrValidation
	}
	if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
		return ErrValidation
	}
	return nil
}
