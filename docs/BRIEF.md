# Hearthlog brief

## Goal
A small self-hosted logs-only investigation tool for Go applications: select sources and time, search structured logs, inspect typed attributes, and follow incoming events. Better Stack Telemetry is a workflow reference, not a brand template.

## MVP rationale and evolution
Start with SQLite because the MVP is low-volume and single-node: no separate database server to operate. PostgreSQL is a later measured option if normal workload, concurrency or recovery needs outgrow this model. Keep storage functions separate from domain/HTTP code, preserve IDs and wire semantics, but build no second backend or generic backend framework now. Baseline validation is small and mandatory; larger stress characterization is optional, not a prerequisite for a healthy low-volume MVP.

## Initial stack
Go HTTP service and html/template rendering; embedded self-hosted htmx and CSS; SQLite with WAL, FULL synchronous commits, FTS5 message search and validated TEXT JSON attributes. One service process, one local persistent database, bounded single writer and reader pool. No separate frontend build system.

## Acceptance
A synthetic Go app emits already-redacted events via a bounded asynchronous slog handler. Authenticated native batches commit atomically with receipts and become searchable in the HTML interface. Replay is idempotent per native batch for 48h; pre-ack crash loss and OTLP duplicates are explicit limits. Workspace/source isolation, bounded query/Live resources, retention, checkpoint pressure and online backup/restore have reproducible evidence before adoption.

## Final twelve-slice deliverable
A versioned deployable binary for declared tested targets, reproducible build/toolchain/CGO/SQLite-feature instructions and checksums; generic deployment/supervision/upgrade/migration/rollback/backup/restore instructions; a compilable synthetic Go slog service-integration guide and only verified logs-only OTLP subset guidance. An independent clean install using only these docs must integrate a demo service, emit/search/Live a redacted event, restart and prove persistence, verify recovery and safe disable. No universal static/platform support or existing build is claimed.

## First vertical slice
Synthetic local Go app → authenticated batch → SQLite commit/FTS → searchable HTML/htmx results and typed detail. Separate read/write credentials and loopback binding. Demonstration is not remote-readiness or production approval.

## Delivery
12 unassigned slices with dependencies, checkable acceptance, tests and sanitized evidence. Plan approval precedes implementation. No metrics/traces product, billing/signup, alert engine, dashboards, distributed deployment, arbitrary SQL, client durable spool or compliance-archive guarantees.

## Open deployment decisions
Actual volume/bursts/size, supported Go versions and log libraries, hosting/resource budget, identity/access policy, retention/recovery needs, redaction policy and tolerance for unacknowledged loss. Numeric gates in PLAN.md are provisional targets, not claims about a current workload.

## Public-safe material only
Use generic synthetic examples and placeholders. Never include real app configuration, identities, endpoints, credentials, environment files, databases, backups or unredacted logs in source, issues or evidence.
