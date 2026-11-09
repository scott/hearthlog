# Slice 02 local QA evidence (post-PR16)

Recorded November 8, 2026. All inputs are synthetic fixtures from the
`cmd/synthetic` emitter and the loopback service; every credential, canary and
path shown here is a placeholder. This records a **local-only** independent
reproduction of the PR #16 "Remaining QA" list. It is not remote CI, not a
production deployment, and not a NATIVE-V1 conformance claim.

- Source: `slice02-walking-skeleton` @ `dbe3937`
- Toolchain: Go 1.27.3, `CGO_ENABLED=1`, build tags `fts5
  sqlite_omit_load_extension`, `github.com/mattn/go-sqlite3 v1.14.53` (bundled
  SQLite 3.54.1); htmx 2.0.11 pinned `d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717`
- Host: macOS arm64. The declared CI target is Linux amd64 (see `.github`
  workflow); local darwin/arm64 reproduction is evidence of behavior, not of the
  CI target.

## Reproduce

```sh
go mod verify
CGO_ENABLED=1 go build -tags "fts5 sqlite_omit_load_extension" -o "$B/hearthlog" ./cmd/hearthlog
CGO_ENABLED=1 go build -tags "fts5 sqlite_omit_load_extension" -o "$B/synthetic"  ./cmd/synthetic
CGO_ENABLED=1 go test -race -count=1 -tags "fts5 sqlite_omit_load_extension" ./...
CGO_ENABLED=1 go vet   -tags "fts5 sqlite_omit_load_extension" ./...
node --test internal/server/web/app.test.js
gitleaks dir --redact --no-banner
gitleaks git --redact --no-banner

# Run: private state dir outside the checkout, loopback only
"$B/hearthlog" -state "$STATE_DIR" -listen 127.0.0.1:8787 &
"$B/synthetic" -endpoint http://127.0.0.1:8787/api/experimental/skeleton/batches \
               -write-token-file "$STATE_DIR/write.token" -canary-file "$STATE_DIR/canary.txt"
# SIGTERM, then restart with the SAME $STATE_DIR (no reseed)
```

All commands ran locally against a private 0700 state directory under a temp
path; tokens and the canary are 64-hex/32-hex synthetic values never printed
here.

## Automated gates (reproduced)

| Gate | Result |
|---|---|
| `go mod verify` | all modules verified |
| `go test -race -count=1` (emitter, model, server, storage/sqlite) | all `ok`, 0 failures |
| `go vet` (both build tags) | clean |
| `gofmt -l .` | clean (CI gate) |
| `node --test .../app.test.js` | 4/4 pass (htmx pin, generation/sequence, beforeSwap stale-sequence/cross-origin/wrong-selection, non-auth error focus, 401/403 fail-closed) |
| `gitleaks dir` / `git` / `pre-commit` | no leaks found |

## Browser observation (current revision, JS enabled, htmx)

Performed in a real browser against the loopback service at `dbe3937`, using a
synthetic READ token in the password field (no secret in the URL).

| Case | Expected (docs/ui) | Observed |
|---|---|---|
| C1 Core search | Search `src_demo_api`/`slice02synthetic`/15m | 100 rows in ~100 ms; focus → `#results-heading`; 100 "Open details" buttons |
| C1 Detail | Open row → typed attrs, focus `#detail-heading` | Detail panel: event/received time, severity `9 (INFO)`, source, escaped message; focus on `#detail-heading`; results region `hidden`+`inert` |
| C1 Back focus | Return focus to originating Open details button | After "Back to results", focus returned to `#event-<origin>-open` (origin matched `data-origin`); results restored, detail cleared |
| C1 Hostile data | `<script>alert(1)</script>` literal | 0 real `<script>` nodes in results; literal text in row and detail heading; `<pre>` attrs escaped; `password` attr absent; `exact_large_int64=9007199254740993` preserved |
| Skiplink | First tab stop, reveal on focus, target main | First tab stop, `href="#investigation"`; `.skiplink:focus{top:.5rem}` rule present; reveal at 0.5rem verified by applying the rule's value (document focus could not attach in this automation context, see Limitations) |
| 320px + 200% (combined) | Stack, no horizontal overflow, controls visible | Emulated 320 CSS px @ 2× deviceScaleFactor: no horizontal overflow; main full-width; from/to/severity/term/Search/status/heading all in-viewport; 100 rows wrap, no clipping |
| Safe status denial | 401 → clear protected content, focus `#status`, "Sign in" | Controlled 401 (rotated session invalidated the open tab's cookie) on an in-flight htmx search: status "Session expired. Sign in" (link → `/login`), all 100 rows cleared to 0, detail cleared, form fields cleared, focus on `#status`. (Node fixtures cover the unit behavior; this is the live end-to-end confirmation.) |
| GET 401 cause | Not assumed "expiry" | Confirmed: plain `GET /investigate` with a missing/invalid session cookie → 401; valid cookie → 200. The controlled denial used a rotation that invalidated the open cookie. |
| Transport headers | no-store, Vary, Referrer-Policy, fragment markers | Full page and `HX-Request` partial both set `Cache-Control: no-store`, `Vary: HX-Request`, `Referrer-Policy: strict-origin` (matching document `<meta>`), CSP same-origin, `X-Content-Type-Options: nosniff`; partials carry `X-Fragment-Kind`/`X-Generation`/`X-Sequence` |
| No-JS fallback | Full-page responses work without htmx | POST `/investigate/search` without `HX-Request` returned a full `<!DOCTYPE html>` page with 100 rows and the skiplink; login is a plain POST form with no `<script>` tags |

## Protocol / security behavior (loopback, curl)

| Check | Result |
|---|---|
| Canonical `/api/v1/batches` | 404 (not yet NATIVE-V1 conformant) |
| Unknown write credential | 401 |
| Unknown READ credential at `/login` | 401 |
| New batch → receipt | 200 |
| Repeated batch id | 501 `replay_not_supported`, `retryable:false` |
| Canary absence | 0 occurrences across store raw bytes (db/wal/shm/lock), logical `logs.message`/`logs.attributes`, FTS5 `MATCH`, diagnostics (server log + terminal), and rendered HTML (login/investigate/results/detail); `[REDACTED]` present; `password` key dropped. Wire carries the raw pre-transmission value by design (client redaction is the control; storage/FTS/HTML are what's asserted) |
| 2 MiB overflow → 422 | Not reachable over HTTP with 100 small events (limit capped at 200 rows); covered by `result_bytes_test.go`, `bounded_render_test.go`, `result_http_test.go` (passing in the race run) |

## Persistence

100 synthetic events committed, service stopped (SIGTERM, graceful), restarted
with the **same** state directory: `integrity_check=ok`, `logs` count = 100,
`logs_fts` count = 100, `metadata` watermark = 100; same-day search still
returns 100 rows.

## Limitations (honest, not claimed)

- **Skiplink live `:focus`:** the automation browser tab could not attach OS
  document focus (`document.hasFocus()==false` in the backgrounded/locked
  context), so the CSS reveal was verified by applying the `.skiplink:focus`
  value rather than by a real keyboard Tab in a focused window. First-tab-stop
  order, target, and the reveal rule are confirmed; the live reveal needs a
  human-in-front-of-window confirmation.
- **Submit-button busy after auth denial:** after a 401 the submit button
  remains disabled/`aria-busy` (htmx does not run `afterSettle` on non-2xx).
  This is correct (no session, can't search) and the "Sign in" link is the
  recovery path; noted because it differs from a transient-error reset.
- **Combined 320px+200%:** verified via device emulation (320 CSS px @ 2×
  density, the standard CDP equivalent of a narrow viewport at 200% zoom), not
  a real narrow physical display.
- **C8 Live, and C3 states not reachable in the skeleton** (503 busy under
  65-concurrency, 429 rate limit, 504/410 expired, multi-source empty states):
  Live/filtering/pagination are explicitly **out of scope** for Slice 02
  (deferred to Slices 06–07); those states are implemented by design, not yet
  exercised. Recorded as NOT-RUN, not passed.
- **Remote CI:** not run here (pre-merge gate; Linux amd64 target).

## Reviewer

Independent local reproduction by an agent; sanitized, no secrets, synthetic
fixtures only. Reviewer to confirm the two Limitations items in a focused
window before treating the accessibility case as fully PASS.
