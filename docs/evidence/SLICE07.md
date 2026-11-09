# Slice 07 evidence: bounded htmx Live polling

Synthetic fixtures only. The pages below were rendered by the investigation
handlers and captured in headless Chrome. They are not production data.
`user_version` stays 1. No new index was added.

## Test profile

| Item | Value |
|---|---|
| Target | linux/amd64, CGO_ENABLED=1, build tags `fts5 sqlite_omit_load_extension` |
| Toolchain | go1.27.3 (GOTOOLCHAIN=local), gcc 13.3.0, node v22.23.4, Chrome 156.0.8078.47 |
| Hardware class | generic x86_64 virtual machine |
| SQLite | 3.54.1, bundled with `github.com/mattn/go-sqlite3` v1.14.53, unchanged from Slice 05 |

## How to run

```bash
go mod verify
test -z "$(gofmt -l .)"
CGO_ENABLED=1 go test -race -count=1 -tags "fts5 sqlite_omit_load_extension" -timeout 15m ./...
CGO_ENABLED=1 go vet -tags "fts5 sqlite_omit_load_extension" ./...
node --test internal/server/web/app.test.js
CGO_ENABLED=1 go test -count=50 -tags "fts5 sqlite_omit_load_extension" -run TestBrowserE2E -timeout 1800s -v ./internal/server/
```

Screenshots: `E2E_SHOT_DIR=docs/evidence/slice07` with the same `TestBrowserE2E`
command, count 1. The count=50 run does not set `E2E_SHOT_DIR`.

## Gates

`go mod verify` reported all modules verified. `gofmt -l` was empty. `go vet`
was clean. Node: 9 pass, 0 fail. The new node test is
`live polling backs off, pauses, dedups and prunes`. The earlier 8 tests are unchanged.

Race package results (0 failures):

```text
ok  example.invalid/hearthlog/cmd/hearthlog             1.012s
ok  example.invalid/hearthlog/examples/slogservice      1.027s
ok  example.invalid/hearthlog/internal/model            1.045s
ok  example.invalid/hearthlog/internal/server          16.501s
ok  example.invalid/hearthlog/internal/storage/sqlite  18.612s
ok  example.invalid/hearthlog/internal/emitter         21.904s
```

Same command with `-json`: 138 top-level tests, 344 including subtests,
0 failures (cmd 1, examples 4, emitter 38/108, model 11/76, server 42/85,
sqlite 42/70). Node: 9 pass, 0 fail. The server package includes one
`TestBrowserE2E`. `TestLivePollConcurrentNoLoss` is in the sqlite package,
so it passed under `-race`.

`TestBrowserE2E -count=50` passed 50/50 in 247.187s. Each run includes the
live step (`live-scroll paused`, pause holds a new row, resume shows it) and
the existing no-JS and session-expired steps. The harness waits until no
element has `htmx-request`, `htmx-swapping`, or `htmx-settling` before each
click, and `waitFn` dumps page and last-request diagnostics on timeout.
The full log is `hearthlog/slice07-e2e-count50.log` in the agent store and
`/opt/cursor/artifacts/slice07-e2e-count50.log`. CI runs the browser test
once, with a 180s timeout. One local run is about 5s.

gitleaks v8.30.1 is not on PATH here. The leak scan is the CI step inside
the `test` job. The pull request records that run id for the head SHA.

## What changed

Live is a tail of events committed after Start, not a replay of history.
`POST /investigate/live` is a separate handler from search. The initial page
has no `hx-trigger`. After a successful start, `app.js` mounts one hidden
trigger, `hx-trigger="every 1000ms"`, `hx-sync="#live-trigger:drop"`, and
drops a second request while one is in flight. Idle responses double the
interval from 1s to 2s, 4s, and 8s, and a non-empty page returns it to 1s.
Pause, a hidden tab, a scrolled result list, a gap, and an error remove the
trigger. A hidden tab or a return to the top of the list sends one `resume`
and then polls again. Pause stays paused until Resume. Reconnect stays
disabled until `Retry-After` and does not click itself.

The poll cursor is `(received_at, ingest_id)`. Each poll reads the watermark
on the reader connection and ignores higher ingest ids. Rows are ordered by
that pair, so equal receipt times are not skipped or repeated. Filters run
in Go after the scan, and the cursor moves to the last examined row, including
rows the filter skips. A tripped 100000-step budget is a gap and does not
move the cursor. The connection is returned before HTML is rendered.

## Done when

| Acceptance | Proof |
|---|---|
| 1s htmx polls, <=1 outstanding request/tab and <=2 Live clients/workspace; read only committed ingest IDs from the persisted single-writer watermark, including after rollback/restart | `app.js` mounts `every 1000ms` and keeps one `liveXhr`. Node test checks `hx-sync="#live-trigger:drop"`. `TestLivePollHTTP` allows two client ids and returns 429 `Live: Throttled` with `Retry-After: 5` for the third; a 16s clock step frees the slot. `TestLivePollRollbackRestartAndEmpty` aborts a watermark update, holds an uncommitted insert, reopens the file, and deletes every row. The rejected and uncommitted messages are absent, and the watermark is unchanged. |
| Fetch <=200 rows/poll and advance by the highest examined authorized scan position even with sparse filters; bounded scan exhaustion yields an explicit gap, never silent loss | `liveMatchLimit` is 200. `TestLivePollSparseFilter` skips 5000 non-matches in pages and then returns the one ERROR row. `TestLivePollBudgetGap` sets the step cap to 1000 and expects a gap with the cursor unchanged. `TestLivePollEqualReceivedAt` and `TestLivePollPagesTiedRows` page 250 and 201 rows that share one `received_at`. |
| Render escaped row partials; minimal external listener updates cursor/status, dedups and prunes the DOM to 1,000 rows before the next poll. No DB transaction is retained while the client reads HTML | `web/live.html` is a normal template. `TestLivePollHTTP` requires `&lt;script&gt;` and `&lt;div hx-get=` and rejects a raw `<script` tag, and `beforeRender` sees `ReaderInUse()==0`. The node test merges two equal ingest ids into one node and prunes 1005 rows to 1000, keeping the newest. |
| Pause freezes rows and cancels polling; a hidden or closed tab stops; reconnect checks auth and <=5,000 events/30s catch-up, otherwise a gap plus a historical-search action | `TestBrowserE2E` scrolls the live list (`live-scroll paused`), pauses, inserts `synthetic live while paused`, and does not show it until Resume. The node test removes the trigger on `visibilitychange` hidden and remounts it on visible, then the next batch is `intent=poll`. `pagehide` calls `clearLive`. `TestLivePollCatchupGap` and `TestLivePollHTTP` return a gap for a 31s jump on resume and for 5001 rows. The gap response is HTTP 410 with `X-Live-Gap-From` and `X-Live-Gap-To`. Search gap fills the historical from/to fields. A second login makes the old poll 401 and the existing session copy. |
| At baseline, p95 committed-event visibility <2s; delayed transactions, an empty store after retention, and crashes do not reset the watermark or skip a supported catch-up. Busy, denied, and error states stop or back off visibly | `TestLivePollVisibility`: `visibility_n=20 p95=1.178143ms max=1.178143ms`. The rollback test covers the delayed transaction, the delete, and reopen. The node test doubles an idle poll to `every 2000ms`. `TestLivePollHTTP` covers 429 and 401. `app.js` maps 504 and other 5xx to `Live: Disconnected`, 400/422 to `Live: Query invalid`, and 503 to `Live: Throttled`. |

## Cursor, scope, and the reader budget

Every live statement contains `workspace_id = ?` and `INDEXED BY idx_logs_retention`.
`TestLiveQueryIsWorkspaceScoped` rejects `OFFSET` and `idx_logs_ws_ingest`.
`TestLivePollTailAfterHistory` loads 5000 rows, starts at the tail, and plans:

```text
SEARCH logs USING INDEX idx_logs_retention (received_at=? AND ingest_id>? AND ingest_id<?)
SEARCH logs USING INDEX idx_logs_retention (received_at>?)
```

The two arms are the receipt-time tie and the strictly newer receipts. Each
arm stops at the page limit, so a tail poll does not read the older rows.
A workspace filter that was planned as `idx_logs_ws_ingest` walked the whole
workspace and tripped the 100000-step budget on the investigation fixture.

`TestLivePollConcurrentNoLoss` freezes the clock so every row shares
`received_at`. Four workers commit 640 events while a poller advances the
cursor. The union of polled ids equals the inserted set, each id once. One
row committed in the other workspace is absent. The same package passed
`go test -race`.

## No JavaScript

The page includes the sentence "Live requires JavaScript. Use Refresh for
historical search." and a Refresh button that posts the historical search
form. The initial HTML has no poll trigger. `TestLiveNoJSRefresh` checks the
sentence, the button, a full-page refresh that returns `synthetic refresh row`,
and a non-htmx start that does not contain `hx-trigger`. `TestBrowserE2E`
step `no-js` still posts the search form with JavaScript disabled.

## Screenshots

Headless Chrome loaded the handler HTML with `web/style.css`. Demo names only
(`demo-api`, `synthetic live arrived`, `synthetic live while paused`).
No sideways scroll: at the live page, `scrollWidth` equalled `clientWidth`
at 1280 (1265 with the scrollbar gutter), 390, and 360.
`docs/evidence/slice07/scroll.json` is that measurement.

| View | Desktop | 390px |
|---|---|---|
| Receiving | [live-receiving-desktop.png](slice07/live-receiving-desktop.png) | [live-receiving-390.png](slice07/live-receiving-390.png) |
| Paused | [live-paused-desktop.png](slice07/live-paused-desktop.png) | [live-paused-390.png](slice07/live-paused-390.png) |
| Gap | [live-gap-desktop.png](slice07/live-gap-desktop.png) | [live-gap-390.png](slice07/live-gap-390.png) |
| Query invalid | [live-error-desktop.png](slice07/live-error-desktop.png) | [live-error-390.png](slice07/live-error-390.png) |

## Judgment calls

- Steady polls do not apply the 5000-event or 30s check. Resume, Reconnect,
  a tab becoming visible, and a scroll back to the top do. After that one
  response the trigger is remounted as `intent=poll`, so a long-lived tail
  does not gap only because the newest receipt is more than 30s past a
  sparse cursor.
- A filtered scan examines 200 rows, the same cap as an unfiltered page.
  A full-column scan of several thousand rows exceeds the 100000-step reader
  budget, so that larger cap would gap instead of advancing. The catch-up
  count still stops at 5001 rows, using the receipt index rather than the
  full row. The budget is not raised.
- Term matching on the live path is a case-insensitive substring. It is not
  the FTS query used by historical search. Severity, source, and predicates
  use the same values the form already validates.
- 401 and 403 keep the Slice 06 sentence "Session expired. Sign in" and clear
  the live rows. The design table said "Access denied". The shared denial
  path is what the existing browser test checks.
- A live client holds one of the two workspace slots for 15s after its last
  request, which is longer than the 8s idle backoff. A paused tab keeps its
  slot until that TTL. The clock is the store clock.
- Start uses `LiveTail`: the cursor is the newest retained `received_at` in
  the workspace and the current watermark. History is not replayed. A later
  row with that same receipt time and a higher ingest id is still returned.
- The DOM cap of 1000 nodes, the 200-row page, and `ReaderInUse()==0` at
  render are the memory evidence. This slice did not run an RSS soak.
- `TestLivePollVisibility` is 20 commit-then-poll samples on an empty store.
  It is not the plan's 30-minute 5/s profile.

## Not run

- The 30-minute baseline profile and the 50/s burst.
- An RSS or heap soak beyond the 1000-node DOM cap.
- gitleaks locally. CI runs it.
- A real OS tab switch. The hidden-tab check drives `visibilitychange` in
  the node test. The browser test hides the trigger by scrolling the live
  list and by Pause.
- PostgreSQL, a new schema version, and the Slice 09
  `(workspace_id, received_at, ingest_id)` index.
