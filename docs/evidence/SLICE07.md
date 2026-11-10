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
CGO_ENABLED=1 go test -count=100 -tags "fts5 sqlite_omit_load_extension" -run TestBrowserE2E -timeout 3600s -v ./internal/server/
```

Screenshots: `E2E_SHOT_DIR=docs/evidence/slice07` with the same `TestBrowserE2E`
command, count 1. The count=100 run does not set `E2E_SHOT_DIR`.

## Gates

`go mod verify` reported all modules verified. `gofmt -l` was empty. `go vet`
was clean. Node on this follow-up: 10 pass, 0 fail. The live test is
`live polling backs off, pauses, dedups and prunes`, and
`a short page polls again and a finished request does not stick`.

Race package results (0 failures):

```text
ok  example.invalid/hearthlog/cmd/hearthlog             1.012s
ok  example.invalid/hearthlog/examples/slogservice      1.027s
ok  example.invalid/hearthlog/internal/model            1.045s
ok  example.invalid/hearthlog/internal/server          16.501s
ok  example.invalid/hearthlog/internal/storage/sqlite  18.612s
ok  example.invalid/hearthlog/internal/emitter         21.904s
```

`TestBrowserE2E` and `TestLiveBrowserP95` run inside `go test -race`.
Package server has no skip and no build tag that drops them under the race
detector. CI's race step runs them too. `TestLivePollConcurrentNoLoss` is
in the sqlite package and also passes under `-race`.

`TestBrowserE2E -count=100` passed 100/100 in 487.526s, with 0 failures.
Each run includes the live step (`live-scroll paused`, pause holds a new
row, resume shows it) and the existing no-JS and session-expired steps.
The harness waits until no element has `htmx-request`, `htmx-swapping`, or
`htmx-settling` before each click, including before the aging-row clock
step. A DevTools command times out after 20s, and a step that is quiet for
20s logs a goroutine dump. The full log is
`hearthlog/slice07-e2e-count100.log` in the agent store and
`/opt/cursor/artifacts/slice07-e2e-count100.log`. CI runs the browser test
once, with a 180s timeout. One local run is about 5s. Node tests are 10
pass, 0 fail, including the short-page and stuck-request cases.

gitleaks v8.30.1 is not on PATH here. The leak scan is the CI step inside
the `test` job. The pull request records that run id for the head SHA.

## What changed

Live is a tail of events committed after Start, not a replay of history.
`POST /investigate/live` is a separate handler from search. The initial page
has no `hx-trigger`. After a successful start, `app.js` mounts one hidden
trigger, `hx-trigger="every 1000ms, live-now"`, `hx-sync="#live-trigger:drop"`,
and drops a second request while one is in flight. Idle responses double the
interval from 1s to 2s and stop there. A non-empty page returns it to 1s.
A page that is not caught up fires `live-now` immediately instead of waiting
out the interval. Pause, a hidden tab, a scrolled result list, a gap, and an
error remove the trigger. A hidden tab or a return to the top of the list
sends `intent=resume` and keeps sending it. Pause stays paused until Resume.
Reconnect stays disabled until `Retry-After` and does not click itself.
`pagehide` posts `intent=stop` before it clears the page, so a reload does
not leave the slot held.

The poll cursor is `(received_at, ingest_id)`. `received_at` is the later of
the wall clock and the last persisted receipt (`receipt_hi`, and
`MAX(received_at)`), written in the commit transaction, so a backward clock
step cannot hide a row from the keyset. A forward spike pins `receipt_hi`.
Later rows keep that receipt until the wall clock catches up, and retention
waits with them. A clamp of more than one second logs
`receipt_hi ahead of wall clock`. Each poll reads the watermark on the
reader connection and ignores higher ingest ids. Rows are ordered by that
pair, so equal receipt times are not skipped or repeated. Filters run in Go
after the scan, and the cursor moves to the last examined row, including
rows the filter skips. A tripped 100000-step budget is a gap and does not
move the cursor. The connection is returned before HTML is rendered.

Every poll, not only resume, returns 410 when more than 5000 rows are still
ahead or the newest receipt is more than 30s after `max(cursor, as_of)`.
`as_of` is the server time of the previous poll. The keyset cursor is not
moved to `as_of`, so quiet time before that poll is not a gap.

## Done when

| Acceptance | Proof |
|---|---|
| 1s htmx polls, <=1 outstanding request/tab and <=2 Live clients/workspace; read only committed ingest IDs from the persisted single-writer watermark, including after rollback/restart | `app.js` mounts `every 1000ms, live-now` and keeps one `liveXhr`. Node test checks `hx-sync="#live-trigger:drop"`. The server allows one in-flight request per slot and a 200ms minimum interval, and returns 429 `Live: Throttled` with `Retry-After: 5` otherwise. `TestLiveMinIntervalAndInFlight` accepts the follow-up while a page is behind, rejects the next poll inside the interval, and accepts 1 of 16 overlapping polls. `TestLivePollHTTP` returns 429 for a third client id; a 16s clock step frees the slot. `TestLiveReloadDoesNotLockOut` starts three different ids and the same id three times, all 200, and `intent=stop` releases the slot. `TestLiveReplacedSessionStarts` fills both slots, replaces the session, and starts again with the same id and a fresh id, both 200. `TestLivePollRollbackRestartAndEmpty` aborts a watermark update, holds an uncommitted insert, reopens the file, and deletes every row. The rejected and uncommitted messages are absent, and the watermark is unchanged. |
| Fetch <=200 rows/poll and advance by the highest examined authorized scan position even with sparse filters; bounded scan exhaustion yields an explicit gap, never silent loss | `liveMatchLimit` is 200. `TestLivePollSparseFilter` skips 400 non-matches in pages and then returns the one ERROR row. `TestLivePollNotCaughtUp` returns 200 rows with `CaughtUp` false, then the last row. `TestLivePollBudgetGap` sets the step cap to 1000 and expects a gap with the cursor unchanged. `TestLivePollClockStepBack` steps the clock back by 1us, 1s, and 10s, reopens the file, and still returns every row committed after the step. `TestLivePollEqualReceivedAt` and `TestLivePollPagesTiedRows` page 250 and 201 rows that share one `received_at`. |
| Render escaped row partials; minimal external listener updates cursor/status, dedups and prunes the DOM to 1,000 rows before the next poll. No DB transaction is retained while the client reads HTML | `web/live.html` is a normal template. `TestLivePollHTTP` requires `&lt;script&gt;` and `&lt;div hx-get=` and rejects a raw `<script` tag, and `beforeRender` sees `ReaderInUse()==0`. The node test merges two equal ingest ids into one node and prunes 1005 rows to 1000, keeping the newest. |
| Pause freezes rows and cancels polling; a hidden or closed tab stops; reconnect checks auth and <=5,000 events/30s catch-up, otherwise a gap plus a historical-search action | `TestBrowserE2E` scrolls the live list (`live-scroll paused`), pauses, inserts `synthetic live while paused`, and does not show it until Resume. The node test removes the trigger on `visibilitychange` hidden and remounts it on visible. The request after the tab is visible again is `intent=resume`, which is what the browser sends. `pagehide` posts `intent=stop` and then calls `clearLive`. `TestLivePollQuietAsOf` is 35s with no new rows, then 2s and one new row: the row is returned and the cursor is not `as_of`. `TestLivePollCatchupGap` gaps a steady poll when the newest row is 31s after `as_of`, and gaps 5001 rows. `TestLivePollHTTP` returns HTTP 410 with `X-Live-Gap-From` and `X-Live-Gap-To`. Search gap fills the historical from/to fields. A second login makes the old poll 401 and the existing session copy. |
| At baseline, p95 committed-event visibility <2s; delayed transactions, an empty store after retention, and crashes do not reset the watermark or skip a supported catch-up. Busy, denied, and error states stop or back off visibly | `TestLiveBrowserP95` inserts one row after the idle interval has reached 2s and measures until the row is in the live list. 20 browser samples, p95 1.972s (samples 632, 794, 798, 837, 882, 958, 960, 1037, 1161, 1294, 1425, 1625, 1635, 1698, 1788, 1832, 1839, 1867, 1972, 2002 ms). `TestLivePollVisibility` remains the store-level loop (`visibility_n=20 p95=1.178143ms`). The rollback test covers the delayed transaction, the delete, and reopen. The node test doubles an idle poll to `every 2000ms` and leaves a second idle poll there. `TestLivePollHTTP` covers 429 and 401. `app.js` maps 504 and other 5xx to `Live: Disconnected`, 400/422 to `Live: Query invalid`, and 503 to `Live: Throttled`. |

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

- Every poll applies the 5000-row and 30s checks. The window is the newest
  receipt minus the later of the cursor and the previous poll's `as_of`.
  A page that is not caught up is requested again immediately, still one
  request at a time. The trigger is not removed while its own response is
  being applied, so a resume request cannot leave `liveXhr` set and drop
  the next poll. The browser keeps sending `intent=resume` after the tab
  is visible again.
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
- A live slot is bound to the session that started it. One request may be
  in flight, and a caught-up poll waits at least 200ms. The slot TTL is 15s
  on the store clock, which is longer than the 2s idle cap. `pagehide` posts
  `intent=stop`, and a new start from the same session replaces that
  session's oldest slot, so three reloads are not locked out for the TTL.
  A replaced login drops every slot from the previous session before the
  cap check, so the same id and a fresh id both start. `TestLiveReplacedSessionStarts`
  covers that. `livePace` and `liveGate` are unset in a normal process.
  They exist so tests can move the interval clock and hold one request.
  A duplicated tab copies sessionStorage, so both tabs share one client id
  and the second tab is throttled.
  The Search gap link is `#f4f7ff` on `#1a1a2e` (contrast 15.9:1), takes
  its own row in the toolbar so it does not wrap into the buttons at 360
  or 390, and `#btn-search-gap[hidden]{display:none}` keeps it off the page
  until the gap state. Open details posts `#detail-form` once
  (`hx-sync="this:drop"`), so that request is not the shared investigation
  slot.
- Start uses `LiveTail`: the cursor is the newest retained `received_at` in
  the workspace and the current watermark. History is not replayed. A later
  row with that same receipt time and a higher ingest id is still returned.
- The DOM cap of 1000 nodes, the 200-row page, and `ReaderInUse()==0` at
  render are the memory evidence. This slice did not run an RSS soak.
- `TestLivePollVisibility` is 20 commit-then-poll samples on an empty store.
  It is not the plan's 30-minute 5/s profile. The browser number above is
  the first row after a quiet spell, measured in Chrome.

## Not run

- The 30-minute baseline profile and the 50/s burst.
- An RSS or heap soak beyond the 1000-node DOM cap.
- gitleaks locally. CI runs it.
- A real OS tab switch. The hidden-tab check drives `visibilitychange` in
  the node test. The browser test hides the trigger by scrolling the live
  list and by Pause.
- PostgreSQL, a new schema version, and the Slice 09
  `(workspace_id, received_at, ingest_id)` index.
