package model

import (
	"regexp"
	"time"
)

var timeRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,6})?(Z|[+-]\d{2}:\d{2})$`)

func parseEventTime(s string) (int64, error) {
	if !timeRe.MatchString(s) {
		return 0, ErrValidation
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, ErrValidation
	}
	return t.UTC().UnixMicro(), nil
}

// AdmitEventTime applies the native horizon: event_time is inclusive of
// [now-7d, now+5m]. Replay of an existing receipt does not call this.
func AdmitEventTime(micros int64, now time.Time) error {
	t := time.UnixMicro(micros).UTC()
	if t.After(now.Add(5*time.Minute)) || t.Before(now.Add(-7*24*time.Hour)) {
		return ErrValidation
	}
	return nil
}

func validateTime(s string, now time.Time) (int64, error) {
	micros, err := parseEventTime(s)
	if err != nil {
		return 0, err
	}
	if err := AdmitEventTime(micros, now); err != nil {
		return 0, err
	}
	return micros, nil
}
