# Hearthlog

A planned self-hosted structured-log investigation tool for Go applications.

SQLite intentionally keeps a low-volume single-node MVP simple. PostgreSQL can be evaluated later if measured workload or operating requirements justify it; no second backend is planned for the MVP. Small baseline acceptance and optional stress diagnostics are separated in the plan.

**Stack:** Go, SQLite, htmx. One Go service renders HTML and stores logs in a local SQLite database with FTS5 search. This repository's planning documents describe intended behavior, not a completed application.

- [Brief](docs/BRIEF.md)
- [Implementation plan](docs/PLAN.md)
- [Implementation slices and GitHub issues](docs/SLICES.md)
- [Slice 00 native v1 contract](docs/contracts/NATIVE-V1.md)
- [Security/public-content contract](docs/contracts/SECURITY-V1.md)
- [SQLite/target ADR](docs/adr/0001-sqlite-target.md)
- [Slice 00 capability and review evidence](docs/evidence/SLICE00.md)
- [Investigation Flow](docs/ui/INVESTIGATION.md)
- [Slice 02 scope and local run/test guide](docs/SLICE02.md)
- [Slice 02 scope notes](docs/SLICE02-SCOPE.md)
- [Go slog SDK integration guide (Slice 03)](docs/SDK.md) and [evidence](docs/evidence/SLICE03.md)
- [Slice 04 single-writer replay evidence](docs/evidence/SLICE04.md)

The final twelve-slice deliverable must include a versioned binary for declared tested targets, reproducible build and checksum verification, generic deployment/upgrade/backup/recovery instructions, and a compilable synthetic service-integration guide. Fresh installation and demo-service search/Live/restart persistence must be proven using only those instructions. This is planned acceptance, not an available release or universal static binary claim.

The investigation workflow takes inspiration from Better Stack Telemetry while using original UI design. Logs only: no metrics/traces platform, billing or public signup.

Examples and test evidence must be synthetic. Do not contribute real application configuration, credentials, environment files, database snapshots or production log captures. Runtime secrets and deployment-specific settings stay outside version control. No run/install instructions are claimed until a runnable slice exists.
