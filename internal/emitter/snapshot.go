package emitter

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"example.invalid/hearthlog/internal/model"
)

const (
	maxAttrs  = MaxAttributes
	maxGroups = MaxNesting - 1 // root members are depth 1; each group adds a container level
	maxMsgLen = 8192
)

// ErrInvalidEvent marks an event the server would reject as a whole batch.
var ErrInvalidEvent = errors.New("emitter: event outside native admission rules")

var reservedRoot = map[string]struct{}{
	"workspace_id": {}, "source_id": {}, "received_at": {},
	"ingest_id": {}, "service": {}, "environment": {},
}

func reasonFor(err error) DropReason {
	switch {
	case errors.Is(err, ErrRedactDepth), errors.Is(err, ErrRedactMembers):
		return ReasonAttributeLimit
	case errors.Is(err, errTooLarge):
		return ReasonEventTooLarge
	case errors.Is(err, ErrInvalidEvent):
		return ReasonInvalidEvent
	default:
		return ReasonRedaction
	}
}

var errTooLarge = errors.New("emitter: attributes too large")

// WithAttrs resolves, redacts and copies attrs now, so later mutation of the
// caller's values cannot change what is sent. The receiver is never modified.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if h.invalid || len(attrs) == 0 {
		return h
	}
	base := cloneMap(h.base)
	added := map[string]any{}
	for _, a := range attrs {
		if err := addAttr(added, a, h.state.secrets, 1+len(h.groups)); err != nil {
			return &Handler{state: h.state, invalid: true, invalidReason: reasonFor(err)}
		}
	}
	if len(added) == 0 {
		return h
	}
	mergeUnderGroups(base, h.groups, added)
	if err := checkShape(base); err != nil {
		return &Handler{state: h.state, invalid: true, invalidReason: reasonFor(err)}
	}
	return &Handler{state: h.state, base: base, groups: h.groups}
}

// WithGroup qualifies later attributes. Groups with no attributes are omitted.
func (h *Handler) WithGroup(name string) slog.Handler {
	if h.invalid || name == "" {
		return h
	}
	if len(h.groups) >= maxGroups {
		return &Handler{state: h.state, invalid: true, invalidReason: ReasonAttributeLimit}
	}
	if !utf8.ValidString(name) || len(name) > maxKeyBytes ||
		shouldDropKey(name) || scrubString(name, h.state.secrets) != name {
		return &Handler{state: h.state, invalid: true}
	}
	groups := make([]string, len(h.groups)+1)
	copy(groups, h.groups)
	groups[len(h.groups)] = name
	return &Handler{state: h.state, base: h.base, groups: groups}
}

func (h *Handler) snapshot(r slog.Record) (model.Event, error) {
	rec := map[string]any{}
	var snapErr error
	r.Attrs(func(a slog.Attr) bool {
		if err := addAttr(rec, a, h.state.secrets, 1+len(h.groups)); err != nil {
			snapErr = err
			return false
		}
		return true
	})
	if snapErr != nil {
		return model.Event{}, snapErr
	}
	attrs := cloneMap(h.base)
	if len(rec) > 0 {
		mergeUnderGroups(attrs, h.groups, rec)
	}
	if err := checkShape(attrs); err != nil {
		return model.Event{}, err
	}
	for k := range attrs {
		if _, ok := reservedRoot[k]; ok {
			return model.Event{}, ErrInvalidEvent
		}
	}
	raw, err := json.Marshal(attrs)
	if err != nil {
		return model.Event{}, ErrRedactValue
	}
	if len(raw) > MaxEventBytes {
		return model.Event{}, errTooLarge
	}
	if err := model.ValidateAttributes(raw); err != nil {
		return model.Event{}, ErrInvalidEvent
	}

	msg := scrubString(r.Message, h.state.secrets)
	if len(msg) > maxMsgLen {
		msg = truncateUTF8(msg, maxMsgLen)
	}
	if !utf8.ValidString(msg) {
		msg = strings.ToValidUTF8(msg, "\uFFFD")
	}

	now := time.Now()
	t := r.Time
	if t.IsZero() {
		t = now
	}
	if t.After(now.Add(5*time.Minute)) || t.Before(now.Add(-7*24*time.Hour)) {
		return model.Event{}, ErrInvalidEvent
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

// addAttr applies slog's handler rules (ignore empty attrs and empty groups,
// inline groups with empty keys) and the redaction policy, writing an owned
// copy into dst. depth is the container depth of dst (root attributes = 1).
func addAttr(dst map[string]any, a slog.Attr, secrets []string, depth int) error {
	if a.Equal(slog.Attr{}) {
		return nil // zero Attr
	}
	v, err := resolve(a.Value)
	if err != nil {
		return err
	}
	if v.Kind() == slog.KindGroup {
		members := v.Group()
		if len(members) == 0 {
			return nil
		}
		if a.Key == "" {
			for _, m := range members {
				if err := addAttr(dst, m, secrets, depth); err != nil {
					return err
				}
			}
			return nil
		}
	}
	key := a.Key
	if !utf8.ValidString(key) || len(key) > maxKeyBytes {
		return ErrRedactKey
	}
	if shouldDropKey(key) || scrubString(key, secrets) != key {
		return nil
	}
	if v.Kind() == slog.KindGroup {
		if depth+1 > maxRedactDepth {
			return ErrRedactDepth
		}
		sub := map[string]any{}
		for _, m := range v.Group() {
			if err := addAttr(sub, m, secrets, depth+1); err != nil {
				return err
			}
			if len(sub) > maxRedactMembers {
				return ErrRedactMembers
			}
		}
		if len(sub) == 0 {
			return nil
		}
		if existing, ok := dst[key].(map[string]any); ok {
			for k, x := range sub {
				existing[k] = x
			}
			return nil
		}
		dst[key] = sub
		return nil
	}
	sv, err := scalar(v, secrets)
	if err != nil {
		return err
	}
	dst[key] = sv
	if len(dst) > maxRedactMembers {
		return ErrRedactMembers
	}
	return nil
}

// checkShape enforces the native limits on the final attribute object:
// <=128 members recursively and container depth <=8 (root members depth 1).
func checkShape(m map[string]any) error {
	n := 0
	var walk func(m map[string]any, depth int) error
	walk = func(m map[string]any, depth int) error {
		for _, v := range m {
			n++
			if n > maxAttrs {
				return ErrRedactMembers
			}
			if sub, ok := v.(map[string]any); ok {
				if depth+1 > maxRedactDepth {
					return ErrRedactDepth
				}
				if err := walk(sub, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(m, 1)
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

// mergeUnderGroups places src under the group path in dst (dst is owned).
func mergeUnderGroups(dst map[string]any, groups []string, src map[string]any) {
	cur := dst
	for _, g := range groups {
		next, ok := cur[g].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[g] = next
		}
		cur = next
	}
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := cur[k].(map[string]any); ok {
				mergeUnderGroups(dm, nil, sm)
				continue
			}
		}
		cur[k] = v
	}
}
