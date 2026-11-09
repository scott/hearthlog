# Slice 04 evidence: SQLite single writer and atomic native replay

Synthetic fixtures only. Numbers below are raw outputs from the commands shown,
on the declared generic test profile; they are not production measurements.
`POST /api/v1/batches` is served. The experimental skeleton route is unchanged:
version 0, one event, replay returns 501.

## Test profile

| Item | Value |
|---|---|
| Target | linux/amd64, CGO_ENABLED=1, build tags `fts5 sqlite_omit_load_extension` |
| Toolchain | go1.27.3 (GOTOOLCHAIN=local), gcc 13.3.0, node v22.23.4 |
| Hardware class | generic x86_64 virtual machine, 4 vCPU, 15 GiB RAM reported by the guest |
| SQLite | 3.54.1 (read back after reopening the file on a replacement connection) |

## Gates

```bash
go mod verify                                                     # all modules verified
test -z "$(gofmt -l .)"                                           # clean
CGO_ENABLED=1 go test -race -count=1 -tags "fts5 sqlite_omit_load_extension" -timeout 15m ./...
CGO_ENABLED=1 go vet -tags "fts5 sqlite_omit_load_extension" ./...
node --test internal/server/web/app.test.js                       # 4/4 pass
```

Race package results (0 failures):

```text
ok  example.invalid/hearthlog/examples/slogservice   1.037s
ok  example.invalid/hearthlog/internal/emitter      21.609s
ok  example.invalid/hearthlog/internal/model         1.045s
ok  example.invalid/hearthlog/internal/server        6.168s
ok  example.invalid/hearthlog/internal/storage/sqlite 4.708s
```

Same tags without `-race`, counted from `go test -json`: 97 top-level tests,
271 including subtests, 0 failures (examples 4, emitter 38/108, model 8/65,
server 26/63, sqlite 21/31). `go vet` was clean.

Remote CI is the GitHub Actions job named `test` (ubuntu-latest, Go 1.27.3,
the same race/vet/gofmt/node/gitleaks steps). The pull request records that
run id for the head SHA. gitleaks v8.30.1 was not on PATH in this environment,
so the leak scan is the CI step.

## SQLite readback

`TestPragmaReadbackReplacement` after close and reopen:

```text
sqlite_version=3.54.1 fts5=1 source_id=2026-10-16 14:45:20 555e07ef33f69a0957d24d8e555b702464a131d6f1607f4c3c1e4551e8e6ca76
compile_options=ATOMIC_INTRINSICS=1,COMPILER=gcc-13.3.0,DEFAULT_AUTOVACUUM,DEFAULT_CACHE_SIZE=-2000,DEFAULT_FILE_FORMAT=4,DEFAULT_JOURNAL_SIZE_LIMIT=-1,DEFAULT_MMAP_SIZE=0,DEFAULT_PAGE_SIZE=4096,DEFAULT_PCACHE_INITSZ=20,DEFAULT_RECURSIVE_TRIGGERS,DEFAULT_SECTOR_SIZE=4096,DEFAULT_SYNCHRONOUS=2,DEFAULT_WAL_AUTOCHECKPOINT=1000,DEFAULT_WAL_SYNCHRONOUS=1,DEFAULT_WORKER_THREADS=0,DIRECT_OVERFLOW_READ,ENABLE_FTS3,ENABLE_FTS3_PARENTHESIS,ENABLE_FTS5,ENABLE_RTREE,ENABLE_UPDATE_DELETE_LIMIT,MALLOC_SOFT_LIMIT=1024,MAX_ATTACHED=10,MAX_COLUMN=2000,MAX_COMPOUND_SELECT=500,MAX_DEFAULT_PAGE_SIZE=8192,MAX_EXPR_DEPTH=1000,MAX_FUNCTION_ARG=1000,MAX_LENGTH=1000000000,MAX_LIKE_PATTERN_LENGTH=50000,MAX_MMAP_SIZE=0x7fff0000,MAX_PAGE_COUNT=0xfffffffe,MAX_PAGE_SIZE=65536,MAX_SCHEMA=10000000,MAX_SQL_LENGTH=1000000000,MAX_TRIGGER_DEPTH=1000,MAX_VARIABLE_NUMBER=32766,MAX_VDBE_OP=250000000,MAX_WORKER_THREADS=8,MUTEX_PTHREADS,OMIT_DEPRECATED,OMIT_LOAD_EXTENSION,SYSTEM_MALLOC,TEMP_STORE=1,THREADSAFE=1
```

Writer connection applies journal_mode=WAL, synchronous=FULL (2),
foreign_keys=ON, busy_timeout=250, locking_mode=NORMAL, auto_vacuum=INCREMENTAL
(2). `user_version` stays 1. A database created by the previous schema, with no
`receipts` table, fails open (`TestSchemaWithoutReceiptsRefused`). A second
process fails the exclusive directory lock (`TestSecondInstanceRefuses`).
`TestWriterBeginImmediateLock` holds `BEGIN IMMEDIATE` on the writer so a
second connection with `busy_timeout=0` cannot start its own immediate
transaction.

Canonical digest golden, locked by `TestCanonicalHashIdentity`:

```text
8223eca21e15a356cd5e19e1345a8dfec914d8599a59cc79558a8b4f2f5f80cf
```

Single-event vector: batch `00000000-0000-4000-8000-000000000001`, event
`00000000-0000-4000-8000-000000000002`, time `2026-01-02T03:04:05.123456Z`,
INFO, message `synthetic task completed`, attributes `{"b":true,"a":1}`.

## Queue, commit, and fault logs

Command:

```bash
CGO_ENABLED=1 go test -count=1 -tags "fts5 sqlite_omit_load_extension" -timeout 180s -v \
  -run 'TestSchedulerAdmission|TestConcurrentWritersSerialized|TestBusyRetryAndRecovery|TestAtomicMidBatch|TestIdempotentReplayAndConflict|TestConcurrentReplay|TestReceiptSurvivesLogDeletion|TestKillRestartReplay|TestCommitLatencyLog|TestNativeBatchOf200|TestPragmaReadbackReplacement|TestWriterBeginImmediateLock|TestSecondInstanceRefuses' \
  ./internal/storage/sqlite/
```

```text
admission_reject_after=100.442123ms cap_jobs=64
queue_byte_cap=16777216 rejected_extra=1 rejected_single=16777217
per_source_cap=32 other_source_queued=1 extra_rejected=1
service_order=[A B A A A A A A A A A]
maintenance_order=[A A A A A A A A A M A A A A A A]
concurrent_writers=32 logs=32 receipts=32 watermark=32
after_busy_release logs=2 receipts=1 watermark=2
mid_batch_failure logs=1 receipts=1 watermark=1 err=storage error
replay_equivalent=1 conflict_rows_unchanged=1 logs=3 watermark=3
receipt_retained_without_logs watermark=1
cycle=before_commit logs=0 receipts=0 watermark=0
cycle=post_commit logs=2 receipts=1 watermark=2 replay_accepted=2
cycle=clean_restart logs=2 receipts=1 watermark=2
cycle=midnight logs=1 watermark=1 receipt={"version":1,"batch_id":"70000000-0000-4000-8000-000000000001","accepted":1,"first_ingest_id":"1","last_ingest_id":"1","received_at":"2026-01-02T23:30:00Z","replay_until":"2026-01-04T23:30:00Z"}
cycle=expiry logs=3 receipts=2 watermark=3
commit_latency_n=20 max=417.443µs total=7.027694ms logs=20 watermark=20
```

`TestNativeBatchOf200` commits one 200-event batch and checks the FTS row
count matches. HTTP coverage (`internal/server`, same tags, non-race, all
pass): `TestNativeBatchReplayAndConflict` (identical replay body, 409
`batch_conflict`, 3 rows remain), `TestNativeReceiptOnWireTypes`,
`TestNativeGzipAndOversize` (gzip accepted; bad gzip 400; `br` 415; raw,
compressed, and decompressed oversize 413; rejected batches leave 1 log row),
`TestNativeUnauthenticated` (401), `TestNativeBusy503` (503 and
`Retry-After: 5` while an external `BEGIN IMMEDIATE` is held). SDK decode:
`TestNativeReceiptDecodeTypes`.

## Acceptance mapping

| Done-when item | Proof |
|---|---|
| One instance, one writer, queue <=64 jobs AND 16MiB, admission wait <=100ms, per-source fairness, 503 + Retry-After | `TestSecondInstanceRefuses`, `TestWriterBeginImmediateLock`, `TestSchedulerAdmission` (reject after 100.442ms at 64 jobs; 16777216 byte cap; per-source 32 when another source is queued; round-robin), `TestConcurrentWritersSerialized` (32 writers, no `SQLITE_BUSY` leaked), `TestNativeBusy503` |
| BEGIN IMMEDIATE commits logs, FTS, watermark, and receipt together; FULL sync; ack after commit | `TestAtomicMidBatch` (trigger aborts the second event; prior receipt/watermark stay), `TestNativeBatchOf200`, pragma readback, `TestCommitLatencyLog` |
| Same key and hash returns the original receipt; changed content is 409; concurrent replay; midnight; receipt kept >=48h; no global exactly-once claim | `TestIdempotentReplayAndConflict`, `TestConcurrentReplay`, `TestNativeBatchReplayAndConflict`, `TestReceiptSurvivesLogDeletion`, `TestKillRestartReplay/midnight`, `TestKillRestartReplay/expiry` (at `replay_until` the old receipt is dropped and a new insert is allowed) |
| WAL, foreign_keys, busy_timeout 250ms, locking_mode NORMAL, auto_vacuum INCREMENTAL; bounded busy recovery; no half-transaction replay and no new batch id | pragma readback, `TestBusyRetryAndRecovery` (8 whole-transaction retries, then commit of the same batch), `TestKillRestartReplay/before_commit` and `/post_commit_pre_response` |
| Five kill/restart/replay cycles, including post-commit/pre-response; acknowledged batches stay duplicate-free inside the window; invalid or oversize compressed and decompressed batches reject atomically | `TestKillRestartReplay` subtests `before_commit`, `post_commit_pre_response`, `clean_restart`, `midnight`, `expiry`; `TestNativeGzipAndOversize` |
| Receipt fields are JSON strings (`first_ingest_id`, `last_ingest_id`, `replay_until`) | `TestEncodeNativeReceiptTypes`, `TestParseNativeReceiptRejectsWrongTypes`, `TestNativeReceiptOnWireTypes`, `TestNativeReceiptDecodeTypes`. This check is stricter than the issue text; see the pull request |

## Judgment calls

- `user_version` stays 1 so `TestFutureSchema` still refuses version 2. Opening
  a version-1 file that has no `receipts` table fails closed. There is no
  v1-to-v2 migration because no database has shipped.
- `UNIQUE(workspace_id, source_id, batch_id)` was removed from `logs` so one
  batch can hold many events and an expired receipt can insert the same event
  ids again. The receipt primary key is that triple. Duplicate event ids
  inside one request are rejected before admission.
- Maintenance is a scheduler hook (`SubmitMaintenance`): one maintenance job
  runs after at most 10 ingest jobs. This slice does not checkpoint or sweep
  retention on a timer.
- Kill cycles close the writer connection in process (`faultBeforeCommit`,
  `faultAfterCommit`). They are not an OS `SIGKILL` and not a power cut.
- A lone source may use all 64 queue slots. The per-source cap of 32 applies
  when another source already has queued work.
- Admission timing under test accepts a wait from half of 100ms through 750ms
  so `-race` does not flake. The recorded reject wait is 100.442ms.
- At exactly `replay_until` the stored receipt is no longer honored. A replay
  is admitted as a new batch and may insert additional rows. `now` strictly
  before `replay_until` returns the stored body.
- Event-time horizon is applied only on the insert path, after the receipt
  lookup, so a lost ack can replay across the horizon. New batches use one
  captured `now`.
- Source and workspace on the native route are still the synthetic demo
  credential (`ws_demo_a` / `src_demo_api`). Revocation is a later slice.
  Scope recheck is "the source row exists".
- Ambiguous `Commit` errors look up the receipt for the same hash and return
  it when it is still inside the window. The caller does not mint a new batch
  id.

## Not run

- Power loss and OS `SIGKILL`. Durability is the SQLite FULL/WAL assumption
  plus the in-process connection-close cycles above.
- The plan's 50,000-event / 30-minute baseline and checkpoint-pressure
  profile. This slice records the 20-commit latency log and the 200-event
  batch, not that baseline.
- Automatic checkpoint loop and receipt-retention sweeper.
- A second normal writer, a cross-host shared database, and a client durable
  spool.
- Local gitleaks. The CI `test` job runs gitleaks v8.30.1 on the directory
  and the git history.
