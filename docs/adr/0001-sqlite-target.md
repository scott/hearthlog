# ADR 0001: low-volume SQLite MVP, bundled driver and narrow initial target

Status: proposed for independent technical review. Scope: Slice 00 contracts
and capability requirements, not an application release or deployment.

## Decision and rationale

Keep Go net/http + html/template + embedded, self-hosted pinned htmx 2.x/CSS.
No frontend build system, copied branding or separate database server. SQLite
is intentionally a low-volume single-node store, not a temporary imitation of
PostgreSQL. WAL/FULL with one bounded writer, four bounded readers and one
separate online-backup source connection is proportionate to that scope.
No shared-cache/network filesystem, active-active nodes or extension loading.
htmx asset exact patch/license/hash will be pinned when assets are introduced;
no assets or runnable application are added in this slice.

Pin `github.com/mattn/go-sqlite3 v1.14.53`, using its **bundled** amalgamation,
`CGO_ENABLED=1`, build tags `fts5 sqlite_omit_load_extension`. Do not use the
`libsqlite3` tag/system SQLite or silently substitute driver/version. Module
checksums from the local downloaded module:

```text
module h1:mnTM0owzUMrKuLo+0w51NUiB0ir2Fo7kFXlPt/l3BGs=
go.mod h1:6JTjA44L93a0QCyJef5YvlPoKXntQPjzWv5gtm9sB6w=
```

Actual probe reports **SQLite 3.54.1**, not a release-page summary's 3.54.0;
`sqlite_source_id()` is recorded in [probe evidence](../evidence/SLICE00.md).
Official [WAL documentation](https://sqlite.org/wal.html#walreset) identifies
3.51.3 and later as containing the WAL-reset corruption fix (older documented
backports exist, but are not selected). Version/actual source ID and feature
probes, not driver version alone, are startup/build gates. No claim of an
independent reproduction of that rare corruption race or power-loss proof.

Selected driver exposes `database/sql` context cancellation/SQLite interrupt,
`SQLiteConn.Backup` + Step/Finish, and bundled FTS5/JSON. Scratch capability
probe passed literal MATCH, typed JSON, WAL/FULL readbacks on writer and new
reader, query-only denial, interrupt + same-connection reuse and incremental
online-backup sentinel/integrity/FTS checks. These prove capabilities only,
not concurrent-write backup acceptance or completed storage behavior.

## Initially supported target contract

| Input | Initial selected and capability-tested value |
|---|---|
| GOOS/GOARCH | linux/amd64 only; no other OS/architecture support claim |
| Go | go1.27.3; `GOTOOLCHAIN=local` for reproducible checks; later go.mod/go.sum must pin dependencies and declared toolchain |
| CGO/compiler | CGO_ENABLED=1; GCC Debian 14.2.0-19, gcc 14.2.0 |
| libc/runtime | glibc Debian 2.41-12+deb13u4; native Linux ELF dynamic loader/libc on tested target; runtime ceiling/floor on other distros untested |
| SQLite | bundled 3.54.1 from driver v1.14.53, FTS5 enabled, JSON built in, dynamic extension loading omitted |
| Build tags | `fts5 sqlite_omit_load_extension`; no `libsqlite3` |
| Persistence | local durable filesystem with honest sync and ownership locks, no network filesystems; exact filesystem/device reliability remains operator decision |

No universal static binary, musl, Darwin/Windows/ARM, cross-CGO or older glibc
compatibility is promised. A real application binary is **not** built in Slice
00. Local compiler/runtime observations and the scratch probe are reproducible
evidence for the chosen initial target only. New toolchains, runtime baselines,
platforms and driver upgrades require probes plus acceptance reruns.

### Versioned binary/build/startup requirements (future runnable slices)

Before claiming a supported deployable application:

1. Commit go.mod/go.sum and embedded htmx/CSS licenses/hashes; disallow floating
   module/asset/toolchain downloads during release build. Record exact Go, GCC,
   libc, tags and all build flags. Use a clean checkout of an exact commit,
   `GOTOOLCHAIN=local`, CGO=1, selected target, module verification, trimpath and
   fixed version/commit/UTC build metadata (no wall-clock variation).
2. Implement actual version variables/command and binary entrypoint before
   publishing an application build command. Run repository tests and two clean
   builds with identical inputs; compare bytes, explain any nondeterminism.
   Produce a versioned target-specific artifact + SHA-256 checksum manifest.
   Verify binary metadata/checksum and dynamic runtime dependencies; never
   infer static packaging from Go alone.
3. Startup must take ownership lock (second process fails), initialize/read back
   WAL, FULL, foreign_keys, busy_timeout, locking_mode, auto_vacuum and checkpoint
   settings on appropriate fresh connections; query_only on readers. Validate
   sqlite_version/source/compile options, FTS5/JSON behavior and migration
   compatibility before accepting work. Unknown PRAGMAs can be silent no-ops.
   Report safe version/features, never secrets/payloads/paths in public evidence.
4. Startup readiness fails closed for unsupported SQLite, missing features,
   wrong schema/runtime/storage settings, ownership conflict or inadequate
   disk reserve. Health/version output must be nonsecret. Restore rotates
   cursor epoch and verifies watermark/identity state; never guesses MAX(logs).
5. Generic Markdown-only installation/supervision/TLS/OIDC/source lifecycle,
   upgrade/preflight snapshot/migration/stopped-service rollback, online backup
   and isolated recovery guides must accompany the versioned binary. Integration
   guide must compile the bounded redacting slog example and state only tested
   OTLP logs compatibility. Final Slice 11 independently proves clean install,
   demo-service emit/search/detail/Live/restart persistence and recovery using
   only guides. These are remaining twelve-slice requirements, not current passes.

## Small SQLite package boundary (not a backend framework)

Proposed package `internal/storage/sqlite`; handlers/domain/SDK do not import
SQL drivers, PRAGMAs or FTS syntax. Domain validates native/query values and
passes context plus an already authenticated immutable workspace/source scope.
No exported method accepts an optional workspace or raw SQL from a handler.
Storage owns SQL, connection setup, transactional receipt race checks, bounded
writer scheduling, index maintenance, watermark, epoch and backup lifecycle.

| Focused operation | Typed input/result obligation |
|---|---|
| CommitBatch | workspace/source, validated batch+canonical hash; recheck source state at execution; return durable receipt or typed conflict/busy/uncertain; all atomic |
| ReadPage / ReadDetail | mandatory workspace + permitted sources + typed validated filters/cursor; bounded materialized rows; no sql.Rows or DB transaction escapes |
| ReadLive | authenticated scope/filter/generation + horizon; bounded rows + highest examined position or explicit gap |
| Source/session/view mutations | mandatory workspace/operator scope where needed; same writer scheduler; revocation order authoritative |
| Retain / Checkpoint | bounded scheduler maintenance class; scoped deletes/FTS; retain watermark and >=48h receipts; no sidecar deletion |
| OnlineBackup / RestoreValidation | single bounded backup, completion evidence, isolated validation and epoch rotation; no open-file copy |

Types/errors stay small and concrete; no generic Store interface, factory,
plugin registry, second store, dual write or migration implementation now.
Cancellation must interrupt SQLite and close rows before releasing slots. One
unresolved implementation risk: the selected driver's exported API has no
proven VM-step progress-budget hook. Slice 05 must demonstrate the initial
100,000-step budget with a reviewed bounded driver integration, **or stop for
an explicit architecture decision**; a context timeout alone does not meet the
VM-step requirement. Do not silently weaken the gate or raise the budget.

Future store changes preserve immutable ingest IDs, event IDs, workspace/source registrations,
native batch IDs, canonical hashes/numeric subtypes/UTC microseconds, receipt
bodies/lifetimes and versioning. In deliberate stopped/export/reimport review:
compare per-workspace/source counts and full IDs, payload/type/hash equality,
ordering and highest committed watermark (including after empty retention),
receipt replay/conflict/expiry behavior and authorization/session/token state;
rotate store epoch to invalidate cursors; prove rollback/recovery. No migration
code is authorized by this recipe.

## Required baseline versus optional diagnostics

Required provisional synthetic baseline (not observed customer workload):
50,000 preloaded events, three generic sources, ~1KiB average, 5/s for 30min,
50/s for 10s, one historical and one Live reader. Record actual CPU/RSS/disk,
accepted/retried/rejected/dropped counts, query plans/latencies, WAL/checkpoints,
retention/backup ages and five deterministic crash/replay cycles. PLAN.md's
handler p95 <1ms, first page p95 <1s, every query <=5s, Live p95 <2s, outage
RSS delta <64MiB, cleanup lag <=10min and checkpoint idle catchup <=10s
remain required. Snapshot completion <=5min, conservative verified off-host
capture-start age <=2h and intended RPO <=2h/RTO <=60min are provisional
measured recovery gates, not guarantees; no verified destination means unready.
All security/acknowledged-data integrity checks remain mandatory.

Optional only: 1M/10 sources, 100/s for 30min, 1,000/s for 60s, two readers,
20 crash/replay cycles and larger maintenance/recovery characterization.
Neither has been run in Slice 00. Optional stress failure does not waive safety
or baseline failure, and optional success is not required for a small healthy MVP.

PostgreSQL review triggers (measure normal load, exclude intentional stress):
- A second active node, shared remote DB or HA requirement.
- Three representative 15min windows with >1% ingest busy/admission rejection
  or writer queue p95 >100ms despite bounded tuning.
- Three windows with representative query p95 >1s or >1% budget rejection
  despite measured index/window tuning; checkpoint read pauses >1/min for 15min.
- Retention lag >10min, recurring checkpoint lag >30s, projected seven-day
  store+index+WAL+backup working space >70% of provisioned disk, or verified
  recovery capture-age/RPO fails twice consecutively after bounded remediation.
- Measured operational/recovery requirements exceed single-node snapshots.

Collect plans, counts, latency/resource/recovery data; compare bounded tuning
and capacity changes to a proposed small PostgreSQL benchmark and obtain a new
accepted ADR. These triggers neither implement nor automatically choose it.

## Decisions still requiring operator/reviewer input

- Actual event volume/burst/size and pre-ack loss tolerance; baseline is synthetic.
- Hosting resource/storage filesystem/sync assurances and disk reserve; no paid
  host, external backup account or real endpoint provisioned.
- OIDC issuer/audience/redirect, workspace membership/access and TLS ownership.
- Real redaction policy, retention/legal deletion needs and encrypted backup
  destination/key custody/expiry/recovery credential rotation; no secrets sought.
- Validate actual glibc runtime/target on intended host; additional platforms
  are unsupported until independently tested.
- Review this v1 canonicalization/numeric/path restriction contract; implement
  golden vectors in Slice 04; resolve VM-step hook feasibility before Slice 05.

The narrow target/driver are selected here; deployment-specific answers remain
open and do not authorize real-data adoption. Plan approval is not a code test.
