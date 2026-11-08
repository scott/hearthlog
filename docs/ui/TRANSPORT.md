# UI Transport Design (Slice 01)

This document specifies the HTTP/htmx transport contract for the Hearthlog investigation interface. It is a Slice 01 design specification only; no implementation or test pass is claimed.

## Routing and Fallback

All interactions use fixed same-origin routes. `HX-Request` is a routing hint only, never an authorization signal.

| Interaction | Route | Method | Target |
|---|---|---|---|
| Root | `/investigate` | GET | *(full page)* |
| Search/Filter | `/investigate/search` | POST | `#results-region` |
| Detail | `/investigate/detail` | POST | `#detail-region` |
| Live Poll | `/investigate/live` | GET | `#live-region` |
| Back/Pagination | `/investigate/search` | POST | `#results-region` |

**No-JavaScript Fallback:**
*   **Search/Filter/Paging:** `POST` forms with session-bound CSRF tokens. Filters include absolute `from`/`to` windows, validated predicates, and cursor. No raw query terms appear in URLs.
*   **Detail:** `POST` forms carrying selected opaque row ID plus bounded validated typed search context (frozen window, cursor, origin focus ID) and session-bound CSRF. This propagates context for Back-to-results without server-side last-query state. If a GET route is retained, it is strictly standalone (no context) with "New search" fallback, never promising previous-results return.
*   **Back to Results:** `POST` form carrying bounded query context/window, not URL-derived terms.
*   **Copy JSON:** Selectable escaped `<pre>` block; no JS required.
*   Full-page responses include skip links and identical labels/heading structure.

## Synchronization and Stale Response Handling

*   **hx-sync:** All search/detail/paging requests use `hx-sync="#investigation:replace"`. New searches cancel older in-flight requests and invalidate the currently selected detail view.
*   **Generation & Sequence:** Server echoes submitted bounded query generation, selection ID, and request sequence as trusted scalars in the response.
*   **Pre-Swap Validation:** An external `htmx:beforeSwap` handler (no inline scripts) validates generation, request sequence, and **selected-detail ID** against current DOM state **before** any DOM modification. Stale responses (including those targeting an older detail selection) are rejected and discarded. This occurs prior to `innerHTML` application.
*   **Live Region:** `#live-region` uses append/prune logic per [LIVE.md](LIVE.md), not generic `innerHTML` replacement.

## Security and Authentication

*   **Reauthorization:** Every request (full/partial, htmx/form) undergoes full workspace/source authorization.
*   **401/403 Handling:** On auth denial, the handler clears ALL protected DOM (source labels, filter inputs, copied JSON, results, detail, Live), invalidates generations, cancels requests, then shows safe "Access denied". `pagehide` clears DOM; **only** `pageshow` triggered by persistence restoration (persisted flag true) triggers a full authenticated reload before revealing content. Normal initial `pageshow` does **not** reload. Note: browser history persists; GET filter URLs may expose terms; no raw POST filter logging. Pinned self-hosted htmx2.x assets are a later implementation requirement.
*   **CSRF:** All state-changing `POST` forms include session-bound tokens.
*   **htmx Config:** `selfRequestsOnly: true`, `allowEval: false`, `allowScriptTags: false`, `reportValidityOfForms: true`, timeout 5000ms.
*   **History:** `hx-history: false`, `historyCacheSize: 0`, `historyRestoreAsHxRequest: false`, `hx-push-url: false`, `hx-replace-url: false`.
*   **CSP:** Strict same-origin. No inline handlers/eval. `template.HTML`/JS/URL bypasses are prohibited. Escaping prevents `hx-`/`data-hx-` injection.

## Error Handling and Privacy

*   **Default:** htmx does not swap 4xx/5xx by default.
*   **Explicit Handling:** `htmx:beforeSwap` validates allowlisted codes (400, 404, 410, 422, 429, 503, 504) targeting `#status`, setting `shouldSwap=true` only for trusted escaped same-origin fragments. External handlers for `htmx:sendError` and `htmx:timeout` display safe, static `#status` messages and **preserve existing DOM data** (except on global auth denial which clears protected content). No automatic retry loops are initiated. `htmx:responseError` handles remaining HTTP notifications. Never raw unexpected body.
    *   **410:** "Results expired" (Action: "Rerun query").
    *   **Unexpected Bodies:** Never inserted into DOM.
*   **Login Redirects:** If a 302 occurs, the browser may auto-follow. The handler does **not** detect intermediate 302s. It requires the *final* response to be same-origin, HTML content-type, and contain fixed fragment-kind/generation headers. Missing markers trigger a full navigation to `/investigate`. Untrusted URLs are never returned.
*   **Headers:** `Cache-Control: no-store`, `Vary: HX-Request`, `Referrer-Policy: no-referrer` on all authenticated responses and errors.
*   **Privacy:** No persistent browser storage/telemetry. Server logs exclude raw query strings. `pagehide` clears protected DOM; `pageshow` triggers full authenticated reload **only** if triggered by persistence restoration (persisted flag true), not on initial load.

## Accessibility

*   **Focus:** Moves to `#results-heading` (search), `#detail-heading` (detail), or `#status` (failure). Closing detail returns focus to origin row or fallback `#results-heading`.
*   **ARIA:** `#status` uses `aria-live="polite"`. Live rows do not use `aria-live`.

## Related Documents

*   [INVESTIGATION.md](INVESTIGATION.md)
*   [STATES.md](STATES.md)
*   [ACCEPTANCE.md](ACCEPTANCE.md)
*   [LIVE.md](LIVE.md)