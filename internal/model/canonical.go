package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// CanonicalDigest is the SHA-256 of the whitespace-free canonical encoding
// defined in docs/contracts/NATIVE-V1.md. The hash is not an authentication
// token. Event time is the UTC microsecond count as a JSON string, so offset
// spelling does not change the digest. Attribute values are tagged; object
// keys are sorted by UTF-8 bytes.
func CanonicalDigest(b Batch) ([32]byte, []byte, error) {
	var buf bytes.Buffer
	buf.Grow(256)
	buf.WriteString(`["native",1,`)
	writeJSONString(&buf, b.BatchID)
	buf.WriteString(`,[`)
	for i := range b.Events {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := writeCanonicalEvent(&buf, &b.Events[i]); err != nil {
			return [32]byte{}, nil, err
		}
	}
	buf.WriteString(`]]`)
	sum := sha256.Sum256(buf.Bytes())
	return sum, buf.Bytes(), nil
}

func writeCanonicalEvent(buf *bytes.Buffer, e *Event) error {
	buf.WriteByte('[')
	writeJSONString(buf, e.EventID)
	buf.WriteByte(',')
	writeJSONString(buf, strconv.FormatInt(e.EventTimeMicros, 10))
	buf.WriteByte(',')
	buf.WriteString(strconv.Itoa(e.SeverityNumber))
	buf.WriteByte(',')
	writeJSONString(buf, e.SeverityText)
	buf.WriteByte(',')
	writeJSONString(buf, e.Message)
	buf.WriteByte(',')
	if err := writeCanonicalAttribute(buf, e.Attributes); err != nil {
		return err
	}
	buf.WriteByte(',')
	writeOptionalID(buf, e.TraceID)
	buf.WriteByte(',')
	writeOptionalID(buf, e.SpanID)
	buf.WriteByte(']')
	return nil
}

func writeOptionalID(buf *bytes.Buffer, v *string) {
	if v == nil {
		buf.WriteString("null")
		return
	}
	writeJSONString(buf, *v)
}

// CanonicalAttributes encodes one attributes object in the tagged form.
func CanonicalAttributes(raw json.RawMessage) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeCanonicalAttribute(&buf, raw); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonicalAttribute(buf *bytes.Buffer, raw json.RawMessage) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := writeCanonicalValue(buf, dec); err != nil {
		return err
	}
	if dec.More() {
		return ErrValidation
	}
	return nil
}

func writeCanonicalValue(buf *bytes.Buffer, dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return ErrValidation
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			return writeCanonicalObject(buf, dec)
		case '[':
			return writeCanonicalArray(buf, dec)
		default:
			return ErrValidation
		}
	case nil:
		buf.WriteString(`["null"]`)
	case bool:
		if v {
			buf.WriteString(`["bool",true]`)
		} else {
			buf.WriteString(`["bool",false]`)
		}
	case string:
		buf.WriteString(`["string",`)
		writeJSONString(buf, v)
		buf.WriteByte(']')
	case json.Number:
		return writeCanonicalNumber(buf, v)
	default:
		return ErrValidation
	}
	return nil
}

func writeCanonicalObject(buf *bytes.Buffer, dec *json.Decoder) error {
	type pair struct {
		key string
		val []byte
	}
	var pairs []pair
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return ErrValidation
		}
		key, ok := kt.(string)
		if !ok {
			return ErrValidation
		}
		var vb bytes.Buffer
		if err := writeCanonicalValue(&vb, dec); err != nil {
			return err
		}
		pairs = append(pairs, pair{key: key, val: vb.Bytes()})
	}
	if _, err := dec.Token(); err != nil {
		return ErrValidation
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].key < pairs[j].key })
	buf.WriteString(`["object",[`)
	for i, p := range pairs {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteByte('[')
		writeJSONString(buf, p.key)
		buf.WriteByte(',')
		buf.Write(p.val)
		buf.WriteByte(']')
	}
	buf.WriteString(`]]`)
	return nil
}

func writeCanonicalArray(buf *bytes.Buffer, dec *json.Decoder) error {
	var vals [][]byte
	for dec.More() {
		var vb bytes.Buffer
		if err := writeCanonicalValue(&vb, dec); err != nil {
			return err
		}
		vals = append(vals, vb.Bytes())
	}
	if _, err := dec.Token(); err != nil {
		return ErrValidation
	}
	buf.WriteString(`["array",[`)
	for i, v := range vals {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(v)
	}
	buf.WriteString(`]]`)
	return nil
}

func writeCanonicalNumber(buf *bytes.Buffer, n json.Number) error {
	s := n.String()
	if strings.ContainsAny(s, ".eE") {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return ErrValidation
		}
		if f == 0 && hasNonZeroMantissa(s) {
			return ErrValidation
		}
		fmt.Fprintf(buf, `["real","%016x"]`, math.Float64bits(f))
		return nil
	}
	if s == "-0" {
		buf.WriteString(`["int","0"]`)
		return nil
	}
	i, err := n.Int64()
	if err != nil {
		return ErrValidation
	}
	buf.WriteString(`["int","`)
	buf.WriteString(strconv.FormatInt(i, 10))
	buf.WriteString(`"]`)
	return nil
}

// writeJSONString escapes only quote, backslash and U+0000–U+001F.
// Other Unicode is raw UTF-8. There is no HTML escaping.
func writeJSONString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		default:
			if r < 0x20 {
				fmt.Fprintf(buf, `\u%04x`, r)
			} else {
				buf.WriteRune(r)
			}
		}
	}
	buf.WriteByte('"')
}
