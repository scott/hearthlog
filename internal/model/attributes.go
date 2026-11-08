package model

import (
	"encoding/json"
	"strings"
)

var reservedRootKeys = map[string]struct{}{
	"workspace_id": {}, "source_id": {}, "received_at": {},
	"ingest_id": {}, "service": {}, "environment": {},
}

// ValidateAttributes enforces native v1 attribute bounds and reserved-key rules.
func ValidateAttributes(raw json.RawMessage) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return ErrInvalidJSON
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return ErrInvalidJSON
	}
	count := 0
	for dec.More() {
		ktok, err := dec.Token()
		if err != nil {
			return ErrInvalidJSON
		}
		key, ok := ktok.(string)
		if !ok {
			return ErrInvalidJSON
		}
		if len(key) > 128 {
			return ErrInvalidJSON
		}
		if _, reserved := reservedRootKeys[key]; reserved {
			return ErrInvalidJSON
		}
		count++
		if count > 128 {
			return ErrInvalidJSON
		}
		if err := walkAttr(dec, 2, &count); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return ErrInvalidJSON
	}
	return nil
}

func walkAttr(dec *json.Decoder, depth int, count *int) error {
	tok, err := dec.Token()
	if err != nil {
		return ErrInvalidJSON
	}
	switch v := tok.(type) {
	case json.Delim:
		if depth > 8 {
			return ErrInvalidJSON
		}
		switch v {
		case '{':
			return walkAttrObj(dec, depth, count)
		case '[':
			return walkAttrArr(dec, depth, count)
		default:
			return ErrInvalidJSON
		}
	case string:
		if len(v) > 8192 {
			return ErrInvalidJSON
		}
	case json.Number:
		if _, err := v.Int64(); err != nil {
			if _, err := v.Float64(); err != nil {
				return ErrInvalidJSON
			}
		}
	}
	return nil
}

func walkAttrObj(dec *json.Decoder, depth int, count *int) error {
	for dec.More() {
		ktok, err := dec.Token()
		if err != nil {
			return ErrInvalidJSON
		}
		key, ok := ktok.(string)
		if !ok {
			return ErrInvalidJSON
		}
		if len(key) > 128 {
			return ErrInvalidJSON
		}
		*count++
		if *count > 128 {
			return ErrInvalidJSON
		}
		if err := walkAttr(dec, depth+1, count); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return ErrInvalidJSON
	}
	return nil
}

func walkAttrArr(dec *json.Decoder, depth int, count *int) error {
	n := 0
	for dec.More() {
		n++
		if n > 128 {
			return ErrInvalidJSON
		}
		if err := walkAttr(dec, depth+1, count); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return ErrInvalidJSON
	}
	return nil
}
