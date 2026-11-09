# Slice 02: Local Walking Skeleton

## Scope
Local Go + SQLite + htmx skeleton. No remote exposure, no real data, no production deployment.
This slice implements the experimental skeleton protocol (version 0) at `/api/experimental/skeleton/batches`. The canonical native-v1 route remains unavailable and returns 404 until fully conformant. See [SLICE02-SCOPE.md](SLICE02-SCOPE.md) for detailed scope boundaries.

## Prerequisites
- Go 1.27.3
- GCC 14.2.0 (Linux amd64)
- Node.js >= 20 (for unit fixtures only, not browser)
- `CGO_ENABLED=1`
- Build tags: `fts5`, `sqlite_omit_load_extension`
- Dependency: `github.com/mattn/go-sqlite3 v1.14.53` (bundled SQLite 3.54.1)
- htmx 2.0.11 (self-hosted, upstream https://github.com/bigskysoftware/htmx/tree/v2.0.11, hash d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717, license internal/server/web/HTMX-LICENSE.txt)

## Setup

```bash
# Verify module integrity
go mod verify

# Create private directories outside checkout
umask 077
BUILD_DIR=$(mktemp -d)
STATE_DIR=$(mktemp -d)
chmod 700 "$STATE_DIR"

# Generate private canary file (crypto-random or placeholder)
openssl rand -hex 16 > "$STATE_DIR/canary.txt"
```

## Build

```bash
# Build server
CGO_ENABLED=1 go build -tags "fts5 sqlite_omit_load_extension" -o "$BUILD_DIR/hearthlog" ./cmd/hearthlog

# Build synthetic emitter
CGO_ENABLED=1 go build -tags "fts5 sqlite_omit_load_extension" -o "$BUILD_DIR/synthetic" ./cmd/synthetic
```

## Test

```bash
# Go tests with race detector
CGO_ENABLED=1 go test -race -count=1 -tags "fts5 sqlite_omit_load_extension" ./...

# Go vet
CGO_ENABLED=1 go vet -tags "fts5 sqlite_omit_load_extension" ./...

# Node unit fixtures (not browser)
node --test internal/server/web/app.test.js

# Secret scanning (redacted output)
gitleaks dir --redact --no-banner
gitleaks git --redact --no-banner
gitleaks git --pre-commit --staged --redact --no-banner
```

## Run

```bash
# Start server (generates read.token/write.token 0600 in STATE_DIR)
"$BUILD_DIR/hearthlog" -state "$STATE_DIR" -listen 127.0.0.1:8787 &
SERVER_PID=$!

# Wait for readiness
curl --retry 5 --retry-connrefused --retry-max-time 10 -sf http://127.0.0.1:8787/health

# Emit exactly 100 synthetic events
"$BUILD_DIR/synthetic" \
  -endpoint "http://127.0.0.1:8787/api/experimental/skeleton/batches" \
  -write-token-file "$STATE_DIR/write.token" \
  -canary-file "$STATE_DIR/canary.txt"

# Stop server
kill -SIGTERM $SERVER_PID
wait $SERVER_PID

# Restart with SAME state directory (no reseed)
"$BUILD_DIR/hearthlog" -state "$STATE_DIR" -listen 127.0.0.1:8787 &
SERVER_PID=$!
curl --retry 5 --retry-connrefused --retry-max-time 10 -sf http://127.0.0.1:8787/health
```

## Verify

1. **Count**: In the UI search form, explicitly enter source `src_demo_api` and term `slice02synthetic`; expect exactly 100 rows.
2. **UI Login**: Use READ token in password field (no secrets in URL).
3. **Search**: 15m window, source `src_demo_api`, term `slice02synthetic`.
4. **Typed Hostile Data**: Verify `<script>alert(1)</script>` renders as literal text, not executed.
5. **Canary Absence**: Confirm canary value absent from wire, store, FTS, HTML, diagnostics.
6. **Auth**: Unknown credentials return 401.

## Evidence
- Automated: file-backed SQLite migration/ingest/search, race, HTTP integration, and Node unit fixtures via pinned local commands.
- Browser (source tree 9b60679, pre-current accessibility revision): JS-disabled authenticated search/detail/back 200 DOCUMENT with filters preserved; JS-enabled htmx search/detail 200 XHR, Enter/Back, clean console; inert literal hostile text; raw typed attributes; sanitized synthetic screenshots; 320px wrap/no overflow; separate 200% zoom pass (not combined with 320px).
- Local post-#16 QA (current revision `dbe3937`): see [SLICE02-LOCAL.md](evidence/SLICE02-LOCAL.md). Reproduced the automated gates, the live browser pass (login → 100-row search → detail → Back-origin focus → hostile-data escaping → combined 320px@2× → controlled 401 safe-status denial), the protocol checks (404/401/200/501), persistence across restart, and canary absence across store/FTS/HTML/diagnostics.
- Not run / not claimed: remote CI (pre-merge gate, Linux amd64 target); Live (C8) and skeleton-unreachable states (503/429/504/410, multi-source empty) — out of Slice 02 scope, deferred to Slices 06–07. See the Limitations section of the evidence file for the two items needing a focused-window confirmation (skiplink live `:focus` reveal; narrow-viewport via emulation rather than a physical display).
- Canary secret absent from store/FTS/HTML/diagnostics (tested; wire carries the pre-transmission value by design).

## Experimental Skeleton Protocol (Version 0)

- **Input**: `version: 0` in the batch envelope. Canonical native-v1 requests are rejected with 404.
- **Receipt**: `protocol: "experimental-skeleton"`, `version: 0`. `received_at` is the exact stored UTC microseconds assigned in the transaction. No `replay_until` field is emitted.
- **Replay**: Repeated batch IDs return 501 with `replay_not_supported`. Clients must not retry or re-batch.
- **Materialization**: Search results exceeding 2 MiB return 422. No truncated 200 responses.
- **Native-v1**: The original NATIVE-V1 contract is not amended. Replay semantics remain deferred (04).

## Contracts
- [NATIVE-V1.md](contracts/NATIVE-V1.md)
- [SECURITY-V1.md](contracts/SECURITY-V1.md)

## Non-goals
No remote exposure, real apps, OIDC, production deployment, or complete retry semantics.