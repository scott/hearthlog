# Live Interface

**Slice 07 design.** Live is a distinct view from **Investigate** (Historical Search). Live shows incoming events; Investigate queries stored history. This is a design specification, not an implemented feature.

## State Model

| State | Exact UI Copy | Trigger | User Action |
|---|---|---|---|
| Active | "Live: Receiving" | Poll succeeds; cursor advances | — |
| Paused | "Live: Paused" | User clicks **Pause**; tab hidden | **Resume** |
| Disconnected | "Live: Disconnected" | Network failure, client timeout, HTTP 504, transient 5xx (excl. 503) | **Reconnect** |
| Gap | "Live: Gap detected" | Catchup exceeds bounds (410 or >5k/30s); cursor reset | **Search gap** |
| Disabled (No JS) | "Live requires JavaScript. Use Refresh for historical search." | htmx unavailable; JS disabled | **Refresh** (triggers historical form) |

## Polling Mechanics

*   **Frequency:** `hx-trigger="every 1s"`.
*   **Concurrency:** One outstanding request per tab. Maximum two active Live clients per workspace.
*   **Synchronization:** `hx-sync="#live-region:drop"` with independent generation. Never queue backlog.
*   **Fetch:** ≤200 committed rows where `ingest_id > cursor`.
*   **Watermark:** Each poll captures the **current committed watermark** at request time. Cursor compares against this per-poll value, not a frozen initial watermark or generic event clock.
*   **Cursor Logic:** Monotonic `ingest_id`. Advances to the **highest examined authorized position**, including non-matching rows, to prevent sparse-filter stalls.
*   **Deduplication:** Events deduplicated by `ingest_id`. Filters and generation are fixed for the Live session.
*   **Termination:** Cancellation on Pause, tab hidden/closed, auth denied, or network loss. No backlog accumulation during pause. No hidden background polls.

## Failure Handling

| HTTP/Condition | Visible Copy | Recovery Action | Note |
|---|---|---|---|
| Network, Timeout, 504, 5xx (transient, excl. 503) | "Live: Disconnected" | **Reconnect** | Stops polling. No auto-retry. |
| 429 / 503 | "Live: Throttled" | **Reconnect** | Disabled until `Retry-After` (or 5s fallback) elapses. |
| 410 | "Live: Gap detected" | **Search gap** | Clears cursor. Requires fresh historical query. |
| 400 / 422 | "Live: Query invalid" | **Edit search** | Clears invalid cursor. Requires fresh validated query. No unchanged retry. |
| 401 / 403 | "Access denied" | **Sign in** | Terminal. Invokes shared stop for ALL Live generations (incl. stale/session). Clears protected DOM. Requires fresh authorized start. No Reconnect. |
| Other | "Live: Error" | **New search** | Stops polling. Explicit manual restart. |

**Shared Stop Transition:**
1. Mark state inactive.
2. Disable periodic scheduling.
3. Cancel/abort pending request.
4. Remove dedicated polling trigger element (not row region).
5. Invalidate generation.

**Event Handlers:**
*   `htmx:beforeOnLoad`: Classify HTTP status; invoke shared stop transition for current-generation failed polls.
*   `htmx:sendError` / `htmx:timeout`: Trigger idempotent stop transition.
*   `htmx:beforeRequest`: Cancel via `preventDefault` if inactive; remove dedicated trigger element.

**Rules:**
*   Failed polls retain last accepted cursor unless invalidation clears it.
*   Stale non-auth failures discarded; stale auth denial clears globally.
*   Auth denial dominates, clearing all protected DOM.
*   Rows remain stale on transient errors, not on denial.

## Recovery and Catchup

*   **Resume/Reconnect:** Explicit click required. Reauthorizes and enforces ≤5,000 events AND ≤30s catchup; else Gap. Starts fresh generation, at most one probe; no retry queue. Only validated successful probe reenables 1s scheduling.
*   **Retry-After:** Validated nonnegative integer seconds. Valid delay respected without shortening. Absent/malformed uses 5s. Unrepresentable stays stopped. Deadline expiry does not auto-rearm.
*   **State Lockout:** Invalid/Query/Auth states cannot bypass required actions via Reconnect.
*   **Catchup Bounds:** Supported if ≤5,000 events AND ≤30 seconds.
*   **Gap Handling:** If bounds are exceeded, UI displays "Live: Gap detected". The **Search gap** action redirects to Historical Search with the explicit absolute time interval of the gap pre-filled.
*   **Guarantee:** Live is a view, not an export guarantee. Gaps are explicitly told to the user.

## DOM and Performance

*   **Limit:** Maximum 1,000 rows in the DOM.
*   **Pruning:** External `after-swap` listener appends new rows and prunes oldest rows before the next poll.
*   **Verification:** Cursor and status verified in the external listener before the next trigger fires.

## Failure and Accessibility

*   **Network:** Failure stops polling. Explicit **Reconnect** button required; no automatic spin/retry.
*   **No JavaScript:** Display "Live requires JavaScript. Use Refresh for historical search." The **Refresh** action submits the historical search form (no polling).
*   **Auth Denied:** Clear protected content. Cancel all pending requests. Return to denied state. Do not offer Reconnect to bypass auth.
*   **Focus:** Status uses `aria-live="polite"` (not per-log-row). **Pause** retains focus. **Resume** returns focus to the pause control. **Search gap** moves focus to the historical search form. No automatic mode changes.

## References

*   [STATES.md](STATES.md)
*   [TRANSPORT.md](TRANSPORT.md)
*   [INVESTIGATION.md](INVESTIGATION.md)
*   [ACCEPTANCE.md](ACCEPTANCE.md)

*All limits are provisional design targets, not measured performance guarantees.*