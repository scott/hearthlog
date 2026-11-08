package emitter

import (
	"encoding/json"
	"errors"
	"log/slog"
	"time"
	"unicode/utf8"

	"example.invalid/hearthlog/internal/model"
)

const (
	maxAttrs    = 128
	maxGroups   = 8
	maxMsgLen   = 8192
	maxAttrJSON = 16 * 1024
)

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if h.invalid {
		return h
	}
	if len(h.base)+len(attrs) > maxAttrs {
		return &Handler{state: h.state, invalid: true}
	}
	base := cloneMap(h.base)
	for _, a := range attrs {
		if shouldDropKey(a.Key) {
			continue
		}
		if !utf8.ValidString(a.Key) || len(a.Key) > 128 {
			return &Handler{state: h.state, invalid: true}
		}
		if scrubString(a.Key, h.state.secrets) != a.Key {
			continue
		}
		sanitized, err := sanitizeValue(a.Value, h.state.secrets, 1)
		if err != nil {
			return &Handler{state: h.state, invalid: true}
		}
		placeUnderGroups(base, h.groups, a.Key, sanitized)
	}
	return &Handler{state: h.state, base: base, groups: h.groups}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	if h.invalid {
		return h
	}
	if name == "" {
		return h
	}
	if len(h.groups) >= maxGroups {
		return &Handler{state: h.state, invalid: true}
	}
	if !utf8.ValidString(name) || len(name) > 128 {
		return &Handler{state: h.state, invalid: true}
	}
	if shouldDropKey(name) {
		return &Handler{state: h.state, invalid: true}
	}
	if scrubString(name, h.state.secrets) != name {
		return &Handler{state: h.state, invalid: true}
	}
	return &Handler{state: h.state, base: h.base, groups: append(append([]string{}, h.groups...), name)}
}

func (h *Handler) snapshot(r slog.Record) (model.Event, error) {
	attrs := cloneMap(h.base)
	count := len(h.base)
	r.Attrs(func(a slog.Attr) bool {
		if count >= maxAttrs {
			return false
		}
		if shouldDropKey(a.Key) {
			return true
		}
		if !utf8.ValidString(a.Key) || len(a.Key) > 128 {
			return true
		}
		if scrubString(a.Key, h.state.secrets) != a.Key {
			return true
		}
		sanitized, err := sanitizeValue(a.Value, h.state.secrets, 1)
		if err != nil {
			return true
		}
		placeUnderGroups(attrs, h.groups, a.Key, sanitized)
		count++
		return true
	})

	raw, err := json.Marshal(attrs)
	if err != nil || len(raw) > maxAttrJSON {
		return model.Event{}, errors.New("attributes too large")
	}

	msg := scrubString(r.Message, h.state.secrets)
	if len(msg) > maxMsgLen {
		msg = msg[:maxMsgLen]
	}
	if !utf8.ValidString(msg) {
		msg = "\uFFFD"
	}

	t := r.Time
	if t.IsZero() {
		t = time.Now()
	}
	t = t.UTC().Truncate(time.Microsecond)

	sevNum, sevText := mapLevel(r.Level)

	return model.Event{
		EventID:        newUUIDv4(),
		EventTime:      t.Format("2006-01-02T15:04:05.000000Z"),
		SeverityNumber: sevNum,
		SeverityText:   sevText,
		Message:        msg,
		Attributes:     raw,
	}, nil
}

func mapLevel(l slog.Level) (int, string) {
	switch {
	case l >= 12:
		return 21, "FATAL"
	case l >= slog.LevelError:
		return 17, "ERROR"
	case l >= slog.LevelWarn:
		return 13, "WARN"
	case l >= slog.LevelInfo:
		return 9, "INFO"
	case l >= slog.LevelDebug:
		return 5, "DEBUG"
	default:
		return 1, "TRACE"
	}
}

func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if em, ok := v.(map[string]any); ok {
			out[k] = cloneMap(em)
		} else {
			out[k] = v
		}
	}
	return out
}

func placeUnderGroups(m map[string]any, groups []string, key string, val any) {
	cur := m
	for _, g := range groups {
		if existing, ok := cur[g]; ok {
			if em, ok := existing.(map[string]any); ok {
				cur = em
				continue
			}
		}
		nm := map[string]any{}
		cur[g] = nm
		cur = nm
	}
	cur[key] = val
}
