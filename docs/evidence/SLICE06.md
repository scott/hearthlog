# Slice 06 evidence: server-rendered investigation UI

Synthetic fixtures only. The pages below were rendered by the investigation
handlers and captured in headless Chrome. They are not production data.
Live polling is not this slice. `user_version` stays 1.

## Test profile

| Item | Value |
|---|---|
| Target | linux/amd64, CGO_ENABLED=1, build tags `fts5 sqlite_omit_load_extension` |
| Toolchain | go1.27.3 (GOTOOLCHAIN=local), gcc 13.3.0, node v22.23.4, Chrome 156.0.8078.47 |
| Hardware class | generic x86_64 virtual machine |
| SQLite | 3.54.1, unchanged from the Slice 05 readback |

## Gates

```bash
go mod verify
test -z "$(gofmt -l .)"
CGO_ENABLED=1 go test -race -count=1 -tags "fts5 sqlite_omit_load_extension" -timeout 15m ./...
CGO_ENABLED=1 go vet -tags "fts5 sqlite_omit_load_extension" ./...
node --test internal/server/web/app.test.js
```

Node: 8 pass, 0 fail. Added `status notices hit the live node`,
`submit disables the submitter after the turn`, and
`notice focus survives a late results settle`. The earlier node tests are unchanged.
`go mod verify` reported all modules verified. `gofmt -l` was empty. `go vet`
was clean.

Race package results (0 failures):

```text
ok  example.invalid/hearthlog/cmd/hearthlog             1.013s
ok  example.invalid/hearthlog/examples/slogservice      1.030s
ok  example.invalid/hearthlog/internal/model            1.045s
ok  example.invalid/hearthlog/internal/server          12.625s
ok  example.invalid/hearthlog/internal/storage/sqlite  14.525s
ok  example.invalid/hearthlog/internal/emitter         21.436s
```

Same command with `-json`: 126 top-level tests, 332 including subtests,
0 failures (cmd 1, examples 4, emitter 38/108, model 11/76, server 39/82,
sqlite 33/61). Node: 8 pass, 0 fail. The server package includes
`TestBrowserE2E`.

gitleaks v8.30.1 is not on PATH here. The leak scan is the CI step inside
the `test` job. The pull request records that run id for the head SHA.

## Done when

| Acceptance | Proof |
|---|---|
| Semantic full pages plus htmx filter, result, and detail partials for source, time, severity, message terms, typed attribute chips, and saved views | `internal/server/web/page.html`, `results.html`, `detail.html`, `context.html`. `TestFragments` checks one results region and one detail region, fragment headers, and frozen from/to. `TestAddTermControl`, `TestTermsPredicatesAndHostileDetail`, `TestSavedViewReevaluatesRelativeWindow`. |
| Search, detail, and pagination work as normal form posts. Stale htmx responses are generation-rejected. Errors keep inputs. Focus survives a detail close | `TestFormSessionGuards`, `TestSearchFullPage`, `TestFormCapKeepsCursorAndFiltersApart` (Older posts the cursor only). `app.test.js` `non-auth focus: current/stale/unknown` and the generation/sequence checks in `app.js configRequest and beforeRequest`. `TestBrowserE2E` steps `back-focus` and `no-js`. |
| Event data is escaped. No trusted HTML/JS/URL cast. Fixed targets. Pinned self-hosted htmx. `allowEval=false`, `allowScriptTags=false`, `selfRequestsOnly=true`. Strict CSP | `TestTermsPredicatesAndHostileDetail` (hostile `hx-get` markup is escaped). `app.test.js` `htmx pin` SHA-256 `d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717`. CSP and htmx-config meta are in `routes.go` and `page.html`. `TestAccessibilityContract` rejects inline handlers. |
| History snapshots and HTML caching stay off. A full navigation is a full page. `Vary: HX-Request` and `no-store`. Raw queries are not access-log fields | `page.html` sets `hx-history=false`, `historyCacheSize=0`, `historyRestoreAsHxRequest=false`. `ServeHTTP` sets `Vary` and `no-store`. `cmd/hearthlog/main.go` does not log request URIs. Non-HX search renders the `page` template (`TestSearchFullPage`). |
| UX states, labels, text severity, narrow layout, session CSRF, and origin checks | `TestUIGuidanceStates`, `TestQueryTooBroadCopy`, `TestRowExpiredVersusMissing`, `TestAccessibilityContract`, `TestNarrowLayoutCSSContract`, `TestFormSessionGuards`. Scroll check below. |

## Carry-overs from the Slice 05 review

| Item | Proof |
|---|---|
| Predicate and multi-term inputs. A page turn posts the signed cursor without repeating the filters, and that body stays within the 4KiB form cap. A 4097-byte body is rejected | `TestTermsPredicatesAndHostileDetail`. `TestFormCapKeepsCursorAndFiltersApart` finds a filter set whose cursor-only form is at most 4096 bytes and whose combined cursor-plus-filters form is over 4096, then posts the cursor-only form and expects the next row. `ValidateQuery` caps the cursor and the filter JSON separately so a page token is not rejected for repeating filters it already binds. |
| A detail lookup uses the live retention cutoff. A row that is still on a frozen page but past that cutoff shows "Row expired", not "Event not found" | `TestRowExpiredVersusMissing`. Missing and cross-workspace ids stay "Event not found" and do not include the row message. |
| 422 `query_too_broad`, 410 `cursor_expired`, and 504 timeout each tell the user what to do next | `TestQueryTooBroadCopy` (10,000 synthetic rows, HTTP 422, "Narrow the time window", no "No matching events"). `TestUIGuidanceStates` (410 keeps the frozen window and drops the cursor token; 504 does not claim the source list is empty). Node test `422, 410, and 504 tell the user what to do` checks the same sentences, does not insert the response body, and clears cursor fields only on 410. |
| One shared name for the retention index | `IndexLogsRetention` in `internal/storage/sqlite/migrate.go`, used by schema creation, the cursor retention probe, and `HasRetainedLogs`. |

## Screenshots

Headless Chrome loaded the handler HTML with `web/style.css`. Demo names only
(`demo-api`, `synthetic checkout accepted`, and the other rows in the shots).
No sideways scroll: `scrollWidth` and `body.scrollWidth` were equal to
`clientWidth` at 1280, 390, and 360. `docs/evidence/slice06/scroll.json` is
that measurement. Desktop shots are the top 1400px. 390px shots are the
page, capped at 2400px where the detail page is taller.

| View | Desktop | 390px |
|---|---|---|
| Ready | [ready-desktop.png](slice06/ready-desktop.png) | [ready-390.png](slice06/ready-390.png) |
| Results | [results-desktop.png](slice06/results-desktop.png) | [results-390.png](slice06/results-390.png) |
| Detail, typed attributes | [detail-desktop.png](slice06/detail-desktop.png) | [detail-390.png](slice06/detail-390.png) |
| Query too broad | [too-broad-desktop.png](slice06/too-broad-desktop.png) | [too-broad-390.png](slice06/too-broad-390.png) |
| Results expired | [cursor-expired-desktop.png](slice06/cursor-expired-desktop.png) | [cursor-expired-390.png](slice06/cursor-expired-390.png) |
| Query timed out | [timeout-desktop.png](slice06/timeout-desktop.png) | [timeout-390.png](slice06/timeout-390.png) |
| Row expired | [row-expired-desktop.png](slice06/row-expired-desktop.png) | [row-expired-390.png](slice06/row-expired-390.png) |
| Too broad after a successful search | [notice-after-search-desktop.png](slice06/notice-after-search-desktop.png) | [notice-after-search-390.png](slice06/notice-after-search-390.png) |

## Judgment calls

- Pagination and detail-back posts send the signed cursor and recover filters with `FiltersFromCursor`. There is no server-side last-query cache. The cursor still embeds the filters, so storage tests that compare the cursor body to the query stay valid.
- A relative preset (5m, 15m, 1h, 24h) is recomputed only when the request has no cursor. Detail, back, and filter-on-value send `preset=custom` and the frozen from/to. The exclusive end of a fresh relative window is one second past now so an event stamped in the current second is inside the window.
- Saved views store the preset and the filters, not a frozen absolute window. A custom window cannot be saved. Opening a view recomputes the window from the store clock.
- The "Register source" link points at `/investigate`. Source registration is Slice 08.
- Session denial copy stays "Session expired. " plus "Sign in". Investigation 503 is the HTML busy page. API 503 stays JSON. A result that exceeds the render budget stays JSON `query_too_broad`.
- Detail of a row past the live cutoff is HTTP 404 with `data-code="row_expired"`. Cursor expiry stays HTTP 410. The 410 page puts the cursor's filters back into the form and clears the cursor.
- `IndexLogsRetention` is the only name used for `idx_logs_retention` in code. Slice 05 evidence still quotes the literal, because that file records the earlier run.

## Follow-up for the client hold

| Item | Proof |
|---|---|
| B1. Notices write to the `#status` that is in the document after a results or detail swap. 400 and 404 use the server copy for `invalid_query`, `unsupported_path`, `row_expired`, and `not_found`. The response body is not inserted | `app.js` looks up `#status` when it writes. `fail` sets `X-Notice`. Node test `status notices hit the live node` replaces the node, then checks 422, 410, 504, 503, 400, 404 `row_expired`, 404 `not_found`, a stale 400, a network error, 401, and the prototype names `constructor` and `toString`. `TestClientContracts` checks the `X-Notice` header for `invalid_query` and `row_expired`. `TestBrowserE2E` steps `too-broad`, `row-expired`, `cursor-expired`, and `session-expired` run after a successful search |
| B2. The clicked submit button keeps its `intent` | The submit listener uses `e.submitter` and disables it on the next turn. Node test `submit disables the submitter after the turn`. Browser steps `remove-term` (alpha/beta becomes beta) and `save-open` (Open morning returns the checkout row, not "Query is invalid") |
| B3. Enter in the filters runs Search | `#btn-search-implicit` is the first submit button, `tabindex="-1"`, `intent=search`, same `hx-post` target as the visible Search. `TestClientContracts` checks that order. Browser step `enter-search` keeps the term and stays on the same document. Step `no-js` is a full page and still keeps the term |
| B4. Back to results focuses the Open details button that opened the detail | The results heading is autofocused only when `OriginID` is empty. The matching `event-N-open` button is autofocused when the origin is on the page. `TestClientContracts` checks both HTML shapes. Browser steps `back-focus` and `no-js` check `document.activeElement` |
| B5. Headless browser walk | `TestBrowserE2E` drives Chrome over the real server. CI installs Chrome, pins Node 22, and runs `go test -run TestBrowserE2E -v` before the race suite. A missing Chrome binary fails the test |

Button text is white on `#1565c0` (5.75:1). Hover is white on `#0d47a1` (8.63:1). Disabled is `#1a1a2e` on `#d7deea` (12.61:1), with no opacity. Links stay `--accent` `#4a9eff` on `#1a1a2e` (6.19:1). `TestClientContracts` locks those declarations. The htmx config sets `attributesToSettle` to `["class"]`. The browser walk fails if Chrome logs a CSP refusal.

The new shots are the too-broad notice after a successful search, with the earlier hostile row still on the page at 390px. Names are `demo-api`, `commonwide`, and `synthetic hostile`. No real host, token, or log text.

A results swap schedules htmx settle for 20ms. Settle focuses the first `[autofocus]` in the new fragment, which is `#results-heading` when the search has no origin. If the next submit returns a notice inside that window, the notice focuses `#status` and the pending settle then moves focus to the heading. `htmx:afterSettle` runs after that autofocus. When the notice is still the current focus owner, the handler focuses `#status` again. A later successful swap clears that owner, so Back to results and a fresh search keep the open-button and heading focus. The heading autofocus attribute stays, including for the no-JS page.

`too-broad` now submits the 422 query from the hostile search's `htmx:afterSwap`, then checks focus 60ms later. `gap0` does the same with a fast invalid time, which returns inside the settle window, and checks that focus is still `#status`. `guidance` and `notices` are `Object.create(null)`, so a header value of `constructor` or `toString` is not a sentence.

CI pins Node 22 with `actions/setup-node` at `949feb2413d6458794dcd2491c4babbbce0c15c1` (v7.1.0). The browser walk needs the global `WebSocket`. The job then runs:

```bash
go test -count=1 -tags "fts5 sqlite_omit_load_extension" -run TestBrowserE2E -timeout 180s -v ./internal/server/
```

Ten local runs, 0 failures:

```bash
CGO_ENABLED=1 go test -count=10 -tags "fts5 sqlite_omit_load_extension" -run TestBrowserE2E -timeout 15m -v ./internal/server/
```

```text
=== RUN   TestBrowserE2E
    browser_e2e_test.go:261: STEP login
    browser_e2e_test.go:261: STEP narrow
    browser_e2e_test.go:261: STEP remove-term
    browser_e2e_test.go:261: STEP enter-search
    browser_e2e_test.go:261: STEP save-open
    browser_e2e_test.go:261: STEP back-focus
    browser_e2e_test.go:261: STEP filter
    browser_e2e_test.go:261: STEP hostile
    browser_e2e_test.go:261: STEP too-broad
    browser_e2e_test.go:261: STEP gap0
    browser_e2e_test.go:261: STEP row-expired
    browser_e2e_test.go:261: NEED_CLOCK row
    browser_e2e_test.go:261: STEP cursor-expired
    browser_e2e_test.go:261: NEED_CLOCK cursor
    browser_e2e_test.go:261: NEED_CLOCK reset
    browser_e2e_test.go:261: STEP no-js
    browser_e2e_test.go:261: STEP session-expired
    browser_e2e_test.go:261: PASS
--- PASS: TestBrowserE2E (1.63s)
=== RUN   TestBrowserE2E
    browser_e2e_test.go:261: STEP login
    browser_e2e_test.go:261: STEP narrow
    browser_e2e_test.go:261: STEP remove-term
    browser_e2e_test.go:261: STEP enter-search
    browser_e2e_test.go:261: STEP save-open
    browser_e2e_test.go:261: STEP back-focus
    browser_e2e_test.go:261: STEP filter
    browser_e2e_test.go:261: STEP hostile
    browser_e2e_test.go:261: STEP too-broad
    browser_e2e_test.go:261: STEP gap0
    browser_e2e_test.go:261: STEP row-expired
    browser_e2e_test.go:261: NEED_CLOCK row
    browser_e2e_test.go:261: STEP cursor-expired
    browser_e2e_test.go:261: NEED_CLOCK cursor
    browser_e2e_test.go:261: NEED_CLOCK reset
    browser_e2e_test.go:261: STEP no-js
    browser_e2e_test.go:261: STEP session-expired
    browser_e2e_test.go:261: PASS
--- PASS: TestBrowserE2E (1.72s)
=== RUN   TestBrowserE2E
    browser_e2e_test.go:261: STEP login
    browser_e2e_test.go:261: STEP narrow
    browser_e2e_test.go:261: STEP remove-term
    browser_e2e_test.go:261: STEP enter-search
    browser_e2e_test.go:261: STEP save-open
    browser_e2e_test.go:261: STEP back-focus
    browser_e2e_test.go:261: STEP filter
    browser_e2e_test.go:261: STEP hostile
    browser_e2e_test.go:261: STEP too-broad
    browser_e2e_test.go:261: STEP gap0
    browser_e2e_test.go:261: STEP row-expired
    browser_e2e_test.go:261: NEED_CLOCK row
    browser_e2e_test.go:261: STEP cursor-expired
    browser_e2e_test.go:261: NEED_CLOCK cursor
    browser_e2e_test.go:261: NEED_CLOCK reset
    browser_e2e_test.go:261: STEP no-js
    browser_e2e_test.go:261: STEP session-expired
    browser_e2e_test.go:261: PASS
--- PASS: TestBrowserE2E (1.65s)
=== RUN   TestBrowserE2E
    browser_e2e_test.go:261: STEP login
    browser_e2e_test.go:261: STEP narrow
    browser_e2e_test.go:261: STEP remove-term
    browser_e2e_test.go:261: STEP enter-search
    browser_e2e_test.go:261: STEP save-open
    browser_e2e_test.go:261: STEP back-focus
    browser_e2e_test.go:261: STEP filter
    browser_e2e_test.go:261: STEP hostile
    browser_e2e_test.go:261: STEP too-broad
    browser_e2e_test.go:261: STEP gap0
    browser_e2e_test.go:261: STEP row-expired
    browser_e2e_test.go:261: NEED_CLOCK row
    browser_e2e_test.go:261: STEP cursor-expired
    browser_e2e_test.go:261: NEED_CLOCK cursor
    browser_e2e_test.go:261: NEED_CLOCK reset
    browser_e2e_test.go:261: STEP no-js
    browser_e2e_test.go:261: STEP session-expired
    browser_e2e_test.go:261: PASS
--- PASS: TestBrowserE2E (1.71s)
=== RUN   TestBrowserE2E
    browser_e2e_test.go:261: STEP login
    browser_e2e_test.go:261: STEP narrow
    browser_e2e_test.go:261: STEP remove-term
    browser_e2e_test.go:261: STEP enter-search
    browser_e2e_test.go:261: STEP save-open
    browser_e2e_test.go:261: STEP back-focus
    browser_e2e_test.go:261: STEP filter
    browser_e2e_test.go:261: STEP hostile
    browser_e2e_test.go:261: STEP too-broad
    browser_e2e_test.go:261: STEP gap0
    browser_e2e_test.go:261: STEP row-expired
    browser_e2e_test.go:261: NEED_CLOCK row
    browser_e2e_test.go:261: STEP cursor-expired
    browser_e2e_test.go:261: NEED_CLOCK cursor
    browser_e2e_test.go:261: NEED_CLOCK reset
    browser_e2e_test.go:261: STEP no-js
    browser_e2e_test.go:261: STEP session-expired
    browser_e2e_test.go:261: PASS
--- PASS: TestBrowserE2E (1.66s)
=== RUN   TestBrowserE2E
    browser_e2e_test.go:261: STEP login
    browser_e2e_test.go:261: STEP narrow
    browser_e2e_test.go:261: STEP remove-term
    browser_e2e_test.go:261: STEP enter-search
    browser_e2e_test.go:261: STEP save-open
    browser_e2e_test.go:261: STEP back-focus
    browser_e2e_test.go:261: STEP filter
    browser_e2e_test.go:261: STEP hostile
    browser_e2e_test.go:261: STEP too-broad
    browser_e2e_test.go:261: STEP gap0
    browser_e2e_test.go:261: STEP row-expired
    browser_e2e_test.go:261: NEED_CLOCK row
    browser_e2e_test.go:261: STEP cursor-expired
    browser_e2e_test.go:261: NEED_CLOCK cursor
    browser_e2e_test.go:261: NEED_CLOCK reset
    browser_e2e_test.go:261: STEP no-js
    browser_e2e_test.go:261: STEP session-expired
    browser_e2e_test.go:261: PASS
--- PASS: TestBrowserE2E (1.59s)
=== RUN   TestBrowserE2E
    browser_e2e_test.go:261: STEP login
    browser_e2e_test.go:261: STEP narrow
    browser_e2e_test.go:261: STEP remove-term
    browser_e2e_test.go:261: STEP enter-search
    browser_e2e_test.go:261: STEP save-open
    browser_e2e_test.go:261: STEP back-focus
    browser_e2e_test.go:261: STEP filter
    browser_e2e_test.go:261: STEP hostile
    browser_e2e_test.go:261: STEP too-broad
    browser_e2e_test.go:261: STEP gap0
    browser_e2e_test.go:261: STEP row-expired
    browser_e2e_test.go:261: NEED_CLOCK row
    browser_e2e_test.go:261: STEP cursor-expired
    browser_e2e_test.go:261: NEED_CLOCK cursor
    browser_e2e_test.go:261: NEED_CLOCK reset
    browser_e2e_test.go:261: STEP no-js
    browser_e2e_test.go:261: STEP session-expired
    browser_e2e_test.go:261: PASS
--- PASS: TestBrowserE2E (1.62s)
=== RUN   TestBrowserE2E
    browser_e2e_test.go:261: STEP login
    browser_e2e_test.go:261: STEP narrow
    browser_e2e_test.go:261: STEP remove-term
    browser_e2e_test.go:261: STEP enter-search
    browser_e2e_test.go:261: STEP save-open
    browser_e2e_test.go:261: STEP back-focus
    browser_e2e_test.go:261: STEP filter
    browser_e2e_test.go:261: STEP hostile
    browser_e2e_test.go:261: STEP too-broad
    browser_e2e_test.go:261: STEP gap0
    browser_e2e_test.go:261: STEP row-expired
    browser_e2e_test.go:261: NEED_CLOCK row
    browser_e2e_test.go:261: STEP cursor-expired
    browser_e2e_test.go:261: NEED_CLOCK cursor
    browser_e2e_test.go:261: NEED_CLOCK reset
    browser_e2e_test.go:261: STEP no-js
    browser_e2e_test.go:261: STEP session-expired
    browser_e2e_test.go:261: PASS
--- PASS: TestBrowserE2E (1.60s)
=== RUN   TestBrowserE2E
    browser_e2e_test.go:261: STEP login
    browser_e2e_test.go:261: STEP narrow
    browser_e2e_test.go:261: STEP remove-term
    browser_e2e_test.go:261: STEP enter-search
    browser_e2e_test.go:261: STEP save-open
    browser_e2e_test.go:261: STEP back-focus
    browser_e2e_test.go:261: STEP filter
    browser_e2e_test.go:261: STEP hostile
    browser_e2e_test.go:261: STEP too-broad
    browser_e2e_test.go:261: STEP gap0
    browser_e2e_test.go:261: STEP row-expired
    browser_e2e_test.go:261: NEED_CLOCK row
    browser_e2e_test.go:261: STEP cursor-expired
    browser_e2e_test.go:261: NEED_CLOCK cursor
    browser_e2e_test.go:261: NEED_CLOCK reset
    browser_e2e_test.go:261: STEP no-js
    browser_e2e_test.go:261: STEP session-expired
    browser_e2e_test.go:261: PASS
--- PASS: TestBrowserE2E (1.71s)
=== RUN   TestBrowserE2E
    browser_e2e_test.go:261: STEP login
    browser_e2e_test.go:261: STEP narrow
    browser_e2e_test.go:261: STEP remove-term
    browser_e2e_test.go:261: STEP enter-search
    browser_e2e_test.go:261: STEP save-open
    browser_e2e_test.go:261: STEP back-focus
    browser_e2e_test.go:261: STEP filter
    browser_e2e_test.go:261: STEP hostile
    browser_e2e_test.go:261: STEP too-broad
    browser_e2e_test.go:261: STEP gap0
    browser_e2e_test.go:261: STEP row-expired
    browser_e2e_test.go:261: NEED_CLOCK row
    browser_e2e_test.go:261: STEP cursor-expired
    browser_e2e_test.go:261: NEED_CLOCK cursor
    browser_e2e_test.go:261: NEED_CLOCK reset
    browser_e2e_test.go:261: STEP no-js
    browser_e2e_test.go:261: STEP session-expired
    browser_e2e_test.go:261: PASS
--- PASS: TestBrowserE2E (1.71s)
PASS
ok  	example.invalid/hearthlog/internal/server	16.612s
```

The same test passed again inside the race run above. Screenshots used `E2E_SHOT_DIR=docs/evidence/slice06`.

## NOTRUN

- Full keyboard Tab and Escape order, and 320px at 200% zoom. Enter in a term field is the browser walk. 360px and 390px were measured at 1x, and the walk checks that 390px does not scroll sideways.
- A live 504 from Chrome. The client path is the node test that replaces `#status` and fires `htmx:timeout`. The server copy is `TestUIGuidanceStates`.
- Notice placement and color. That polish is left for later.
- C8 Live polling, catch-up, and reconnect. That is Slice 07.
- 30-minute profile, power loss, and count-all.
- OIDC and source lifecycle. Slice 08.
- Slice 09 index on `(workspace_id, received_at, ingest_id)`, and a fresh confirmation of single-session login. The existing second-login invalidation was not changed.
- Slice 10 retention sweeper deleted-through watermark. Without that watermark, a cursor can walk a snapshot after rows have been deleted.
- Local gitleaks. CI runs it, including on this follow-up. The pull request records that run id.
