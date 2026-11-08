# INVESTIGATION.md

## Scope
This document specifies the original UI flow, wireframes, and interaction logic for searching and inspecting structured logs. It is a design artifact for **Slice 01**. It does not claim that any feature is implemented.

**Out of scope (defined in sibling documents):**
- [STATES.md](STATES.md): Exhaustive loading, empty, error, and paused states.
- [TRANSPORT.md](TRANSPORT.md): htmx partials, cache headers, history, and transport rules.
- [ACCEPTANCE.md](ACCEPTANCE.md): Test procedures, fixtures, and pass/fail criteria.

## Layout and Navigation
The interface uses a two-column layout: **Investigate** (left/filter) and **Results/Detail** (right/content). At 100% width and 200% zoom, all controls remain accessible without horizontal scrolling.

1. **Entry:** `/investigate` or a saved view.
2. **Search:** User configures filters.
3. **Submit:** "Search" (or `Enter` in message box).
4. **Results:** Dense list.
5. **Detail:** Selecting a row opens the Detail panel.
6. **Filter-on-Value:** Clicking "Filter" in Detail adds a predicate.
7. **Return:** "Back to results" or `Escape`. Focus returns to the specific row.

### Wireframes & Copy

#### 1. The Investigate Panel
Stacks above results on narrow screens (<600px).

```text
[ Sources: All (3) ▼ ]  [ Time: 15m ▼ ]  [ Min Severity: TRACE ▼ ]
[ Term 1: "synthetic" ] [ + Add term ] [ - Remove term ] [ Predicates: 1 active ]
[ + Add Attribute Filter ]                 [ Search ] [ Clear filters ]
```
* **Sources:** Multi-select, bounded to authorized sources (≤32).
* **Time:** Dropdown (5m, 15m, 1h, 24h) or "Custom". Displays resolved absolute window (e.g., `2026-01-02 03:00 to 03:15 UTC`).
* **Min Severity:** Single select (TRACE=1, DEBUG=5, INFO=9, WARN=13, ERROR=17, FATAL=21). Default: TRACE.
* **Message:** Up to eight separate labelled literal-term inputs, each ≤128 UTF-8 bytes, with "Add term" and "Remove term" buttons. No quote parser or splitting. Server constructs AND of quoted literal terms with unicode61 folding.
* **Predicates:** List of typed attribute filters (≤8).

#### 2. The Results List
Dense, tabular layout.

```text
| Timestamp (Event)   | Level | Source      | Message (truncated)
| 2026-01-02 03:04:05 | INFO  | demo-api    | synthetic task completed...
| 2026-01-02 03:04:04 | ERROR | demo-worker | failed to process job #42
```
* **Timestamps:** Displayed in UTC.
* **Level:** Text only (INFO, ERROR, etc.).
* **Interaction:** "Open details" form button activated by Enter. Focus returns to the originating button or `#results-heading` on Back.

#### 3. The Detail Panel
Wide panel; full-width document-flow view on narrow screens (not a modal). Old results are hidden/inert while open; restored on Back. No-JS detail is a full page.

```text
< Back to results    [ Event Details ]    [ Copy JSON ]

Event Time:   2026-01-02 03:04:05.123456 (UTC)
Received:     2026-01-02 03:04:06.000000 (UTC)
Severity:     INFO (9)
Source:       demo-api (staging)

Attributes:
[ task ]
    [ attempt ] 1    (integer)  [ Filter ]
    [ status ] "done" (string)  [ Filter ]
```
* **Copy JSON:** Copies redacted JSON to clipboard (JS-enhanced only). Announces "JSON copied" on success; "Copy unavailable; select JSON below" on failure. Fallback is escaped, redacted, selectable `<pre>`.
* **Filter:** Adds a predicate using a JSON array path (e.g., `["task", "attempt"]`). v1 supports only `op: "eq"` (with type/value) and `op: "missing"` (no type/value). Null is `eq` with null value (present null). Applying Filter submits a new typed search against the frozen window, resets cursor, closes/invalidates the detail panel (removing the Back control), and focuses `#results-heading`. Fresh Search evaluates relative range now (default 15m). Older freezes `[from,to)` absolute. Saved views store relative selection, not frozen window. Action is "New search", never "New".

## Interaction, Keyboard, and Accessibility
* **Tab Order:** `#source-select` → `#time-select` → `#severity-select` → `#msg-term-1`…`#msg-term-N` → `#btn-add-term`/`#btn-remove-term-N` → `#pred-1`…`#pred-N` → `#btn-add-pred`/`#btn-remove-pred-N` → `#btn-search` → `#btn-clear` → Result rows (`<a>` or `<button>`) → `#btn-older` → `#btn-new-search` → `#btn-back` → `#btn-copy` → `#btn-filter` → `#btn-show-full`.
* **Focus Targets:** Headings (`#results-heading`, `#detail-heading`) are `tabindex="-1"` only. Search focuses `#results-heading`; Detail focuses `#detail-heading`; Errors focus `#status`. Closing Detail returns focus to the originating Open details button (fallback `#results-heading`). Arrows/Escape are optional enhancements; No-JS uses native Tab/Enter/forms/links.
* **No-JS:** POST detail forms carrying opaque ID + bounded context + CSRF. POST Search/Back/Filter/Older with bounded context + CSRF. Clipboard fallback: escaped redacted `<pre>` selectable text.
* **Hostile Strings:** Rendered via `html/template` escaping. No raw HTML.
* **Accessibility:** At 320px/200% zoom, source sidebar stacks above full-width results/detail; controls visible. `#btn-show-full` labeled "Show full" expands wrapping text/pre, breaks long tokens, no hover-only. No-JS exposes full escaped values via native `<details>`/`<summary>` or always-visible `<pre>`.

Typed values use distinct badges for missing, null, boolean, integer, real, string. `true`/`1`/`1.0`/`"1"` are never equated. Decimal int64 is preserved exactly without JS Number rounding; real float64 preserves signed-zero type. Containers and exotic paths are viewable but have a disabled Filter button with copy "Unsupported path". Segment limits are in NATIVE-V1.md.

## Time and Paging Semantics
* **Timezone:** Default UTC.
* **Window:** `[from, to)` absolute. Frozen for paging.
* **Keyset Paging:** "Older" and "New search" POST forms. No OFFSET.

## Deliverable Mapping
* **Slice 02:** Walking skeleton. Source/time/severity/message → synthetic result → escaped detail.
* **Slice 06:** Final interface. Typed attributes, filter-on-value, keyset paging, keyboard navigation.
* **Slice 07:** Bounded Live view, distinct from historical search, with pause/gap states.

## Design Constraints
* **No Query DSL:** Users select fields/values; no SQL/logql.
* **No External Assets:** Server-rendered HTML, self-hosted htmx/CSS.
* **Separation:** This document defines search/inspect flow. Live and transport are separate.