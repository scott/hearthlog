package emitter

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

var (
	ErrRedactDepth   = errors.New("redact: depth exceeded")
	ErrRedactMembers = errors.New("redact: too many members")
	ErrRedactKey     = errors.New("redact: invalid key")
	ErrRedactValue   = errors.New("redact: unsupported value")
)

const (
	maxRedactDepth   = MaxNesting
	maxRedactMembers = MaxAttributes
	maxKeyBytes      = 128
	maxStringBytes   = 8192
	maxResolveSteps  = 32
)

// Keys containing these fragments (case-insensitive) are removed at every
// level before enqueue. Bodies, headers and arbitrary objects are default-deny.
var dropKeys = []string{"password", "secret", "token", "authorization", "cookie", "header", "body", "credential"}

func shouldDropKey(key string) bool {
	lower := strings.ToLower(key)
	for _, k := range dropKeys {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

func scrubString(s string, secrets []string) string {
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "[REDACTED]")
		}
	}
	if len(s) > maxStringBytes {
		s = truncateUTF8(s, maxStringBytes)
	}
	return s
}

// truncateUTF8 shortens s to at most max bytes without splitting a rune.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// resolve evaluates LogValuers synchronously with a step bound. Unlike
// slog.Value.Resolve, a panicking LogValue fails closed instead of producing a
// stack-trace string that could leak internals onto the wire.
func resolve(v slog.Value) (out slog.Value, err error) {
	defer func() {
		if recover() != nil {
			out, err = slog.Value{}, ErrRedactValue
		}
	}()
	for i := 0; i < maxResolveSteps; i++ {
		if v.Kind() != slog.KindLogValuer {
			return v, nil
		}
		v = v.LogValuer().LogValue()
	}
	return slog.Value{}, ErrRedactValue
}

// scalar converts a resolved non-group value into an owned JSON value.
// Arbitrary objects (KindAny other than nil) are rejected: fail closed.
func scalar(v slog.Value, secrets []string) (any, error) {
	switch v.Kind() {
	case slog.KindAny:
		if v.Any() == nil {
			return nil, nil
		}
		return nil, ErrRedactValue
	case slog.KindString:
		s := v.String()
		if !utf8.ValidString(s) {
			s = strings.ToValidUTF8(s, "\uFFFD")
		}
		return scrubString(s, secrets), nil
	case slog.KindBool:
		return v.Bool(), nil
	case slog.KindInt64:
		return v.Int64(), nil
	case slog.KindUint64:
		u := v.Uint64()
		if u > math.MaxInt64 {
			return nil, ErrRedactValue
		}
		return int64(u), nil
	case slog.KindFloat64:
		f := v.Float64()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, ErrRedactValue
		}
		s := strconv.FormatFloat(f, 'g', -1, 64)
		if !strings.ContainsAny(s, ".eE") {
			s += ".0"
		}
		return json.Number(s), nil
	case slog.KindTime:
		return v.Time().UTC().Format("2006-01-02T15:04:05.000000Z"), nil
	case slog.KindDuration:
		return int64(v.Duration()), nil
	default:
		return nil, ErrRedactValue
	}
}
