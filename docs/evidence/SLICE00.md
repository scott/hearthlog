# Slice 00 reproducible evidence and decision checklist

Capability checks recorded November 7, 2026 (MST). All diagnostic inputs are
synthetic; target, dependency versions, reproducible commands and limitations
are documented below.

## Acceptance map

| Issue #1 checkbox | Artifact / review evidence |
|---|---|
| v1 event/batch/query/error, bounds, receipt and threat matrix | [Native contract](../contracts/NATIVE-V1.md), [security matrix](../contracts/SECURITY-V1.md); JSON parse/link checks below; duplicate/type/reserved/ack/expiry/recovery review table |
| SQLite rationale, driver/CGO/features, Go/htmx | [ADR 0001](../adr/0001-sqlite-target.md); pinned driver, runtime readbacks + probe below |
| Required baseline vs optional stress, measured review triggers | ADR baseline and PostgreSQL sections; no workload/performance measurements claimed |
| Public allowlist/no env/ignore/scan gate | Security policy + root .gitignore; positive-control/ignore/staged/history commands below |
| Small SQLite boundary and invariant-preserving future change recipe | ADR operation table and epoch/identity/replay verification; no second backend or migration code |
| Declared target/runtime and build/version/checksum/startup contract | ADR target table and five future release gates; scratch build/linkage evidence below, not an application release |

These contract/specification artifacts require independent technical review.
No claim of API/auth/durability/performance/clean-install passes.

## Reproduce the capability check outside the checkout

This standalone **diagnostic snippet in Markdown** is not application source or
a server. Run only in a new scratch directory, never against an operator DB.
It creates/deletes its own synthetic temporary databases. Dependencies are the
ADR-selected Go/GCC/glibc target; no system SQLite dependency. Its source is
included in full so review does not depend on a private scratch path.

```sh
mkdir /tmp/hearthlog-slice00-review-unique
cd /tmp/hearthlog-slice00-review-unique
go mod init example.invalid/hearthlog-slice00-probe
GOTOOLCHAIN=local go get github.com/mattn/go-sqlite3@v1.14.53
# Save the following Go block as main.go in this scratch directory only.
```

```go
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	sqlite3 "github.com/mattn/go-sqlite3"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func main() {
	dir, err := os.MkdirTemp("", "hearthlog-probe-")
	must(err)
	defer os.RemoveAll(dir)
	ctx := context.Background()
	dsn := "file:" + filepath.Join(dir, "probe.db") + "?_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on&_busy_timeout=250&_auto_vacuum=incremental&_mutex=full"
	db, err := sql.Open("sqlite3", dsn)
	must(err)
	defer db.Close()
	db.SetMaxOpenConns(2)
	c, err := db.Conn(ctx)
	must(err)
	defer c.Close()
	var version, source string
	must(c.QueryRowContext(ctx, "SELECT sqlite_version(),sqlite_source_id()").Scan(&version, &source))
	fmt.Printf("sqlite=%s source_id=%s\n", version, source)
	for _, name := range []string{"journal_mode", "synchronous", "foreign_keys", "busy_timeout", "locking_mode", "auto_vacuum", "wal_autocheckpoint"} {
		var v string
		must(c.QueryRowContext(ctx, "PRAGMA "+name).Scan(&v))
		want := map[string]string{"journal_mode": "wal", "synchronous": "2", "foreign_keys": "1", "busy_timeout": "250", "locking_mode": "normal", "auto_vacuum": "2", "wal_autocheckpoint": "1000"}
		if v != want[name] {
			panic("writer PRAGMA mismatch: " + name)
		}
		fmt.Printf("writer %s=%s\n", name, v)
	}
	var enabled int
	must(c.QueryRowContext(ctx, "SELECT sqlite_compileoption_used('ENABLE_FTS5')").Scan(&enabled))
	if enabled != 1 {
		panic("no FTS5")
	}
	fmt.Println("ENABLE_FTS5=1")
	rows, err := c.QueryContext(ctx, "PRAGMA compile_options")
	must(err)
	var opts []string
	for rows.Next() {
		var opt string
		must(rows.Scan(&opt))
		opts = append(opts, opt)
	}
	must(rows.Err())
	must(rows.Close())
	sort.Strings(opts)
	fmt.Println("compile_options=" + strings.Join(opts, ","))
	_, err = c.ExecContext(ctx, `CREATE TABLE logs(id INTEGER PRIMARY KEY, message TEXT, attrs TEXT CHECK(json_valid(attrs))); INSERT INTO logs VALUES(1,'synthetic sentinel','{"n":1,"b":true,"z":null,"s":"1","r":1.0}'); CREATE VIRTUAL TABLE search USING fts5(message); INSERT INTO search VALUES('synthetic sentinel');`)
	must(err)
	var n int
	must(c.QueryRowContext(ctx, `SELECT count(*) FROM search WHERE search MATCH '"sentinel"'`).Scan(&n))
	if n != 1 {
		panic("FTS count")
	}
	fmt.Println("FTS5 literal match=1")
	var types string
	must(c.QueryRowContext(ctx, `SELECT json_type(attrs,'$.n')||','||json_type(attrs,'$.b')||','||json_type(attrs,'$.z')||','||json_type(attrs,'$.s')||','||json_type(attrs,'$.r')||','||coalesce(json_type(attrs,'$.missing'),'missing') FROM logs`).Scan(&types))
	if types != "integer,true,null,text,real,missing" {
		panic(types)
	}
	fmt.Println("JSON types=" + types)
	// This is a separate new connection, not a checkout of the writer.
	r, err := db.Conn(ctx)
	must(err)
	_, err = r.ExecContext(ctx, "PRAGMA query_only=ON")
	must(err)
	for _, name := range []string{"journal_mode", "synchronous", "foreign_keys", "busy_timeout", "locking_mode", "query_only"} {
		var v string
		must(r.QueryRowContext(ctx, "PRAGMA "+name).Scan(&v))
		want := map[string]string{"journal_mode": "wal", "synchronous": "2", "foreign_keys": "1", "busy_timeout": "250", "locking_mode": "normal", "query_only": "1"}
		if v != want[name] {
			panic("reader PRAGMA mismatch: " + name)
		}
		fmt.Printf("reader %s=%s\n", name, v)
	}
	_, err = r.ExecContext(ctx, "INSERT INTO logs VALUES(2,'forbidden','{}')")
	if err == nil {
		panic("reader wrote")
	}
	fmt.Println("query_only write=denied")
	start := time.Now()
	deadline, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	err = r.QueryRowContext(deadline, `WITH RECURSIVE x(n) AS (VALUES(0) UNION ALL SELECT n+1 FROM x WHERE n<1000000000) SELECT sum(n) FROM x`).Scan(&n)
	cancel()
	if time.Since(start) > time.Second {
		panic("interrupt exceeded 1s")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		panic(fmt.Sprintf("cancel: %v", err))
	}
	must(r.QueryRowContext(ctx, "SELECT 1").Scan(&n))
	if n != 1 {
		panic("connection reuse")
	}
	must(r.Close())
	fmt.Println("interrupt=deadline exceeded; same connection reusable")
	dest, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, "backup.db"))
	must(err)
	dc, err := dest.Conn(ctx)
	must(err)
	must(dc.Raw(func(d any) error {
		return c.Raw(func(s any) error {
			b, e := d.(*sqlite3.SQLiteConn).Backup("main", s.(*sqlite3.SQLiteConn), "main")
			if e != nil {
				return e
			}
			done := false
			until := time.Now().Add(time.Second)
			for !done && time.Now().Before(until) {
				done, e = b.Step(16)
				if e != nil {
					b.Finish()
					return e
				}
			}
			e = b.Finish()
			if e != nil {
				return e
			}
			if !done {
				return errors.New("backup timeout")
			}
			return nil
		})
	}))
	var integrity string
	must(dc.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity))
	if integrity != "ok" {
		panic(integrity)
	}
	must(dc.QueryRowContext(ctx, "SELECT count(*) FROM logs WHERE id=1 AND message='synthetic sentinel'").Scan(&n))
	if n != 1 {
		panic("backup sentinel")
	}
	must(dc.QueryRowContext(ctx, `SELECT count(*) FROM search WHERE search MATCH '"sentinel"'`).Scan(&n))
	if n != 1 {
		panic("backup FTS")
	}
	fk, err := dc.QueryContext(ctx, "PRAGMA foreign_key_check")
	must(err)
	if fk.Next() {
		panic("backup foreign key violation")
	}
	must(fk.Err())
	must(fk.Close())
	_, err = dc.ExecContext(ctx, "INSERT INTO search(search) VALUES('integrity-check')")
	must(err)
	must(dc.Close())
	must(dest.Close())
	fmt.Println("online backup=complete; integrity=ok; sentinel=1; FTS=1")
	fmt.Println("PASS capability probe only; no application or durability acceptance")
}
```

```sh
go version
go env GOOS GOARCH CGO_ENABLED CC
gcc --version
ldd --version
GOTOOLCHAIN=local go mod verify
GOTOOLCHAIN=local CGO_ENABLED=1 go run -tags 'fts5 sqlite_omit_load_extension' .
GOTOOLCHAIN=local CGO_ENABLED=1 go build -trimpath -buildvcs=false -tags 'fts5 sqlite_omit_load_extension' -ldflags=-buildid= -o probe-a .
GOTOOLCHAIN=local CGO_ENABLED=1 go build -trimpath -buildvcs=false -tags 'fts5 sqlite_omit_load_extension' -ldflags=-buildid= -o probe-b .
cmp probe-a probe-b
sha256sum probe-a probe-b
ldd probe-a
```

Observed exit 0, actual output (SQLite source ID is the exact upstream identifier,
not a local host timestamp):

```text
sqlite=3.54.1 source_id=2026-10-16 14:45:20 555e07ef33f69a0957d24d8e555b702464a131d6f1607f4c3c1e4551e8e6ca76
writer journal_mode=wal
writer synchronous=2
writer foreign_keys=1
writer busy_timeout=250
writer locking_mode=normal
writer auto_vacuum=2
writer wal_autocheckpoint=1000
ENABLE_FTS5=1
compile_options=ATOMIC_INTRINSICS=1,COMPILER=gcc-14.2.0,DEFAULT_AUTOVACUUM,DEFAULT_CACHE_SIZE=-2000,DEFAULT_FILE_FORMAT=4,DEFAULT_JOURNAL_SIZE_LIMIT=-1,DEFAULT_MMAP_SIZE=0,DEFAULT_PAGE_SIZE=4096,DEFAULT_PCACHE_INITSZ=20,DEFAULT_RECURSIVE_TRIGGERS,DEFAULT_SECTOR_SIZE=4096,DEFAULT_SYNCHRONOUS=2,DEFAULT_WAL_AUTOCHECKPOINT=1000,DEFAULT_WAL_SYNCHRONOUS=1,DEFAULT_WORKER_THREADS=0,DIRECT_OVERFLOW_READ,ENABLE_FTS3,ENABLE_FTS3_PARENTHESIS,ENABLE_FTS5,ENABLE_RTREE,ENABLE_UPDATE_DELETE_LIMIT,MALLOC_SOFT_LIMIT=1024,MAX_ATTACHED=10,MAX_COLUMN=2000,MAX_COMPOUND_SELECT=500,MAX_DEFAULT_PAGE_SIZE=8192,MAX_EXPR_DEPTH=1000,MAX_FUNCTION_ARG=1000,MAX_LENGTH=1000000000,MAX_LIKE_PATTERN_LENGTH=50000,MAX_MMAP_SIZE=0x7fff0000,MAX_PAGE_COUNT=0xfffffffe,MAX_PAGE_SIZE=65536,MAX_SCHEMA=10000000,MAX_SQL_LENGTH=1000000000,MAX_TRIGGER_DEPTH=1000,MAX_VARIABLE_NUMBER=32766,MAX_VDBE_OP=250000000,MAX_WORKER_THREADS=8,MUTEX_PTHREADS,OMIT_DEPRECATED,OMIT_LOAD_EXTENSION,SYSTEM_MALLOC,TEMP_STORE=1,THREADSAFE=1
FTS5 literal match=1
JSON types=integer,true,null,text,real,missing
reader journal_mode=wal
reader synchronous=2
reader foreign_keys=1
reader busy_timeout=250
reader locking_mode=normal
reader query_only=1
query_only write=denied
interrupt=deadline exceeded; same connection reusable
online backup=complete; integrity=ok; sentinel=1; FTS=1
PASS capability probe only; no application or durability acceptance
```

Toolchain output: `go version go1.27.3 linux/amd64`; GOOS linux, GOARCH amd64,
CGO_ENABLED 1, CC gcc. GCC `(Debian 14.2.0-19) 14.2.0`; glibc
`(Debian GLIBC 2.41-12+deb13u4) 2.41`. `go mod verify`: `all modules verified`.
`ldd probe-a` showed Linux vdso, libm.so.6, libc.so.6 and Linux ELF loader;
ASLR addresses/system paths omitted. This is dynamically linked, **not** a
universally static binary. Build equality/checksum results are recorded below.

The probe asserts PRAGMA values on the writer and a separate new reader, exact
JSON types and FTS match, denies a reader write, times out a large recursive SQL
query and reuses the same connection within a 1s bound. It completes stepped
Online Backup API copying, integrity_check, foreign_key_check, FTS integrity
and sentinel checks. It does not exercise concurrent application writes,
external-content FTS triggers, power loss, receipt/watermark recovery, tenant
authorization, source revocation, VM-step budgets or benchmark acceptance.
Those remain mandatory implementing-slice tests, not inferred from capability.

## Documentation, ignores and public safety gate

Run from repository root with Python 3 (documentation-only validation):

```sh
python3 - <<'PY_CHECK'
import json, re
from pathlib import Path
root = Path.cwd()
parsed = 0
for path in root.rglob('*.md'):
    if '.git' in path.parts:
        continue
    text = path.read_text()
    for block in re.findall(r"```json\n(.*?)\n```", text, re.S):
        json.loads(block)
        parsed += 1
    for target in re.findall(r"\[[^\]]*\]\(([^)]+)\)", text):
        if '://' not in target and not target.startswith('#'):
            assert (path.parent / target.split('#')[0]).exists(), (path, target)
print(f"JSON examples parsed={parsed}; local Markdown links=ok")
PY_CHECK
```

Run ignore checks (all 13 paths must be printed, exit 0):

```sh
printf '%s\n' .env .env.example nested/.env.test nested/example.env nested/example.env.sample example.db example.db-wal example.sqlite3-shm example.backup example.log example.pem example.har dist/probe | git check-ignore --stdin
```

Actual result: all 13 printed, including environment examples and nested paths.
This does not check tracked files; manually review the complete staged inventory.

Scanner positive control (only a generated, unusable synthetic marker in a
self-deleting temporary directory, no runtime token/account accessed):

```sh
python3 - <<'PY_SCAN'
from pathlib import Path
import tempfile, secrets, subprocess
with tempfile.TemporaryDirectory(prefix='hearthlog-scan-') as d:
    Path(d, 'synthetic.txt').write_text('github_token = "' + 'ghp_' + secrets.token_hex(20) + '"\n')
    r = subprocess.run(['gitleaks','dir','--redact','--no-banner',d], capture_output=True, text=True)
    print('synthetic scanner canary exit=' + str(r.returncode))
    print('scanner detected canary=' + str('leaks found' in (r.stderr+r.stdout)))
    assert r.returncode == 1 and 'leaks found' in (r.stderr+r.stdout)
PY_SCAN
```

Actual output:

```text
synthetic scanner canary exit=1
scanner detected canary=True
```

With gitleaks 8.30.1, stage only reviewed Slice 00 docs/ignore/link changes, then:

```sh
git status --short
git diff --cached --name-only
git diff --cached --check
git diff --cached
# Manually verify every changed line against the public allowlist.
gitleaks git --pre-commit --staged --redact --no-banner
gitleaks git --redact --no-banner
```

Do not treat an empty staging area as a meaningful staged scan. Repeat after
edits. Build equality is below. No scripts,
Go module/source, application, fixture/config/env files, databases, binaries,
CI jobs or runtime artifacts are tracked in this slice. Local scan procedure
is exercised here; later runnable slices must add automated enforcement before
merge. Review of exact staged contents remains necessary even with a clean scan.

## Unresolved technical decisions

See ADR operator questions, especially VM-step hook feasibility, actual host
runtime/storage and privacy/recovery requirements. Proposed v1 API semantics
need independent acceptance, not a silent assumption. Reviewer should repeat
commands above and examine duplicate keys/types, reserved fields, lost acks,
expiry, watermark/recovery and every security matrix route. A context timeout
is not evidence of the required VM-step budget before Slice 05.

## Recorded local verification summary

- Documentation validation: `JSON examples parsed=5; local Markdown links=ok`.
- `git diff --check`: exit 0; ignore check: 13/13 expected paths printed.
- Scanner version 8.30.1; synthetic positive-control exit 1/detected True.
- Final probe: exit 0, including real vs integer vs bool/null/string/missing types.
- Two scratch builds compared equal (`cmp` exit 0), both SHA-256
  `5a3d5bf3bc2944924a73bd4f89c2c3fc5e4d7cf3e96b103e37fd676e8d00d96f`.
  This fingerprint is for the documented diagnostic on the stated toolchain,
  **not** a Hearthlog release checksum; different compiler inputs may differ.
- Include this evidence file in the public-content review and scanner gate.
  A successful secret scan does not establish that prose or outgoing history
  is free of private metadata.
