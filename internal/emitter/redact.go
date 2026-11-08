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
	maxRedactDepth   = 8
	maxRedactMembers = 128
	maxKeyBytes      = 128
	maxStringBytes   = 8192
)

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
		s = s[:maxStringBytes]
	}
	return s
}

func sanitizeValue(v slog.Value, secrets []string, depth int) (any, error) {
	if depth > maxRedactDepth {
		return nil, ErrRedactDepth
	}

	v = v.Resolve()

	if v.Kind() == slog.KindAny {
		if v.Any() == nil {
			return nil, nil
		}
		return nil, ErrRedactValue
	}

	switch v.Kind() {
	case slog.KindString:
		return scrubString(v.String(), secrets), nil
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
	case slog.KindGroup:
		return sanitizeGroup(v, secrets, depth)
	default:
		return nil, ErrRedactValue
	}
}

func sanitizeGroup(v slog.Value, secrets []string, depth int) (map[string]any, error) {
	result := make(map[string]any)
	attrs := v.Group()
	for _, a := range attrs {
		if len(result) >= maxRedactMembers {
			return nil, ErrRedactMembers
		}
		key := a.Key
		if !utf8.ValidString(key) || len(key) > maxKeyBytes {
			return nil, ErrRedactKey
		}
		if shouldDropKey(key) {
			continue
		}
		for _, sec := range secrets {
			if sec != "" && strings.Contains(key, sec) {
				return nil, ErrRedactKey
			}
		}
		sanitized, err := sanitizeValue(a.Value, secrets, depth+1)
		if err != nil {
			return nil, err
		}
		result[key] = sanitized
	}
	return result, nil
}

func SanitizeAttrs(attrs []slog.Attr, secrets []string) (map[string]any, error) {
	result := make(map[string]any, len(attrs))
	for _, a := range attrs {
		if len(result) >= maxRedactMembers {
			return nil, ErrRedactMembers
		}
		key := a.Key
		if !utf8.ValidString(key) || len(key) > maxKeyBytes {
			return nil, ErrRedactKey
		}
		if shouldDropKey(key) {
			continue
		}
		for _, sec := range secrets {
			if sec != "" && strings.Contains(key, sec) {
				return nil, ErrRedactKey
			}
		}
		sanitized, err := sanitizeValue(a.Value, secrets, 1)
		if err != nil {
			return nil, err
		}
		result[key] = sanitized
	}
	return result, nil
}
