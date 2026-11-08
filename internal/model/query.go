package model

import (
	"encoding/json"
	"regexp"
	"time"
)

var sourceIDRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ValidateQuery enforces native v1 query bounds. Historical windows may end
// in the future relative to admission time; no event-time admission applies.
func ValidateQuery(q Query) error {
	if len(q.Sources) > 32 {
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
	if len(q.Terms) > 8 {
		return ErrValidation
	}
	for _, t := range q.Terms {
		if t == "" || len(t) > 128 {
			return ErrValidation
		}
	}
	if q.Limit < 1 || q.Limit > 200 {
		return ErrValidation
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
	if to.Sub(from) > 7*24*time.Hour {
		return ErrValidation
	}

	b, err := json.Marshal(q)
	if err != nil || len(b) > 4*1024 {
		return ErrValidation
	}
	return nil
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
