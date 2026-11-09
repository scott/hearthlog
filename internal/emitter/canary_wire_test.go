package emitter

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func randomCanary(t *testing.T) string {
	t.Helper()
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "canary_" + hex.EncodeToString(b)
}

// mutableValuer returns whatever its pointer holds at LogValue time.
type mutableValuer struct{ v *atomic.Pointer[string] }

func (m mutableValuer) LogValue() slog.Value { return slog.StringValue(*m.v.Load()) }

type secretGroupValuer struct{ canary string }

func (s secretGroupValuer) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("user", "demo-user"),
		slog.String("api_token", s.canary),           // secret key: stripped
		slog.String("note", "has "+s.canary+" here"), // secret value: scrubbed
		slog.Any("nested", nestedValuer{s.canary}),
	)
}

type nestedValuer struct{ canary string }

func (n nestedValuer) LogValue() slog.Value {
	return slog.GroupValue(slog.String("Authorization", "Bearer "+n.canary), slog.String("ok", "visible"))
}

type panicValuer struct{}

func (panicValuer) LogValue() slog.Value { panic("synthetic panic with internal detail") }

// TestCanaryAbsentOnWire proves redaction happens before enqueue for messages,
// WithAttrs, groups, LogValuers and mutable values: neither the configured
// canary nor the write token appears in any wire body, diagnostic or stat,
// while positive controls show the surrounding data did arrive.
func TestCanaryAbsentOnWire(t *testing.T) {
	canary := randomCanary(t)
	ns := newNativeServer(t)
	var diags bytes.Buffer
	h := newTestHandler(t, Config{
		Endpoint: ns.endpoint(),
		Secrets:  []string{canary},
		OnDiagnostic: func(d Diagnostic) {
			fmt.Fprintf(&diags, "%+v\n", d)
		},
	}, nil, tuning{})

	cur := new(atomic.Pointer[string])
	first := "value-at-log-time"
	cur.Store(&first)
	mv := mutableValuer{cur}

	base := slog.New(h).
		With("component", "demo-api", "with_secret", canary, "session_cookie", canary, "mut_with", mv).
		WithGroup("request")
	base.Info("message carries "+canary+" inline",
		"path", "/synthetic",
		slog.Group("headers", slog.String("x", canary)), // default-deny headers
		slog.Group("ctx", slog.String("password", canary), slog.String("echo", canary), slog.String("keep", "ok")),
		slog.Any("valuer", secretGroupValuer{canary}),
		"mut", mv,
	)
	// Mutation after Handle must not change the already-snapshotted event.
	later := "mutated-" + canary
	cur.Store(&later)
	// Fail closed: a panicking LogValuer drops the event; no stack text is sent.
	base.Warn("panics", "bad", panicValuer{})
	// The canary embedded in a key is never sent either.
	base.Info("key scrub", canary+"_key", "v")
	// Token value itself is always scrubbed.
	base.Info("token " + testToken)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = h.Flush(ctx)

	bodies, _, _ := ns.snapshot()
	if len(bodies) == 0 {
		t.Fatal("no wire bodies")
	}
	all := bytes.Join(bodies, []byte("\n"))
	for _, needle := range []string{canary, testToken, "synthetic panic", "goroutine", "mutated-"} {
		if bytes.Contains(all, []byte(needle)) {
			t.Fatalf("wire contains %q", needle)
		}
	}
	for _, positive := range []string{
		`"component":"demo-api"`, `"path":"/synthetic"`, `"keep":"ok"`, `"user":"demo-user"`,
		`"ok":"visible"`, `"mut_with":"value-at-log-time"`, `"mut":"value-at-log-time"`,
		`message carries [REDACTED] inline`, `"echo":"[REDACTED]"`, `"note":"has [REDACTED] here"`,
		`token [REDACTED]`,
	} {
		if !bytes.Contains(all, []byte(positive)) {
			t.Fatalf("positive control missing %s in %s", positive, all)
		}
	}
	for _, removed := range []string{"with_secret", "session_cookie", "headers", "password", "api_token", "Authorization"} {
		if bytes.Contains(all, []byte(`"`+removed+`"`)) {
			t.Fatalf("secret key %q present on wire", removed)
		}
	}
	st := h.Stats()
	if st.Dropped["redaction_failed"]["WARN"] != 1 || st.AckedEvents != 3 {
		t.Fatalf("stats %+v", st)
	}
	statText := fmt.Sprintf("%+v", st) + diags.String()
	if strings.Contains(statText, canary) || strings.Contains(statText, testToken) {
		t.Fatal("canary in diagnostics/stats")
	}
	if !strings.Contains(diags.String(), "Reason:redaction_failed Severity:WARN") {
		t.Fatalf("expected a redaction diagnostic, got %q", diags.String())
	}
	checkAccounting(t, st)
}
