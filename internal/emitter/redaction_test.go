package emitter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type atomicValuer struct {
	counter *atomic.Int64
}

func (a *atomicValuer) LogValue() slog.Value {
	return slog.StringValue(fmt.Sprintf("v%d", a.counter.Load()))
}

func TestRedactionSnapshot(t *testing.T) {
	const canary = "canary-secret-xyz"
	h, err := New("http://127.0.0.1:9999/api/experimental/skeleton/batches", "tok", []string{canary})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close(context.Background())

	var counter atomic.Int64
	counter.Store(1)
	av := &atomicValuer{counter: &counter}

	h2 := h.WithAttrs([]slog.Attr{
		slog.Any("mut", av),
		slog.Float64("real", 1.0),
		slog.Int64("int", 9007199254740993),
		slog.Bool("flag", true),
		slog.Any("nil", nil),
	})

	hh, ok := h2.(*Handler)
	if !ok {
		t.Fatal("WithAttrs did not return *Handler")
	}

	counter.Store(2)

	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "hello "+canary, 0)
	rec.AddAttrs(
		slog.Any("arbitrary", struct{ X string }{canary}),
		slog.String("password", "p"),
		slog.String("canary_attr", canary),
		slog.String("safe", "ok"),
	)

	ev, err := hh.snapshot(rec)
	if err != nil {
		t.Fatal(err)
	}

	if counter.Load() != 2 {
		t.Errorf("expected 2 capture, got %d", counter.Load())
	}

	if ev.Message != "hello [REDACTED]" {
		t.Errorf("message not scrubbed: %q", ev.Message)
	}

	dec := json.NewDecoder(bytes.NewReader(ev.Attributes))
	dec.UseNumber()
	var attrs map[string]any
	if err := dec.Decode(&attrs); err != nil {
		t.Fatal(err)
	}

	if v, ok := attrs["mut"]; !ok || v != "v1" {
		t.Errorf("mut not captured as v1: %v", v)
	}

	if _, ok := attrs["arbitrary"]; ok {
		t.Error("arbitrary Any should be dropped")
	}

	if _, ok := attrs["password"]; ok {
		t.Error("password key not stripped")
	}

	if v, ok := attrs["real"]; !ok {
		t.Error("real missing")
	} else if n, ok := v.(json.Number); !ok || n.String() != "1.0" {
		t.Errorf("real not 1.0: %v", v)
	}

	if v, ok := attrs["int"]; !ok {
		t.Error("int missing")
	} else if n, ok := v.(json.Number); !ok || n.String() != "9007199254740993" {
		t.Errorf("int not 9007199254740993: %v", v)
	}

	if v, ok := attrs["flag"]; !ok || v != true {
		t.Errorf("flag not true: %v", v)
	}

	if v, ok := attrs["nil"]; !ok || v != nil {
		t.Errorf("nil not preserved: %v", v)
	}

	if v, ok := attrs["canary_attr"]; !ok || v != "[REDACTED]" {
		t.Errorf("canary attr not scrubbed: %v", v)
	}

	if v, ok := attrs["safe"]; !ok || v != "ok" {
		t.Errorf("safe attr wrong: %v", v)
	}

	raw := string(ev.Attributes)
	if strings.Contains(raw, canary) {
		t.Error("canary present in raw attributes")
	}
}
