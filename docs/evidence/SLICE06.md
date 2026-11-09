# Slice 06 evidence: server-rendered investigation UI

Synthetic fixtures only. The pages below were rendered by the investigation
handlers and captured in headless Chrome. They are not production data.
Live polling is not this slice. `user_version` stays 1.

## Test profile

| Item | Value |
|---|---|
| Target | linux/amd64, CGO_ENABLED=1, build tags `fts5 sqlite_omit_load_extension` |
| Toolchain | go1.27.3 (GOTOOLCHAIN=local), gcc 13.3.0, node v22.23.4 |
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

Node: 5 pass, 0 fail. The fifth test is `422, 410, and 504 tell the user what to do`.
`go mod verify` reported all modules verified. `gofmt -l` was empty. `go vet`
was clean.

Race package results (0 failures):

```text
ok  example.invalid/hearthlog/cmd/hearthlog             1.012s
ok  example.invalid/hearthlog/examples/slogservice      1.024s
ok  example.invalid/hearthlog/internal/model            1.044s
ok  example.invalid/hearthlog/internal/server           8.976s
ok  example.invalid/hearthlog/internal/storage/sqlite  14.326s
ok  example.invalid/hearthlog/internal/emitter         22.046s
```

Same command with `-json`: 124 top-level tests, 330 including subtests,
0 failures (cmd 1, examples 4, emitter 38/108, model 11/76, server 37/80,
sqlite 33/61). Node: 5 pass, 0 fail.

gitleaks v8.30.1 is not on PATH here. The leak scan is the CI step inside
the `test` job. The pull request records that run id for the head SHA.

## Done when

| Acceptance | Proof |
|---|---|
| Semantic full pages plus htmx filter, result, and detail partials for source, time, severity, message terms, typed attribute chips, and saved views | `internal/server/web/page.html`, `results.html`, `detail.html`, `context.html`. `TestFragments` checks one results region and one detail region, fragment headers, and frozen from/to. `TestAddTermControl`, `TestTermsPredicatesAndHostileDetail`, `TestSavedViewReevaluatesRelativeWindow`. |
| Search, detail, and pagination work as normal form posts. Stale htmx responses are generation-rejected. Errors keep inputs. Focus survives a detail close in the client | `TestFormSessionGuards`, `TestSearchFullPage`, `TestFormCapKeepsCursorAndFiltersApart` (Older posts the cursor only). `app.test.js` `non-auth focus: current/stale/unknown` and the generation/sequence checks in `app.js configRequest and beforeRequest`. |
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

## Judgment calls

- Pagination and detail-back posts send the signed cursor and recover filters with `FiltersFromCursor`. There is no server-side last-query cache. The cursor still embeds the filters, so storage tests that compare the cursor body to the query stay valid.
- A relative preset (5m, 15m, 1h, 24h) is recomputed only when the request has no cursor. Detail, back, and filter-on-value send `preset=custom` and the frozen from/to. The exclusive end of a fresh relative window is one second past now so an event stamped in the current second is inside the window.
- Saved views store the preset and the filters, not a frozen absolute window. A custom window cannot be saved. Opening a view recomputes the window from the store clock.
- The "Register source" link points at `/investigate`. Source registration is Slice 08.
- Session denial copy stays "Session expired. " plus "Sign in". Investigation 503 is the HTML busy page. API 503 stays JSON. A result that exceeds the render budget stays JSON `query_too_broad`.
- Detail of a row past the live cutoff is HTTP 404 with `data-code="row_expired"`. Cursor expiry stays HTTP 410. The 410 page puts the cursor's filters back into the form and clears the cursor.
- `IndexLogsRetention` is the only name used for `idx_logs_retention` in code. Slice 05 evidence still quotes the literal, because that file records the earlier run.

## NOTRUN

- Interactive browser click-through: keyboard Tab/Enter/Escape order, htmx swap after a real click, and focus return in a live browser. Client focus and stale-response behavior are the node tests. Form posts are the Go HTTP tests. Chrome was used to render and measure, not to click through the product.
- C5 at 320px and 200% zoom. 360px and 390px were measured at 1x device scale, with no sideways scroll.
- C8 Live polling, catch-up, and reconnect. That is Slice 07.
- 30-minute profile, power loss, and count-all.
- OIDC and source lifecycle. Slice 08.
- Slice 09 index on `(workspace_id, received_at, ingest_id)`, and a fresh confirmation of single-session login. The existing second-login invalidation was not changed.
- Slice 10 retention sweeper deleted-through watermark. Without that watermark, a cursor can walk a snapshot after rows have been deleted.
- Local gitleaks. CI runs it.
