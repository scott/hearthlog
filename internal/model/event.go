package model

import (
	"encoding/json"
	"regexp"
	"time"
)

var hexRe = regexp.MustCompile(`^[0-9a-f]+$`)

func validateEvent(e *Event, now time.Time) error {
	if !uuidRe.MatchString(e.EventID) {
		return ErrValidation
	}
	micros, err := validateTime(e.EventTime, now)
	if err != nil {
		return err
	}
	e.EventTimeMicros = micros

	if e.SeverityNumber < 1 || e.SeverityNumber > 24 {
		return ErrValidation
	}
	group := (e.SeverityNumber - 1) / 4
	if e.SeverityText != severityTexts[group] {
		return ErrValidation
	}

	if len(e.Message) > 16*1024 {
		return ErrValidation
	}

	if err := ValidateAttributes(e.Attributes); err != nil {
		return err
	}

	if e.TraceID != nil {
		if len(*e.TraceID) != 32 || !hexRe.MatchString(*e.TraceID) || *e.TraceID == "00000000000000000000000000000000" {
			return ErrValidation
		}
	}
	if e.SpanID != nil {
		if e.TraceID == nil {
			return ErrValidation
		}
		if len(*e.SpanID) != 16 || !hexRe.MatchString(*e.SpanID) || *e.SpanID == "0000000000000000" {
			return ErrValidation
		}
	}

	compact, err := json.Marshal(e)
	if err != nil || len(compact) > 32*1024 {
		return ErrValidation
	}
	return nil
}
