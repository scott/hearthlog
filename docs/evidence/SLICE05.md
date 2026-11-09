# Slice 05 evidence: indexed search and keyset cursors

Synthetic fixtures only. Numbers below are raw outputs from the commands shown,
on the declared generic test profile; they are not production measurements.
`user_version` stays 1. Search pages use a signed keyset cursor. Live is not
this slice.

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
ok  example.invalid/hearthlog/cmd/hearthlog             1.012s
ok  example.invalid/hearthlog/examples/slogservice      1.046s
ok  example.invalid/hearthlog/internal/model            1.047s
ok  example.invalid/hearthlog/internal/server           6.400s
ok  example.invalid/hearthlog/internal/storage/sqlite  10.965s
ok  example.invalid/hearthlog/internal/emitter         21.941s
```

Same command with `-json`: 113 top-level tests, 319 including subtests,
0 failures (cmd 1, examples 4, emitter 38/108, model 10/75, server 30/73,
sqlite 30/58). `go vet` was clean. Node: 4 pass, 0 fail.

Remote CI is the GitHub Actions job named `test` (ubuntu-latest, Go 1.27.3,
the same race/vet/gofmt/node/gitleaks steps). The pull request records that
run id for the head SHA. gitleaks v8.30.1 was not on PATH in this environment,
so the leak scan is the CI step.

## SQLite readback

`TestPragmaReadbackReplacement` after close and reopen:

```text
sqlite_version=3.54.1 fts5=1 source_id=2026-10-16 14:45:20 555e07ef33f69a0957d24d8e555b702464a131d6f1607f4c3c1e4551e8e6ca76
```

`user_version` stays 1. A file with no `receipts` table fails open as
`ErrOlderSlice`. `TestOlderSliceOpenMessage` checks the process log text:
`database is from an older slice and cannot be opened; start the service with a new -state directory`.

## Baseline, plans, and budgets

`TestBaselineSearch` under `-race` (one maintenance transaction, FTS triggers
on, then `ANALYZE`):

```text
seed_n=50000 seed_elapsed=2.74138323s
explain_source_time=SEARCH l USING INDEX idx_logs_time (workspace_id=? AND source_id=? AND event_time>? AND event_time<?)
explain_term=SCAN logs_fts VIRTUAL TABLE INDEX 0:M1
    SEARCH l USING INTEGER PRIMARY KEY (rowid=?)
    USE TEMP B-TREE FOR ORDER BY
samples=37 p95=2.408434ms max=2.412826ms ingest=15 live_reads=429 live_rows=50015
pathological_steps=100000 elapsed=5.671563ms
```

The ingest side is a 200ms ticker (5/s) held for 3 seconds: 15 commits.
Representative pages are a source/time window, the literal term
`representative`, and an integer attribute equality. p95 is under 1s.
The common-token plus non-matching JSON predicate returns `BudgetError` at
100000 steps, not an empty page. Elapsed is under 5s.

`TestIndexesAndExplain` on one row:

```text
explain_time=SEARCH l USING INDEX idx_logs_time (workspace_id=? AND source_id=? AND event_time>? AND event_time<?)
explain_fts=SEARCH l USING INDEX idx_logs_time (workspace_id=? AND source_id=? AND event_time>? AND event_time<?)
    SCAN logs_fts VIRTUAL TABLE INDEX 0:=M1
```

Indexes present: `idx_logs_time`, `idx_logs_ws_time`, `idx_logs_ws_ingest`,
`idx_logs_retention`, plus external-content `logs_fts`.

`TestReaderPoolAndInterrupt`:

```text
vm_steps=100000 elapsed=779.63µs
interrupt_elapsed=80.478774ms
```

Reader `MaxOpenConnections` is 4. A fifth checkout blocks. Eight sequential
pages return the connection (`ReaderInUse` is 0 after each). A recursive CTE
trips the progress handler at exactly 100000 steps. A canceled context
interrupts the statement.

`TestResultHTTPBytes/rawoverflow` renders a bounded page at 2084544 bytes
(under 2097152) with `id="btn-next"`. `escapeoverflow` stays 422 and 96 bytes.
`TestRowsClosedBeforeRender` records `ReaderInUse() == 0` at the start of
render.

## Acceptance mapping

| Done-when item | Proof |
|---|---|
| Workspace, source, time, severity, <=8 literal terms, <=8 typed JSON equality filters; bound values; quoted FTS terms; no exposed SQL/FTS operators | `TestValidateQueryBounds`, `TestPredicatePaths` (dot, quote, index, bracket, nine segments, missing-with-value, bool-as-int, real-word), `TestTypedJSONFilters` (null, missing, boolean, integer, real, string, nested path), `FuzzLiteralTerm` seeds 0–10, `FuzzPredicatePath` seeds 0–6, `TestWorkspaceIsolation` |
| Planned B-tree and external-content FTS5 indexes; transactional triggers; JSON null/missing/bool/numeric distinctions; valid bound paths | `TestIndexesAndExplain`, `TestFTSDeleteTrigger` (delete trigger then `integrity-check`), `TestTypedJSONFilters`, `TestBaselineSearch` EXPLAIN lines above |
| 4KiB input, default 15m / max 7d, <=200 rows / 2MiB, 3s interrupted DB query / 5s HTTP, four readers, 100000-step budget; broad queries report the budget error | `TestValidateQueryBounds` (exact 7d ok, 7d+1µs refused, 9 terms, 129-byte term, cursor over 4KiB), `TestResultBytesBound`, `TestResultHTTPBytes` (`below`, `rawoverflow`, `escapeoverflow`), `TestReaderPoolAndInterrupt`, `TestBaselineSearch` pathological case, `TestHTTPLimitSeparateFromWriterQueue` (`httpTimeout` is 5s) |
| Signed 15m cursor binds scope, filter, highest committed id, frozen receipt cutoff, and last `(event_time, ingest_id)`; keyset pagination; no OFFSET or long-lived snapshot; retention minute and restore epoch invalidate the cursor | `TestSearchPageKeysetAndCursor` (equal time, limit 2, SQL contains no `OFFSET`, reopen keeps the MAC, suffix and MAC tamper, cross-workspace, 15m expiry, epoch, next-minute retention), `TestHorizonExcludesLaterRows`, `TestInvalidCursorRejected` |
| 50000-event baseline, one historical reader, one Live reader, 5/s ingest, representative p95 <1s; EXPLAIN shows the indexes; pathological scan aborts <=5s; rows close before render | `TestBaselineSearch` (p95 2.408434ms, ingest 15, live_rows 50015, pathological 100000 steps in 5.671563ms), `TestIndexesAndExplain`, `TestRowsClosedBeforeRender` |

## Judgment calls

- N2 stays two layers. The HTTP cap of 64 and the writer queue of 64 jobs /
  16MiB are separate. Ingest takes an HTTP slot and then a writer-queue slot.
  A full HTTP limit returns 503 before writer admission. Search does not enter
  the writer queue. The ingest route is not exempt. `TestHTTPLimitSeparateFromWriterQueue`
  fills only the HTTP semaphore and checks the watermark stays 0.
- N4 parses `Content-Type` with `mime.ParseMediaType` on the native and
  skeleton JSON routes, so `application/json; charset=utf-8` is accepted.
  Form and login content types are unchanged.
- N1: a database with no `receipts` table returns `ErrOlderSlice` and the
  process logs the sentence above. A Slice 04 `user_version` 1 file still
  opens. Missing search indexes and the cursor metadata keys are created on
  open. There is no in-place upgrade from a pre-receipts file, and no v1-to-v2
  migration. `TestFutureSchema` still refuses `user_version` 2.
- The receipt cutoff is truncated to the UTC minute. A page turn inside that
  minute keeps the frozen snapshot. The next minute invalidates the cursor
  even when the 15m TTL remains. Detail uses the same cutoff.
- The VM budget is 100000 steps, checked every 1000 opcodes, so the abort
  lands on 100000. It is not raised. The progress handler is a small cgo call
  on the bundled SQLite, aimed at the driver connection with reflection. The
  3s context timeout is additional. Writers stay disarmed.
- Row-byte accounting adds 1024 bytes per row so a storage page still renders
  under the existing 2MiB HTML cap. Extra rows produce a cursor. One row that
  cannot fit is still `ErrTooBroad`. The old "more rows than Limit is an
  error" behavior is gone.
- Typed predicates and eight terms are on the storage and model API. The
  skeleton HTML form is still one term and has no predicate inputs. A next-page
  form posts the signed cursor with the current filters. `ReadSince` is a
  bounded `ingest_id` catch-up used by the baseline live reader. It is not the
  htmx Live UI.
- The 50000-event measurement is a 3s window with a 200ms ingest ticker, one
  historical reader, and one `ReadSince` reader. It is not the plan's
  30-minute profile or 50/s burst. The seed is one maintenance transaction
  with direct inserts so the FTS triggers and the watermark still run.
  `src_demo_cron` and `src_demo_batch` exist only in that fixture.
- FTS hits are ordered with a temporary B-tree. Source/time queries use
  `idx_logs_time`. Both plans are the ones recorded above.
- The cursor MAC and epoch live in `metadata`. Restart does not rotate the
  epoch. `RotateCursorEpoch` is the restore invalidation hook. This slice does
  not implement restore.
- `normalizeQuery` truncates to microseconds only when strict validation
  fails, so `time.Now()` callers keep working. A query that already validates
  is left unchanged, so the cursor text matches the form.

## Not run

- The plan's 30-minute 5/s profile and the 50/s burst.
- Power loss and OS `SIGKILL` during a search.
- htmx Live. `ReadSince` is only the baseline catch-up reader.
- Slice 06 investigation UI: multi-term form controls and predicate inputs.
- An interactive browser click of Next page. The rendered control is
  `TestResultHTTPBytes/rawoverflow` (`id="btn-next"`, body 2084544 bytes).
- A full restore. Only `RotateCursorEpoch` is implemented.
- Count-all, an arbitrary query language, dynamic user indexes, and unbounded
  export.
- Automatic checkpoint and the retention sweeper.
- Local gitleaks. The CI `test` job runs gitleaks v8.30.1.
