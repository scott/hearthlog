# Historical Investigation States

Historical query and detail views only. Live polling states are specified in `docs/ui/LIVE.md`.

| ID | Trigger + HTTP | Exact Copy | Action + Data |
|---|---|---|---|
| Initial | Fresh load, no query | "Ready to investigate" | Empty form. Focus source selector. |
| Loading | Search submitted, 200 pending | "Searching..." | Preserve prior rows, mark `data-stale="true"`. |
| Onboarding | Zero registered sources | "No sources yet" | Link: "Register source". |
| No Events | Valid query, no ingested events (bounded metadata) | "No events in this window" | Form preserved. Distinct from filter zero-results. |
| Success | 200, rows > 0 | Heading: "Results" | Render rows. Focus #results-heading. |
| Detail | Row selected | Heading: "Event details" | Render typed attributes. |
| No Matches | Valid filter, zero results | "No matching events" | Form preserved. Distinct from no-ingest. |
| Invalid Query | 400 `invalid_query` | "Query is invalid" | Focus linked field. Do not retry. |
| Unsupported Path | 400 `unsupported_path` | "Attribute path not supported" | Focus predicate. Do not retry. |
| Too Broad | 422 `query_too_broad` | "Query too broad" | Guidance: narrow window. Do not show zero matches. |
| Busy | 503 `busy` | "Service busy" | Wait `Retry-After`. Preserve stale rows. |
| Rate Limited | 429 `rate_limited` | "Rate limited" | Wait `Retry-After`. Respect budget. |
| Timeout | 504 `query_timeout` | "Query timed out" | Suggest narrowing. Do not auto-retry. |
| Auth Denied | 401 / 403 | "Access denied" | Clear all protected content. Cancel requests. |
| Detail Missing | 404 | "Event not found" | Same response for missing/cross-workspace. |
| Cursor Expired | 410 `cursor_expired` (15m/retention/epoch) | "Results expired" | Clear old cursor. Prompt: "Rerun query". Never silently partial. |
| Network Error | Client failure | "Connection error" | Preserve stale rows. Enable Retry. |

## Global Rules

* **Loading/Transient (5xx/429/408):** Retain prior rows, clearly marked stale. No automatic search retries.
* **Auth Denial (401/403):** Immediately clear all protected content, cancel pending requests, redirect to login.
* **Invalid (400):** Stop. Do not retry. Focus validation error.
* **Too Broad (422):** Stop. Do not treat as "zero matches". Provide narrowing guidance.
* **Cursor Expired (410):** Invalidate horizon. Require explicit rerun. Do not serve partial old data.
* **Retry-After:** Respect server value before enabling Retry. No infinite loops.

## UI Behavior

* **Focus:** `#status` summarizes failure first, then links to specific invalid field. Results/Detail headings announce successful actions.
* **Accessibility:** `aria-live="polite"` on `#status` only. Never announce individual log rows.
* **No-JS:** Responses are equivalent full HTML pages. All states reachable via standard links/forms.
* **Transitions:** Deterministic. "Search", "Retry", "Sign in", "Back to results" navigate explicitly. No hidden state.
* **Safety:** Error copy is constant. Never echo query text, SQL, raw payload, or tokens.

This is a Slice 01 specification. No APPLICATION implementation/runtime-test pass is claimed; static documentation evidence is separate. See `docs/ui/INVESTIGATION.md`, `docs/ui/TRANSPORT.md`, `docs/ui/ACCEPTANCE.md`, `docs/ui/LIVE.md`.