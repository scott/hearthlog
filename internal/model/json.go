package model

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"unicode/utf8"
)

var ErrInvalidJSON = errors.New("invalid json")

func ValidateJSON(data []byte) error {
	if err := validateUnicode(data); err != nil {
		return err
	}
	if len(data) == 0 {
		return ErrInvalidJSON
	}
	r := strings.NewReader(string(data))
	dec := json.NewDecoder(r)
	dec.UseNumber()
	if err := walk(dec, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return ErrInvalidJSON
	}
	return nil
}

func walk(d *json.Decoder, depth int) error {
	if depth > 16 {
		return ErrInvalidJSON
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	switch v := t.(type) {
	case json.Delim:
		switch v {
		case '{':
			return walkObj(d, depth)
		case '[':
			return walkArr(d, depth)
		default:
			return ErrInvalidJSON
		}
	case string:
		if !validStr(v) {
			return ErrInvalidJSON
		}
	case json.Number:
		if err := checkNum(v); err != nil {
			return err
		}
	}
	return nil
}

func walkObj(d *json.Decoder, depth int) error {
	seen := make(map[string]struct{})
	for d.More() {
		k, err := d.Token()
		if err != nil {
			return ErrInvalidJSON
		}
		if s, ok := k.(string); ok {
			if !validStr(s) {
				return ErrInvalidJSON
			}
			if _, dup := seen[s]; dup {
				return ErrInvalidJSON
			}
			seen[s] = struct{}{}
		} else {
			return ErrInvalidJSON
		}
		if err := walk(d, depth+1); err != nil {
			return err
		}
	}
	return closeDelim(d, '}')
}

func walkArr(d *json.Decoder, depth int) error {
	for d.More() {
		if err := walk(d, depth+1); err != nil {
			return err
		}
	}
	return closeDelim(d, ']')
}

func closeDelim(d *json.Decoder, want json.Delim) error {
	t, err := d.Token()
	if err != nil || t != want {
		return ErrInvalidJSON
	}
	return nil
}

func validStr(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r >= 0xD800 && r <= 0xDFFF {
			return false
		}
	}
	return true
}

func checkNum(n json.Number) error {
	s := n.String()
	if strings.ContainsAny(s, ".eE") {
		f, err := n.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return ErrInvalidJSON
		}
		if f == 0 && hasNonZeroMantissa(s) {
			return ErrInvalidJSON
		}
	} else if _, err := n.Int64(); err != nil {
		return ErrInvalidJSON
	}
	return nil
}

func hasNonZeroMantissa(s string) bool {
	for _, c := range s {
		if c == 'e' || c == 'E' {
			break
		}
		if c >= '1' && c <= '9' {
			return true
		}
	}
	return false
}
