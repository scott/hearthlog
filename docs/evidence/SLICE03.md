# Slice 03 evidence: bounded redacting asynchronous slog SDK

Synthetic fixtures only. Numbers below are raw outputs from the commands shown,
on the declared generic test profile; they are not production measurements.
Limits are the provisional PLAN.md targets.

## Test profile (hardware class and versions)

| Item | Value |
|---|---|
| Target | linux/amd64, CGO_ENABLED=1, build tags `fts5 sqlite_omit_load_extension` |
| Toolchain | go1.27.3 (GOTOOLCHAIN=local), gcc 14.2.0, node v20.19.2 |
| Hardware class | generic x86_64 virtual machine, 8 vCPU (Intel Xeon class), 16 GiB RAM |
| SDK | `internal/emitter`, `SDKVersion = "0.3.0-dev"` |

## Gates (same commands as CI)

```bash
go mod verify                                                     # all modules verified
test -z "$(gofmt -l .)"                                           # clean
CGO_ENABLED=1 go test -race -count=1 -tags "fts5 sqlite_omit_load_extension" ./...   # all ok
CGO_ENABLED=1 go vet -tags "fts5 sqlite_omit_load_extension" ./...                   # clean
node --test internal/server/web/app.test.js                       # 4/4 pass
gitleaks dir --redact --no-banner && gitleaks git --redact --no-banner                # no leaks
```

`go test -race ./...`: 71 top-level tests (209 including subtests), 0 failures.
Flake check: `go test -race -count=4 ./internal/emitter ./examples/...` passed.

## Acceptance mapping

| Done-when / test item | Test(s) in `internal/emitter` unless noted |
|---|---|
| Concurrent Enabled/Handle/WithAttrs/WithGroup, immutable snapshots | `TestSlogtestConformance` (stdlib `testing/slogtest`; only deviation: zero time is stamped), `TestConcurrentHandlerMethodsRace`, `TestConcurrentGroupedEmit`, `TestSiblingDoesNotMutateParent`, `TestLevelFiltering` |
| Redact before enqueue/network, fail closed, canaries (messages, groups, WithAttrs, LogValuer, mutable values) | `TestCanaryAbsentOnWire`, `TestRedactionSnapshot`, `TestSnapshotRejectsUnsupportedAttr`, `TestWireFixtures` |
| Nonrecursive errors/counters | `TestDiagnosticHookNonRecursive` (hook that logs through the handler, panicking hook) |
| 2,048 events AND 8MiB queue, 32KiB event, nesting 8, 128 attributes, drop newest by reason/severity | `TestLimitsDropNewestWithAccounting/{queue_events,queue_bytes,event_size,nesting,attributes,invalid_event}` |
| Batch <=200 events / 256KiB / 250ms, <=2 in flight | `TestBatchingEventAndByteBounds`, `TestBatchIntervalAndInFlightLimit` (observed max concurrency exactly 2) |
| 5s requests, slow server | `TestSlowServerTimeoutRetriesSameBatch` (timeout shortened via test tuning; identical bytes resent) |
| Jittered retry <=60s for network/429/5xx, bounded Retry-After (fake clock) | `TestRetryBackoffAndBudgetFakeClock` (jitter 0/0.5/0.999), `TestRetryAfterBoundedByBudget` (seconds, HTTP-date, beyond budget, invalid) |
| Terminal 4xx | `TestTerminalStatusesAreNotRetried` (400/404/409/413/415/422/501/307), `TestUnauthorizedStopsSender` (401/403), `TestReceiptStrictJSON`, `TestVersionPresence`, `TestRedirectNotFollowed` |
| Unchanged batch identity after ambiguity | `TestAmbiguousCommitReplaysIdenticalBatch` (commit then lost response; replayed receipt; each event committed once) |
| Flush barrier, idempotent Close, deadline and unsent counts, no leaks | `TestConcurrentFlushBarrier`, `TestFlushDeadlineReportsUnsent`, `TestCloseDeadlineReportsUnsent` (900 unsent reported exactly), `TestCloseDrainsIsIdempotentAndLeakFree`, `TestLifecycleFlushClose`, `TestConcurrentHandleCloseNoPanic` |
| Canceled request context still logs | `TestCanceledRequestContextStillLogs` |
| Handler p95 <1ms at 5/s | `TestHandlerLatencyBaseline` (gate in non-race builds), below |
| 5min outage RSS delta <64MiB, overflow exactly accounted | `TestEndpointOutageBoundedAndAccounted`, below |
| Compilable integration example | `examples/slogservice` + its tests; [SDK guide](../SDK.md); local run below |

## Raw profile results

Baseline, 5/s for 5 minutes, ~1KiB synthetic events, healthy receiver, no race detector:

```bash
HEARTHLOG_SDK_BASELINE=5m CGO_ENABLED=1 go test -count=1 -tags "fts5 sqlite_omit_load_extension" -run TestHandlerLatencyBaseline -v ./internal/emitter
```

```json
{"profile":"baseline_5ps","race":false,"go_version":"go1.27.3","goos":"linux","goarch":"amd64","num_cpu":8,"duration":"5m0s","rate_per_sec":5,"samples":1500,"handle_p50":"94.943µs","handle_p95":"162.93µs","handle_p99":"198.313µs","handle_max":"588.744µs","server_committed":1500,"stats":{"Handled":1500,"Enqueued":1500,"SentBatches":750,"AckedEvents":1500,"Retries":0,"Dropped":{}}}
```

Endpoint outage, 5 minutes at 100/s (the optional higher rate is used so the
queue overflows), connections dropped by the receiver, then restored:

```bash
HEARTHLOG_SDK_OUTAGE=5m HEARTHLOG_SDK_OUTAGE_RATE=100 CGO_ENABLED=1 go test -count=1 -tags "fts5 sqlite_omit_load_extension" -run TestEndpointOutageBoundedAndAccounted -v -timeout 20m ./internal/emitter
```

```json
{"profile":"outage","race":false,"duration":"5m0s","rate_per_sec":100,"samples":30000,"handle_p50":"59.779µs","handle_p95":"75.64µs","handle_p99":"93.786µs","handle_max":"1.006379ms","rss_baseline_mib":16.07,"rss_peak_mib":23.60,"rss_delta_mib":7.54,"server_committed":2648,"stats":{"Handled":30200,"Enqueued":4299,"QueuedEvents":0,"InFlightBatches":0,"SentBatches":14,"AckedEvents":2648,"Retries":205,"RejectedBatches":10,"ShutdownUnsent":0,"Dropped":{"queue_full":{"INFO":25901},"retry_exhausted":{"INFO":1651}}},"extra":{"heap_inuse_peak_bytes":10199040,"queued_bytes_at_outage_end":2455104,"queued_events_at_outage_end":2048}}
```

Accounting: 30,200 handled (200 warm-up + 30,000) = 2,648 acked + 25,901
`queue_full` + 1,651 `retry_exhausted`; the receiver committed exactly the
2,648 acked events, each once. The first 5-minute run reported an RSS delta of
73MiB; the cause was the test receiver keeping every retried request body in
memory. The receiver was changed to stop retaining bodies in this profile (SDK
code unchanged) and the run above passed. Both logs are kept with the local
evidence bundle.

Optional microbenchmark (not a gate): `go test -run '^$' -bench Handle ./internal/emitter`
gave about 31µs/op, 45KB/op, 315 allocs/op for a ~1KiB event.

## Local integration run (example to the Slice 02 server)

Commands follow [SDK.md](../SDK.md#reproduce-locally-synthetic) with a temporary
state directory. Sanitized stdout (local stderr logging continuing while remote
logging is disabled or failing is asserted by `examples/slogservice` tests):

```text
run 1 skeleton route, valid token : {"acked":21,"handled":21,"sent_batches":21,"dropped":{},"shutdown_unsent":0,"flush_ok":true,"close_ok":true} exit 0
run 2 remote disabled            : {"remote":"disabled"} exit 0
native route on this server      : {"acked":0,"dropped":{"rejected":{"INFO":1,"WARN":1}}} exit 1  (404 until Slice 04)
rotate write.token (new 64-hex value, 0600), restart server
run 3 old (revoked) token        : {"acked":0,"dropped":{"unauthorized":{"INFO":5,"WARN":1}},"retries":0} exit 1
run 4 new token                  : {"acked":6,"handled":6,"dropped":{}} exit 0
```

## Not run / limits

- The native `/api/v1/batches` route is not served until Slice 04, so multi-event
  batches, replay receipts and ambiguity handling are proven against the
  synthetic test receiver, not the real server.
- CI runs the short profile defaults (2s) under `-race`, where the p95 gate only
  logs; the 5-minute profiles above were run locally without `-race`.
- No remote/TLS endpoint, durable spool, local replay fallback or delivery
  guarantee (non-goals). Overlapping token rotation is Slice 08.
