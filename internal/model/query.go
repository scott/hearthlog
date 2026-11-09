package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var sourceIDRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var pathSegRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ErrUnsupportedPath is a query path the v1 contract refuses.
var ErrUnsupportedPath = errors.New("unsupported path")

// Query bounds from the native v1 contract.
const (
	MaxQueryBytes = 4 * 1024
	MaxTerms      = 8
	MaxTermBytes  = 128
	MaxPredicates = 8
	MaxSources    = 32
	MaxWindow     = 7 * 24 * time.Hour
	DefaultWindow = 15 * time.Minute
)

// ValidateQuery enforces native v1 query bounds. Historical windows may end
// in the future relative to admission time; no event-time admission applies.
func ValidateQuery(q Query) error {
	if len(q.Sources) > MaxSources {
		return ErrValidation
	}
	for _, s := range q.Sources {
		if !sourceIDRe.MatchString(s) {
			return ErrValidation
		}
	}
	if q.MinSeverity < 1 || q.MinSeverity > 24 {
		return ErrValidation
	}
	if len(q.Terms) > MaxTerms {
		return ErrValidation
	}
	for _, t := range q.Terms {
		if t == "" || len(t) > MaxTermBytes || !utf8.ValidString(t) || strings.ContainsRune(t, 0) {
			return ErrValidation
		}
	}
	if q.Limit < 1 || q.Limit > 200 {
		return ErrValidation
	}
	if len(q.Predicates) > MaxPredicates {
		return ErrValidation
	}
	for i := range q.Predicates {
		if err := validatePredicate(&q.Predicates[i]); err != nil {
			return err
		}
	}

	from, err := parseQueryTime(q.From)
	if err != nil {
		return ErrValidation
	}
	to, err := parseQueryTime(q.To)
	if err != nil {
		return ErrValidation
	}
	if !from.Before(to) {
		return ErrValidation
	}
	if to.Sub(from) > MaxWindow {
		return ErrValidation
	}
	if len(q.Cursor) > MaxQueryBytes {
		return ErrValidation
	}

	b, err := json.Marshal(q)
	if err != nil || len(b) > MaxQueryBytes {
		return ErrValidation
	}
	return nil
}

func validatePredicate(p *Predicate) error {
	if err := validatePath(p.Path); err != nil {
		return err
	}
	switch p.Op {
	case "missing":
		if p.Type != "" || len(bytes.TrimSpace(p.Value)) != 0 {
			return ErrValidation
		}
		p.Value = nil
		return nil
	case "eq":
		return validateEq(p)
	default:
		return ErrValidation
	}
}

func validatePath(segs []string) error {
	if len(segs) < 1 || len(segs) > 8 {
		return ErrValidation
	}
	total := 0
	for _, s := range segs {
		n := len(s)
		if n < 1 || n > 128 {
			return ErrValidation
		}
		total += n
		if total > 512 {
			return ErrValidation
		}
		if !pathSegRe.MatchString(s) {
			return ErrUnsupportedPath
		}
	}
	return nil
}

func validateEq(p *Predicate) error {
	switch p.Type {
	case "null":
		if len(bytes.TrimSpace(p.Value)) == 0 || bytes.Equal(bytes.TrimSpace(p.Value), []byte("null")) {
			p.Value = nil
			return nil
		}
		return ErrValidation
	case "boolean":
		v := bytes.TrimSpace(p.Value)
		if bytes.Equal(v, []byte("true")) || bytes.Equal(v, []byte("false")) {
			p.Value = append([]byte(nil), v...)
			return nil
		}
		return ErrValidation
	case "integer":
		n, ok := signedInteger(p.Value)
		if !ok {
			return ErrValidation
		}
		p.Value = []byte(strconv.FormatInt(n, 10))
		return nil
	case "real":
		f, ok := finiteReal(p.Value)
		if !ok {
			return ErrValidation
		}
		p.Value = []byte(strconv.FormatFloat(f, 'g', -1, 64))
		return nil
	case "string":
		var s string
		dec := json.NewDecoder(bytes.NewReader(p.Value))
		if err := dec.Decode(&s); err != nil || !utf8.ValidString(s) {
			return ErrValidation
		}
		if dec.More() {
			return ErrValidation
		}
		var extra any
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			return ErrValidation
		}
		raw, err := json.Marshal(s)
		if err != nil {
			return ErrValidation
		}
		p.Value = raw
		return nil
	default:
		return ErrValidation
	}
}

func signedInteger(raw []byte) (int64, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return 0, false
	}
	if dec.More() {
		return 0, false
	}
	num, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	s := string(num)
	if s == "" || strings.ContainsAny(s, ".eE") {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func finiteReal(raw []byte) (float64, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return 0, false
	}
	if dec.More() {
		return 0, false
	}
	num, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(string(num), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

func parseQueryTime(s string) (time.Time, error) {
	if !timeRe.MatchString(s) {
		return time.Time{}, ErrValidation
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, ErrValidation
	}
	return t.UTC(), nil
}

// JSONPath builds a bound SQLite JSON path from validated segments.
func JSONPath(segs []string) (string, error) {
	if err := validatePath(segs); err != nil {
		return "", err
	}
	return "$." + strings.Join(segs, "."), nil
}
