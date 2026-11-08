# UI Fixture Projections

**Context:** Stored-result projections for `docs/ui`. These are deterministic, synthetic data. **No live ingestion, real clock changes, or runtime execution occurs.**
**Clock:** Fixed at `2026-01-02T03:15:00Z`.
**Window:** `[2026-01-02T03:00:00Z, 2026-01-02T03:15:00Z)`.
**Timezone:** UTC.

## Fixture A

**Source:** `src_demo_api`
**Service:** `demo-api`
**Environment:** `staging`

```json
{
  "ingest_id": "1",
  "event_time": "2026-01-02T03:04:05.123456Z",
  "received_at": "2026-01-02T03:04:06Z",
  "severity_number": 9,
  "severity_text": "INFO",
  "message": "synthetic task completed",
  "attributes": {
    "task": {
      "attempt": 1
    },
    "real": 1.0,
    "string": "1",
    "boolean": true,
    "present": null,
    "exact_large_integer": 9007199254740993,
    "tags": ["demo"],
    "x.y": "value_with_dot",
    "hostile_markup": "<div hx-get=\"/evil\">data</div>"
  }
}
```

*Note: `missing_field` is intentionally omitted. The `hostile_markup` is a safe string literal; `hx-get` is escaped data, not active HTML. Zero script execution or network requests.*

## Fixture B

**Source:** `src_demo_worker`
**Service:** `demo-worker`
**Environment:** `staging`

*Described prose for brevity:*
`ingest_id` "2", `event_time` `2026-01-02T03:04:04Z`, `received_at` `2026-01-02T03:04:06Z`. Severity `ERROR` (17). Message: "failed to process job42". Attributes minimal.

## Expected Type Cases

The UI must distinguish distinct values that may appear equal in simple text:
- `null`: Present JSON null value.
- `true`: Boolean.
- `1`: Integer.
- `1.0`: Float/Double.
- `"1"`: String.

**Int64 Handling:** `9007199254740993` exceeds JS `Number.MAX_SAFE_INTEGER`. It must remain an exact integer string/number, **not** rounded to `9007199254740992`.

## Path & Query Support

- **Supported:** Path segment arrays like `["task", "attempt"]` navigate nested objects.
- **Unsupported/View-Only:** Literal keys containing dots as part of the name (e.g., `x.y`) or array indices (e.g., `tags[0]`) are **not** supported for query filtering. They are view-only metadata.

## Security & Markup

All user-provided strings, including `hostile_markup`, must be **escaped** during rendering. No HTML injection, no script execution, and no attribute-driven requests (e.g., `hx-get`) from fixture data.

## Query Examples

- **Match A:** Source `src_demo_api` + Min Severity `INFO` + Term `synthetic` → Returns Fixture A.
- **No Match:** Term `nomatch` → Returns "No matching events".

**Empty Workspace:** Onboarding state is a separate scenario; do not simulate full-count-all queries for empty states.

## References

- [NATIVE-V1.md](../contracts/NATIVE-V1.md)
- [ACCEPTANCE.md](ACCEPTANCE.md)
- [INVESTIGATION.md](INVESTIGATION.md)