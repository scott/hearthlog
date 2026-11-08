# Hearthlog

A planned self-hosted structured-log investigation tool for Go applications.

SQLite intentionally keeps a low-volume single-node MVP simple. PostgreSQL can be evaluated later if measured workload or operating requirements justify it; no second backend is planned for the MVP. Small baseline acceptance and optional stress diagnostics are separated in the plan.

**Stack:** Go, SQLite, htmx. One Go service renders HTML and stores logs in a local SQLite database with FTS5 search. This repository's planning documents describe intended behavior, not a completed application.

- [Brief](docs/BRIEF.md)
- [Implementation plan](docs/PLAN.md)
- [Implementation slices and GitHub issues](docs/SLICES.md)

The investigation workflow takes inspiration from Better Stack Telemetry while using original UI design. Logs only: no metrics/traces platform, billing or public signup.

Examples and test evidence must be synthetic. Do not contribute real application configuration, credentials, environment files, database snapshots or production log captures. Runtime secrets and deployment-specific settings stay outside version control. No run/install instructions are claimed until a runnable slice exists.
