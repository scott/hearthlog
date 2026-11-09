# Go slog SDK integration (Slice 03)

Status: in-repository SDK revision `0.3.0-dev` (`emitter.SDKVersion`) in
`internal/emitter`. It is importable only from inside this module until a
later slice publishes a versioned package; the compilable example lives at
[`examples/slogservice`](../examples/slogservice/main.go). All names, endpoints
and values below are synthetic placeholders. This is not remote-readiness or
production approval.

## What the handler guarantees

| Area | Behavior (provisional limits from PLAN.md) |
|---|---|
| slog API | `Enabled`, `Handle`, `WithAttrs`, `WithGroup` are safe for concurrent use; derived handlers are immutable snapshots. Passes `testing/slogtest` except "zero time is ignored" (the native contract requires `event_time`, so the SDK stamps the current time). |
| Redaction before enqueue | Records are resolved (including `LogValuer`, bounded, panic-safe), redacted and serialized synchronously inside `Handle`; no application value is retained. Keys containing `password`, `secret`, `token`, `authorization`, `cookie`, `header`, `body`, `credential` are removed at every level; the write token and configured literal secrets are replaced with `[REDACTED]` in messages, string values and keys. Arbitrary objects (`slog.Any` of non-nil non-LogValuer values) fail closed. |
| Fail closed | Redaction/conversion failures, >128 attributes, nesting >8, events >32KiB, reserved root keys (`service`, `environment`, ...) and event times outside the server window drop that event with a counted reason. Nothing is written to stderr. |
| Bounded queue | 2,048 events AND 8MiB serialized bytes; overflow drops the newest event and counts it by reason and severity. `Handle` never waits for the network. |
| Batching | <=200 events or <=256KiB per request, sent when full or 250ms after the oldest queued event; at most 2 requests in flight; 5s per request. The skeleton route sends 1 event per batch. |
| Retry | Network errors, timeouts, 429, 500/502/503/504 resend the identical bytes and `batch_id` with jittered exponential backoff (250ms..5s) or `Retry-After`, bounded by a 60s budget per batch. `Retry-After` is clamped to at least 250ms, so `0` or a past HTTP-date cannot cause a tight loop. A `Retry-After` beyond the remaining budget ends the batch immediately. Only 500/502/503/504 are retried among 5xx, on purpose: other 5xx (for example 501, 505, 507) are treated as terminal. Batches are never split or re-identified. |
| Terminal | 400/404/409/413/415/422/501 and redirects (never followed) drop the batch as `rejected`. 401/403 stop the sender: queued and later events count as `unauthorized` until a new handler is built with a valid token. |
| Flush / Close | `Flush(ctx)` waits for events accepted before the call; on deadline it returns `*DeliveryError` with `Unsent`. `Close(ctx)` stops acceptance, drains, and at the deadline cancels in-flight work and counts abandoned events as `shutdown_unsent`. Close is idempotent and leaves no goroutines. Both also report `ErrUndelivered` if any event was dropped since start. |
| Diagnostics | `Stats()` counters plus an optional `OnDiagnostic` hook that receives only reason, severity, count and HTTP status. The hook never recurses (nested or concurrent calls are counted as `DiagnosticsSuppressed`) and is never called while the SDK holds its lock. |

Errors: `slog.Any("err", err)` passes an arbitrary object, which fails closed
and drops the whole event (`redaction_failed`). Log the text instead:
`slog.String("err", err.Error())` or `"err", err.Error()`. The string is still
scrubbed of the token and configured secrets, but free text is not guaranteed
PII-free, so only log error text you would accept on the wire.

Non-goals: no durable spool, no local replay fallback, no delivery guarantee.
SIGKILL or a Close deadline can lose queued and in-flight events; they are
counted when the process lives long enough to count them.

Drop reasons (bounded labels): `redaction_failed`, `attribute_limit`,
`event_too_large`, `invalid_event`, `queue_full`, `queue_bytes`, `closed`,
`unauthorized`, `rejected`, `invalid_receipt`, `retry_exhausted`,
`shutdown_unsent`. Accounting invariant:
`Handled == AckedEvents + TotalDropped() + QueuedEvents + InFlightEvents`.

## Endpoint

The endpoint path selects the envelope:

| Endpoint | Envelope | Server support today |
|---|---|---|
| `http://127.0.0.1:<PORT>/api/v1/batches` | NATIVE-V1 `version: 1`, <=200 events | Not yet served (404) until Slice 04 |
| `http://127.0.0.1:<PORT>/api/experimental/skeleton/batches` | Slice 02 skeleton `version: 0`, 1 event | Served by the local skeleton |

Only loopback `http` URLs with an explicit port are accepted, matching the
loopback-only server. Remote/TLS endpoints are out of scope for this slice.

## Integrate

```go
h, err := emitter.NewWithConfig(emitter.Config{
    Endpoint: "<HEARTHLOG_ENDPOINT>",      // operator setting, e.g. http://127.0.0.1:<PORT>/api/v1/batches
    Token:    tokenFromFile,              // read from a 0600 file outside the repo, never a flag/env literal in source
    Secrets:  extraLiteralsToScrub,       // optional
    Level:    slog.LevelInfo,
    OnDiagnostic: func(d emitter.Diagnostic) { /* increment your own metric; must not block */ },
})
if err != nil {
    // keep the existing local handler only (safe fallback)
}
logger := slog.New(slog.NewMultiHandler(existingLocalHandler, h))
...
flushCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
_ = h.Flush(flushCtx); cancel()
closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
err = h.Close(closeCtx); cancel()       // *emitter.DeliveryError reports Unsent / Dropped
```

## Reproduce locally (synthetic)

Prerequisites as in [SLICE02.md](SLICE02.md) (Go 1.27.3, CGO, build tags).

```bash
umask 077
BUILD_DIR=$(mktemp -d); STATE_DIR=$(mktemp -d); chmod 700 "$STATE_DIR"
CGO_ENABLED=1 go build -tags "fts5 sqlite_omit_load_extension" -o "$BUILD_DIR/hearthlog" ./cmd/hearthlog
go build -o "$BUILD_DIR/slogservice" ./examples/slogservice

# Provision: the server creates a 0600 write.token in STATE_DIR on first start.
"$BUILD_DIR/hearthlog" -state "$STATE_DIR" -listen 127.0.0.1:<PORT> &
"$BUILD_DIR/slogservice" -endpoint http://127.0.0.1:<PORT>/api/experimental/skeleton/batches \
  -token-file "$STATE_DIR/write.token" -events 20      # prints counters JSON, exit 0

# Safe disable: omit -endpoint. Local logging continues unchanged.
"$BUILD_DIR/slogservice" -events 2

# Rotate/revoke (local skeleton): stop the server, write a new 64-hex token to
# write.token with mode 0600, restart. The old token now gets 401: the SDK stops
# sending and counts the events as "unauthorized"; restart the app with the new
# token file to resume. Overlapping rotation is Slice 08 server work.
```

## Rollout

Stage with remote logging disabled, then enable for one synthetic or staging
service while its prior local handler stays in the `MultiHandler`. Watch
`Stats()` (drops by reason, retries, shutdown_unsent). Disable immediately by
removing the endpoint setting or revoking the token; prior safe local logging
is unaffected either way. Real-application rollout needs separate approval.
