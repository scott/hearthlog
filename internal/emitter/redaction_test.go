package emitter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
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

func TestSnapshotRejectsUnsupportedAttr(t *testing.T) {
	h, err := New("http://127.0.0.1:9999/api/experimental/skeleton/batches", "tok", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close(context.Background())

	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "synthetic", 0)
	rec.AddAttrs(slog.Any("arbitrary", struct{ X string }{"secret"}))
	if _, err := h.snapshot(rec); !errors.Is(err, ErrRedactValue) {
		t.Fatalf("snapshot err = %v, want ErrRedactValue", err)
	}

	before := h.state.drops.Load()
	if err := h.Handle(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if got := h.state.drops.Load(); got != before+1 {
		t.Fatalf("drops = %d, want %d", got, before+1)
	}
}

func TestSnapshotRejectsAttrOverflow(t *testing.T) {
	h, err := New("http://127.0.0.1:9999/api/experimental/skeleton/batches", "tok", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close(context.Background())

	cur := slog.Handler(h)
	for i := 0; i < maxAttrs; i++ {
		cur = cur.WithAttrs([]slog.Attr{slog.String(fmt.Sprintf("k%d", i), "v")})
	}
	hh, ok := cur.(*Handler)
	if !ok || hh.invalid {
		t.Fatal("filling to the attribute cap should stay valid")
	}

	extra := slog.NewRecord(time.Now(), slog.LevelInfo, "synthetic", 0)
	extra.AddAttrs(slog.String("extra", "v"))
	if _, err := hh.snapshot(extra); !errors.Is(err, ErrRedactMembers) {
		t.Fatalf("snapshot err = %v, want ErrRedactMembers", err)
	}

	secretOnly := slog.NewRecord(time.Now(), slog.LevelInfo, "synthetic", 0)
	secretOnly.AddAttrs(slog.String("password", "p"))
	if _, err := hh.snapshot(secretOnly); err != nil {
		t.Fatalf("dropped secret key at cap should not fail snapshot: %v", err)
	}
}

func TestMessageTruncationPreservesRunes(t *testing.T) {
	h, err := New("http://127.0.0.1:9999/api/experimental/skeleton/batches", "tok", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close(context.Background())

	prefix := strings.Repeat("a", maxMsgLen-1)
	rec := slog.NewRecord(time.Now(), slog.LevelInfo, prefix+"€", 0)
	ev, err := h.snapshot(rec)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(ev.Message) {
		t.Fatalf("truncated message is invalid UTF-8: %q", ev.Message)
	}
	if ev.Message != prefix {
		t.Fatalf("message = %q, want the %d-byte prefix", ev.Message, len(prefix))
	}

	bad := slog.NewRecord(time.Now(), slog.LevelInfo, "ok\xfftail", 0)
	ev, err = h.snapshot(bad)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Message != "ok\ufffdtail" {
		t.Fatalf("invalid sequence message = %q", ev.Message)
	}
}
