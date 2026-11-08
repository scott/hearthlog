# Native contract v1 (Slice 00)

Status: proposed implementation contract for independent Slice 00 review. No endpoint,
SDK, schema or application exists. All names and data here are synthetic. This is
not a claim that any application test passed. Breaking wire or semantic changes
require a new version; unknown fields are errors, not silently ignored.

## Envelope, validation and limits

Future native route: `POST /api/v1/batches`, JSON UTF-8 only, authenticated with a
write-only source credential in `Authorization: Bearer <INGEST_TOKEN>`. No token
in query parameters. Source and workspace come only from that credential.

```json
{
  "version": 1,
  "batch_id": "00000000-0000-4000-8000-000000000001",
  "events": [{
    "event_id": "00000000-0000-4000-8000-000000000002",
    "event_time": "2026-01-02T03:04:05.123456Z",
    "severity_number": 9,
    "severity_text": "INFO",
    "message": "synthetic task completed",
    "attributes": {"task": {"attempt": 1}, "ok": true, "duration_ms": 1.5, "optional": null, "tags": ["demo"]},
    "trace_id": "11111111111111111111111111111111",
    "span_id": "2222222222222222"
  }]
}
```

| Field/resource | v1 rule (provisional resource targets) |
|---|---|
| Envelope | Required version=1, batch_id, nonempty events; no other fields |
| Batch/event IDs | Lowercase canonical UUIDv4, 36 ASCII bytes; batch ID stable across retries; event IDs immutable, unique **within** batch, not globally deduplicated |
| Event fields | All shown required except trace_id/span_id; attributes must be object; empty object/message allowed |
| Time | RFC3339 with explicit Z/offset; 0–6 fractional digits; parse exactly to UTC signed integer microseconds, no rounding/leap seconds; format response UTC Z; offset spelling does not affect identity |
| Time admission | event_time <= server validation time +5m and >= time minus configured event horizon (default 7d); capture validation time once/batch; inclusive bounds |
| Receipt time | Server UTC microseconds assigned in transaction; retention uses this, not event clock; default receipt-age retention 7d |
| Severity | Integer 1–24; text canonical TRACE (1–4), DEBUG (5–8), INFO (9–12), WARN (13–16), ERROR (17–20), FATAL (21–24); mismatch rejected; SDK maps slog levels explicitly later |
| Correlation | trace_id 32 / span_id 16 lowercase hex, not all zero; span requires trace; log metadata only |
| Message | UTF-8 string <=16KiB; event total <=32KiB minified UTF-8 JSON; no trusted markup |
| Attributes | <=128 total object members recursively; depth <=8 (root attributes depth 1, each nested container +1); arrays <=128 elements/container; key <=128 UTF-8 bytes; scalar string <=8KiB |
| Numbers | Lexically integral tokens (no dot/exponent) must fit signed int64; fractional/exponent tokens finite float64; reject overflow/underflow-to-zero of nonzero input; no NaN/Infinity, no silent int->float conversion |
| Parser | Reject duplicate object keys at **every** level after JSON unescaping (including equivalent escaped keys), malformed UTF-8, unpaired surrogate escapes and trailing bytes; RFC8259 grammar; no Unicode normalization |
| Native batch | <=200 events AND <=256KiB decompressed request bytes including envelope/whitespace; <=256KiB compressed bytes too; bounded streaming gzip or identity only; reject excess before allocation/admission |
| Server-owned keys | Reject top-level workspace_id, source_id, received_at, ingest_id, service, environment; same names reserved at attributes root, never override registration; nested values remain ordinary data |
| Native retry | Same batch/payload for <=60s; at most 2 requests in flight/client; server request budget 5s, admission <=100ms; no rebatching after ambiguity |

JSON strings/array ordering and numeric subtypes are preserved. Missing, null,
boolean, integer, real and string are not interchangeable. Reject input that
cannot roundtrip without changing a type/value. Integer -0 normalizes to 0;
float signed zero remains real and preserves its sign. Server filters must use
`json_type` alongside `json_extract`; bool true must not equal integer 1.

### Canonical batch identity

Validate then hash SHA-256 of the UTF-8, whitespace-free canonical JSON encoding
of this tagged representation (ordinary JSON structural punctuation; strings
escaped with only quote/backslash and U+0000–001F as lowercase `\u00xx`; other
Unicode encoded directly as UTF-8; no HTML escaping):

- Root `["native",1,batch_id,[event,...]]`; event fixed array
  `[event_id,utc_microseconds_as_decimal_string,severity_number,severity_text,message,attributes,trace_or_null,span_or_null]`.
- Attributes recursively tagged: `["object",[[key,value],...]]` with keys sorted
  by UTF-8 bytes; `["array",[value,...]]` preserves order; `["null"]`,
  `["bool",true]`, `["string",value]`, `["int","<base10>"]`,
  `["real","<16 lowercase hex digits of IEEE754 binary64 bits>"]`.
- No leading plus/zeros in canonical integers; numeric real lexical variants
  with identical float64 bits canonicalize identically. Event order matters.
  Omitted optional correlation fields canonicalize to null; explicit null input
  is rejected for those string fields. Batch identity includes batch_id/version.

Example attributes `{"b":true,"a":1}` canonicalize to
`["object",[["a",["int","1"]],["b",["bool",true]]]]`.
Compare digests only within UNIQUE(workspace_id,source_id,batch_id). Hash is not
an authentication token; never accept a client-supplied hash as authoritative.
An implementation must publish digest golden vectors and permutation/float
roundtrip tests before claiming replay correctness (Slice 04).

## Atomic receipt and replay

First successful commit returns HTTP 200, and replay returns the **same body**:

```json
{"version":1,"batch_id":"00000000-0000-4000-8000-000000000001","accepted":1,"first_ingest_id":"1","last_ingest_id":"1","received_at":"2026-01-02T03:04:06Z","replay_until":"2026-01-04T03:04:06Z"}
```

Workspace/source IDs are server-issued opaque ASCII `[a-z][a-z0-9_]{0,63}`
registration IDs (synthetic examples above); they are never client scope authority.
Ingest IDs in responses are decimal strings to avoid JavaScript precision loss.
`replay_until = received_at + 48h`, minimum receipt lifetime; at the exact
boundary guarantee ends. Before then same key/hash returns original result even
if event retention has removed rows. Same key/different hash is 409, never
rewrites rows. Once cleanup expires a receipt, a replay is eligible to create
new events, subject to normal validation; retained receipts may dedupe longer.
There is no global event exactly-once guarantee. Different source/workspace or
batch ID is a different identity. Midnight has no effect.

After authentication and bounded parse/canonicalization, check an existing
receipt **before current event-time admission**, so lost acknowledgments can
replay across a horizon boundary. New batches must pass current time policy.
Recheck receipt/hash inside the writer transaction; concurrent equal replays
must not produce extra rows. Invalid syntax/types never bypass validation just
because a batch ID exists. Revoke invalidates retries as well as new writes.
No externally readable receipt lookup: replay through this scoped write route
resolves ambiguity without exposing read access.

```text
unauthenticated -> deny (no receipt existence disclosure)
authenticated -> bounded parse/canonicalize -> invalid: reject whole batch
  -> admission (64 jobs AND 16MiB); full/timeout: 503, no partial acceptance
  -> single writer BEGIN IMMEDIATE -> recheck credential scope/revocation
     -> existing receipt + equal hash: return stored receipt, no writes
     -> existing receipt + unequal hash: rollback -> 409
     -> absent: validate time -> allocate AUTOINCREMENT IDs
        -> immutable logs + transactional FTS + receipt + max watermark
        -> COMMIT FULL -> 200
any failure before commit -> rollback ALL -> no 200
commit outcome unknown / response lost -> retry same key+payload, never fresh ID
restart -> load persisted watermark, never MAX(remaining logs) or RAM counter
```

Watermark is updated in the same commit even if later retention deletes every
row. No published ID reuse; rollback allocation holes are irrelevant. Commit
order follows the single writer. Restore/store changes rotate persisted cursor
epoch and invalidate all old cursors; receipt/identity preservation is checked.
SQLite durability assumes honest fsync/storage, not protection against disk
loss; queued/in-flight unacknowledged events can be lost. OTLP does not inherit
native batch replay and may duplicate on retries.

## Versioned query, result and error shapes

Future internal query adapter (JSON representation for contract review, **not**
a promise of a new public JSON read API); ordinary HTML forms use the same typed
values. Authenticated read session, workspace from membership, never body scope:

```json
{"version":1,"sources":["src_demo_api"],"from":"2026-01-02T03:00:00Z","to":"2026-01-02T03:15:00Z","min_severity":9,"terms":["synthetic"],"predicates":[{"path":["task","attempt"],"op":"eq","type":"integer","value":1}],"limit":200,"cursor":null}
```

`[from,to)` absolute event-time window required after relative time evaluation;
max window is configured event horizon (7d default). <=32 sources, <=8 literal
FTS terms <=128 UTF-8 bytes each; AND-of-quoted-terms only, unicode61 default
case/diacritic folding (remove_diacritics=1); no raw operators/relevance/SQL.
<=8 predicates, equality only in v1. Path is 1–8 string segments, <=128 bytes
per segment and <=512 bytes total; initially accept only ASCII
`[A-Za-z_][A-Za-z0-9_]*` segments. Dots/quotes/backslashes/brackets, array indices
and exotic keys return unsupported_path (values still stored/displayed).
Predicates type null/boolean/integer/real/string; use `op: "missing"` without
type/value for missing. Null equality means present JSON null; real and integer
are distinct. Query limit is an integer 1–200 (default 200). Query input <=4KiB, result <=200 rows AND 2MiB, no count-all query.

```json
{"version":1,"rows":[{"event_id":"00000000-0000-4000-8000-000000000002","ingest_id":"1","source_id":"src_demo_api","service":"demo-api","environment":"staging","event_time":"2026-01-02T03:04:05.123456Z","received_at":"2026-01-02T03:04:06Z","severity_number":9,"severity_text":"INFO","message":"synthetic task completed","attributes":{"task":{"attempt":1}}}],"next_cursor":null,"window":{"from":"2026-01-02T03:00:00Z","to":"2026-01-02T03:15:00Z"}}
```

Result is an abbreviated synthetic projection, not the ingest event to replay.
Order `(event_time DESC,ingest_id DESC)`; keyset only, no OFFSET. First page
freezes committed watermark and receipt retention cutoff. Signed opaque cursor
binds version, store epoch, workspace, selected sources, filters, window, last
tuple, horizon, retention cutoff, and 15m expiry. Every page reauthorizes and
checks retention/epoch; invalidation requires rerun, not silent partial results.
No DB snapshot survives materialization/network rendering. 4 reader slots,
<=4 searches/workspace, 3s DB/5s HTTP, initial 100,000 VM-step budget.
Budget exhaustion returns explicit query_too_broad, not zero matches. Never
silently raise budget. Live has a separate monotonic ingest cursor, generation
and captured watermark; sparse filters advance highest examined authorized
position, with explicit gaps at scan/catchup bounds (PLAN.md).

All native errors have safe constant text and no payload/token/SQL echo:

```json
{"version":1,"error":{"code":"invalid_event","message":"Event validation failed","retryable":false,"event_index":0,"field":"attributes"}}
```

| HTTP | code | Client action |
|---|---|---|
| 400 | invalid_event / invalid_query / unsupported_path / unsupported_version | Fix input, no automatic retry; batch wholly rejected |
| 401 | unauthenticated | Stop sender/session; generic response |
| 403 | forbidden | Stop; no existence disclosure |
| 404 | not_found | Same response for absent/out-of-workspace detail/source |
| 409 | batch_conflict | Stop; never automatically invent batch ID |
| 410 | cursor_expired | Rerun historical query (also epoch/retention invalidation) |
| 413 | payload_too_large | Fix bounds; never split an ambiguous batch |
| 415 | unsupported_media | Fix encoding |
| 422 | query_too_broad | Narrow window/filter, no hidden result truncation |
| 429 | rate_limited | Retry-After integer seconds bounded by remaining retry budget |
| 503 | busy / storage_unavailable | Retry-After; replay unchanged native batch |
| 504 | query_timeout | Visible failure, narrow/retry consciously |

Only validated event_index and fixed field names may identify an invalid field.
Unknown commit outcome uses retryable 503 if a response is possible. No error
claims non-commit when it is uncertain. HTML/htmx errors preserve these meanings
without assuming htmx swaps 4xx/5xx automatically.

## Contract review cases (requirements, not application tests)

| Case | Expected decision |
|---|---|
| Duplicate escaped key / NaN / int overflow / >6 time decimals | Reject whole batch before write |
| Missing vs null vs true vs 1 vs 1.0 vs "1" | Six distinct typed cases; JSON type guard required |
| Reserved root key / malformed ID / duplicate event ID in batch | Reject; credential is sole scope authority |
| Key order / JSON whitespace / timezone spelling changes | Equal canonical payload/hash |
| Float spelling 1.0 vs 1e0 / event order change | First equal real; second hash differs |
| Same receipt across midnight / event horizon crossed on replay | Original receipt, zero new rows before replay_until |
| Lost post-commit response / concurrent replay | Original receipt only; no fresh ID/rebatch |
| Kill before commit / rollback | No receipt/log/FTS/watermark partial state |
| Retain receipt but delete all logs | Original receipt + persistent watermark retained |
| Expire receipt / new batch ID | Duplicate events possible; no exactly-once claim |
| Cross-workspace source/detail/FTS/cursor/Live | Deny/filter scoped, never existence leakage |
| Revoke after queued admission, before writer | Recheck denies; revocation effective after its ordered commit |
| Restart/restore | Committed watermark recovers; restore rotates epoch, verifies identities |

Implementation slices must convert this matrix into deterministic tests;
reviewing this document is only contract evidence.
