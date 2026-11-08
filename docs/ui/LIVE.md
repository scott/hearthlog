# Live Interface

**Slice 07 design.** Live is a distinct view from **Investigate** (Historical Search). Live shows incoming events; Investigate queries stored history. This is a design specification, not an implemented feature.

## State Model

| State | Exact UI Copy | Trigger | User Action |
|---|---|---|---|
| Active | "Live: Receiving" | Poll succeeds; cursor advances | — |
| Paused | "Live: Paused" | User clicks **Pause**; tab hidden | **Resume** |
| Disconnected | "Live: Disconnected" | Network failure only | **Reconnect** |
| Gap | "Live: Gap detected" | Catchup exceeds bounds; cursor reset | **Search gap** |
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

## Resume and Catchup

*   **Resume:** User clicks **Resume**.
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