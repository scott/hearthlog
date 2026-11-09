# Slice 02 Scope: Local Walking Skeleton

## Status
**EXPERIMENTAL SKELETON** — Local-only, loopback, synthetic fixtures. Not production-ready.

## Endpoint & Protocol
- **Route:** `POST /api/experimental/skeleton/batches`
- **Protocol:** `experimental-skeleton`
- **Version:** `0`
- **Auth:** Bearer write-token (loopback only). Unknown/invalid credentials return `401`.
- **Canonical Route:** `/api/v1/batches` is **unavailable** (`404`) until full NATIVE-V1 conformance.

## Input & Receipt
- **Input:** `version: 0`, `batch_id` (UUIDv4), `events[]` (shared UUID/event layout).
- **Receipt:** `version: 0`, `protocol: "experimental-skeleton"`, `accepted`, `first/last_ingest_id`, `received_at`.
- **Not Native:** This is **not** NATIVE-V1. No `replay_until`, no canonical digest, no 48h idempotency.

## Validation & Commit
- **UUID/Event:** Shared validation logic. Event IDs unique within batch.
- **Commit:** Single-event batch commits before acknowledgment.
- **Failure:** DB failure returns `503` (retryable). **Never** false success.
- **Replay:** Repeated batch IDs (identical or different payload) return `501 replay_not_supported` (non-retryable). No automatic retry/rebatch.
- **Time:** `received_at` assigned in stored microseconds, returned as RFC3339Nano.

## Search & Rendering
- **FTS5:** Search over source/time/message tokens.
- **Projection:** Materialized projection and escaped HTML output bounded to **2 MiB**.
- **Overflow:** Exceeding 2 MiB returns `422 query_too_broad`. **No partial 200 responses.**
- **HTML:** `html/template` full page + htmx partials. No frontend bundler.

## Accepted NATIVE-V1 Constraints (Unchanged)
- **48h Canonical Replay/Goldens:** Reserved for **Slice 04**.
- **60s Client Retry Budget:** Client-side only; server does not enforce 409 cutoff.
- **No Fake `replay_until`:** Never emitted in skeleton receipts.

## Non-Goals (Slice 02)
- Full NATIVE-V1 conformance
- Live view, pagination, typed filtering
- Complete SDK with full retry semantics
- Remote authentication, OIDC, production deployment
- Atomic replay, canonical goldens

## Evidence & Review
- **Local Evidence:** See [SLICE02.md#evidence](SLICE02.md#evidence) for automated command families and browser observations.
- **Browser/Screenshot:** Sanitized synthetic screenshots captured against source tree 9b60679 (pre-current accessibility revision). Current revision requires retest.
- **Remote CI:** **NOT RUN** (pre-merge gate).
- **Review:** Independent review required before commit/publication.
- **Secrets:** Canary secret absent from wire/store/FTS/HTML/diagnostics (tested).

## Decision
This slice is an **experimental skeleton** only. It does not claim NATIVE-V1 conformance, production readiness, or future feature availability.