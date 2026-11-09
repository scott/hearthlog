package sqlite

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"example.invalid/hearthlog/internal/model"
)

const cursorTTL = 15 * time.Minute

var (
	ErrCursorExpired = errors.New("cursor expired")
	ErrCursorInvalid = errors.New("cursor invalid")
	ErrCursorScope   = errors.New("cursor scope")
)

type cursorBody struct {
	V       int               `json:"v"`
	Epoch   string            `json:"epoch"`
	WS      string            `json:"ws"`
	Sources []string          `json:"sources"`
	From    string            `json:"from"`
	To      string            `json:"to"`
	Sev     int               `json:"sev"`
	Terms   []string          `json:"terms"`
	Preds   []model.Predicate `json:"preds"`
	Limit   int               `json:"limit"`
	Horizon int64             `json:"horizon"`
	Cutoff  int64             `json:"cutoff"`
	HasLast bool              `json:"has_last"`
	LastT   int64             `json:"last_t"`
	LastID  int64             `json:"last_id"`
	Exp     int64             `json:"exp"`
}

func signCursor(key []byte, body cursorBody) (string, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	sum := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sum), nil
}

func openCursor(key []byte, token string) (cursorBody, error) {
	var body cursorBody
	i := bytes.LastIndexByte([]byte(token), '.')
	if i <= 0 || i == len(token)-1 {
		return body, ErrCursorInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(token[:i])
	if err != nil {
		return body, ErrCursorInvalid
	}
	sum, err := base64.RawURLEncoding.DecodeString(token[i+1:])
	if err != nil || len(sum) != sha256.Size {
		return body, ErrCursorInvalid
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	if !hmac.Equal(sum, mac.Sum(nil)) {
		return body, ErrCursorInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.V != 1 {
		return body, ErrCursorInvalid
	}
	return body, nil
}

func filtersMatch(q model.Query, b cursorBody) bool {
	if q.From != b.From || q.To != b.To || q.MinSeverity != b.Sev || q.Limit != b.Limit {
		return false
	}
	if !stringSliceEq(q.Sources, b.Sources) || !stringSliceEq(q.Terms, b.Terms) {
		return false
	}
	if len(q.Predicates) != len(b.Preds) {
		return false
	}
	for i := range q.Predicates {
		if !predEq(q.Predicates[i], b.Preds[i]) {
			return false
		}
	}
	return true
}

func stringSliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func predEq(a, b model.Predicate) bool {
	if a.Op != b.Op || a.Type != b.Type || len(a.Path) != len(b.Path) {
		return false
	}
	for i := range a.Path {
		if a.Path[i] != b.Path[i] {
			return false
		}
	}
	return bytes.Equal(bytes.TrimSpace(a.Value), bytes.TrimSpace(b.Value))
}

func epochNext(v string) (string, error) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 1 {
		return "", ErrStorage
	}
	return strconv.FormatInt(n+1, 10), nil
}

func decodeMAC(hexKey string) ([]byte, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil || len(key) != 32 {
		return nil, ErrStorage
	}
	return key, nil
}
