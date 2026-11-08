package model

import (
	"regexp"
	"time"
)

var timeRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,6})?(Z|[+-]\d{2}:\d{2})$`)

func validateTime(s string, now time.Time) (int64, error) {
	if !timeRe.MatchString(s) {
		return 0, ErrValidation
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, ErrValidation
	}
	t = t.UTC()
	if t.After(now.Add(5*time.Minute)) || t.Before(now.Add(-7*24*time.Hour)) {
		return 0, ErrValidation
	}
	return t.UnixMicro(), nil
}
