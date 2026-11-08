# Security and public-content contract v1

No authentication implementation exists yet. Synthetic loopback work uses separate
read/write credentials; remote or real logs are forbidden until TLS, verified
OIDC code+PKCE, session and isolation gates in PLAN.md pass. This is a single
operator's host, not database-enforced hostile-tenant isolation.

## Threat / authorization matrix

| Principal/input | Allowed | Required denial/check, including all secondary paths |
|---|---|---|
| Anonymous | Static nonsecret assets, generic health without internals | Full pages, partials, search/detail, source admin, views, Live, receipts denied |
| Source write token | Native batch only for bound registered source/workspace | No reads/session/admin; body/query cannot choose workspace/source/service/environment; hashed token, constant generic errors, no URL tokens |
| Reader session in workspace A | All sources in A, search/detail/Live/saved own views | No source lifecycle/member admin, no B access even with known IDs/cursors; read session cannot ingest |
| Operator session in A | A source create/rotate/revoke and membership policy | No B operations; state changes require session-bound CSRF + Origin validation |
| Valid source in B | B writes only | Reusing A batch ID does not expose A receipt or dedupe across scopes |
| Rotated/revoked/expired credentials | Explicit overlap only until revoke/expiry | Recheck every page/poll and writer execution; queued jobs cannot bypass revocation; auth DB overload fails closed |
| Cursor / HX-Request / guessed row ID | Routing hints only | Not authorization; signed cursor scope+epoch+expiry checks plus membership/source recheck on each request |
| Message, attrs, query text | Escaped data only | html/template everywhere; never template.HTML/JS/URL; no hx/data-hx/template injection; literal quoted FTS, bound path/value parameters, no raw SQL |
| Untrusted client/resource pressure | Bounded input/admission/readers/Live | Body/decompression caps, per-source bounded rate buckets, writer/read/VM/deadline limits; no unbounded labels/queues/retries |
| Database/backup file reader | Operator/service filesystem boundary only | Private directory/files incl sidecars and snapshots; no DB download; encrypted volume/backup for sensitive use; SQLite not encrypted by default |
| Browser/cache/history | Session-bound same-origin pages | no-store, Vary HX-Request, no-referrer, htmx history disabled, strict CSP, no external htmx/CDN/eval/inline handlers, stale generation discarded |
| SDK redaction failure/free text | Already-redacted bounded events only | Recursive secret-key stripping/default-deny bodies/headers/arbitrary objects, safe hooks/counters; fail closed, no payload stderr; free text is not guaranteed PII-free |

Every storage SELECT/JOIN/UPDATE/DELETE requires authenticated workspace scope,
including FTS external-content join, source admin, views, retention and Live.
Read-only methods also apply authorized sources and receipt-age expiry. No
workspace is inferred from a row ID or supplied by an untrusted request.

Negative fixtures use `ws_demo_a`, `ws_demo_b`, `src_demo_api`, `src_demo_worker`
and generic staging registrations. Test each matrix row through full page,
partial, detail, FTS, cursor, Live and state-change paths where applicable.
CSRF covers ordinary forms **and** htmx. Canary redaction tests must prove
absence on wire/fallback/DB/FTS/HTML/diagnostics/snapshot/evidence; security must
not weaken to meet low-volume performance gates.

## Public artifact allowlist and gate

Allowed: source/tests in later slices; generic deterministic synthetic fixtures;
Markdown planning/contracts/ADRs/guides with `<INGEST_TOKEN>`, `<PUBLIC_ORIGIN>`,
`<OIDC_ISSUER>`, `<STATE_DIRECTORY>` placeholders; sanitized capability/count
outputs; dependency locks; pinned self-hosted assets with license; ignore rules.
Do not create settings files as examples: all operational configuration examples
are Markdown-only placeholders. No environment files, even fake ones.

Forbidden: credentials/private keys/token material, real configurations/endpoints,
private identities/paths/hostnames, personal data, real logs/captures, DB/WAL/SHM,
backups/dumps, environment files, signed URLs, diagnostic payloads. Screenshots
and command output require manual review too. `.gitignore` reduces accidents;
it is not protection for already tracked or force-added content.

Before **every** commit/publication and before the first runnable slice:

1. Inventory `git status --short`, `git diff --cached --name-only`; manually
   classify every file against allowlist, not just secret regex matches.
2. `git diff --cached --check` and review **entire** staged diff, including docs
   and evidence. Reject real operational data even if no secret detector fires.
3. Run pinned `gitleaks 8.30.1` (actual version in Slice 00 evidence), using
   `gitleaks git --pre-commit --staged --redact --no-banner` for staged additions and
   `gitleaks git --redact --no-banner` for history. Nonzero exits block work;
   no blanket exclusions/baselines or suppressions to make a failing gate pass.
   If scanner version changes, record and retest it explicitly.
4. Prove scanner detects a generated synthetic canary in a disposable directory,
   not in the repository or real account; scanner output must remain redacted.
5. Verify ignore patterns with `git check-ignore --stdin` for environment,
   database/sidecar/backup/log/key/capture/build paths. Check tracked files
   separately; ignored does not mean approved.
6. Repeat gate after any edit. Independent review of exact commit precedes
   merge. A later automated CI gate is required before runnable code is merged;
   this docs-only slice establishes and exercises the local gate, not existing CI.

A clean scan is evidence only of the scanner's rules, not proof of no sensitive
data. Never publish raw scanner findings; remediate locally. Runtime and
credential files must never be used as fixtures. Review outgoing commit
contents/messages and public branch names as well as the final tree; fixing
a file does not remove private metadata from an earlier commit.
