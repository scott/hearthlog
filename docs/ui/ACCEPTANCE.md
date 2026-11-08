## ACCEPTANCE.md

**Status:** Specification walkthrough only. Executable app, browser, layout, keyboard, noJS, htmx, and Live checks are **NOT RUN**. Execution scheduled for Slice 02 skeleton and Slices 06/07 final cuts.

### Check Procedure

- [ ] **C1: Core Search & Detail.** Select `src_demo_api`, custom absolute UTC window `2026-01-02 03:00–03:15`, min severity INFO, term `synthetic`. Submit Search. Verify window and focus per [INVESTIGATION.md](INVESTIGATION.md). Open details; verify typed attributes. Back to results; verify focus return to the originating Open details button. Reopen details; apply Filter; verify `#results-heading` focus, predicate addition, and reset-cursor semantics.
- [ ] **C2: Distinct Empty States.** Trace no-sources, no-ingest, and filtered-zero scenarios. Verify distinct copy and actions per [STATES.md](STATES.md). Ensure no conflation of empty vs. zero-matches.
- [ ] **C3: Error Handling.** Trigger/inspect 400 (invalid), 422 (broad), 503 (busy), 504 (timeout), 410 (expired), 401/403 (denied), and 404 (scoped). Verify exact copy, focus targets, data clearing rules, and user actions per [STATES.md](STATES.md).
- [ ] **C4: Security & Typing.** Ingest hostile and typed fixtures from [FIXTURES.md](FIXTURES.md). Verify HTML escaping, disabled "Unsupported path" filters, "Show full" behavior, exact numeric type preservation (int/float/bool/string), and redacted JSON copy.
- [ ] **C5: Accessibility & Layout.** Walk Tab/Enter/Escape order. Verify focus management. Test 320px width at 200% zoom for stacking and visibility. **Visual test NOT RUN.**
- [ ] **C6: No-JavaScript Fallback.** Verify POST forms for Search/Filter/Older/Back and Detail. Confirm Detail POST carries bounded context for Back-to-results. Confirm selectable JSON fallback. Verify manual Refresh triggers historical search, not polling. See [TRANSPORT.md](TRANSPORT.md).
- [ ] **C7: Transport Integrity.** Verify reordered requests are discarded pre-swap. Confirm `hx-sync` cancellation, header history behavior, persisted-only restore, and auth-denial content clearing. Details in [TRANSPORT.md](TRANSPORT.md).
- [ ] **C8: Live Interface.** Verify Pause/Resume/Reconnect/Search gap flows. Confirm single outstanding poll, row/catchup bounds, and per-poll watermark logic. See [LIVE.md](LIVE.md). Subcases (NOT RUN): (a) network/timeout/500/504 => stop/remove trigger/abort/invalidate, rows stale/cursor unchanged, late responses rejected; (b) 429/503 (with/without trusted fragment) Retry-After 2s => no poll at 1s, 2s+ requires explicit Reconnect; rows stale/cursor unchanged; absent/malformed wait 5s, unrepresentable stays stopped; (c) Reconnect/Resume reauthorizes, validates 5000 events AND 30s, fresh generation, <=1 probe, only success enables every 1s; failed probe stops; (d) 410 clears cursor/Search gap; 400/422 Edit search, no unchanged retry; (e) 401/403/stale auth globally stop+clear; missing markers only on successful 2xx login HTML triggers global stop+clear; known non-2xx throttle/timeout follows state matrix; no Reconnect bypass, fresh authorized start required. Original Pause/catchup/DOM bound cases preserved.

### Evidence Record Schema

Each check requires a record with the following fields. No passes are prepopulated.

| Field | Description |
| :--- | :--- |
| `case_id` | C1–C8 |
| `ref` | Source file + heading (e.g., `INVESTIGATION.md#3-the-detail-panel`) |
| `expected` | Specific spec requirement text |
| `observed` | Result of walkthrough or future runtime test |
| `reviewer` | Name + Date |
| `status` | `DOCWALK` (spec check) or `NOTRUN` (pending execution) |