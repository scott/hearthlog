# Implementation slices

All issues are open planned work and intentionally unassigned. Filing does not indicate implementation or approval to deploy.

| Slice | Issue | Dependencies |
|---|---|---|
| 00 | [[Slice 00] Lock synthetic event, SQLite and security contracts](https://github.com/scott/hearthlog/issues/1) | None |
| 01 | [[Slice 01] Specify original HTML investigation flow and states](https://github.com/scott/hearthlog/issues/2) | [00](https://github.com/scott/hearthlog/issues/1) |
| 02 | [[Slice 02] Ship local Go-to-SQLite-to-HTML walking skeleton](https://github.com/scott/hearthlog/issues/3) | [00](https://github.com/scott/hearthlog/issues/1), [01](https://github.com/scott/hearthlog/issues/2) |
| 03 | [[Slice 03] Complete bounded redacting asynchronous slog SDK](https://github.com/scott/hearthlog/issues/4) | [02](https://github.com/scott/hearthlog/issues/3) |
| 04 | [[Slice 04] Implement SQLite single writer and atomic native replay](https://github.com/scott/hearthlog/issues/5) | [03](https://github.com/scott/hearthlog/issues/4) |
| 05 | [[Slice 05] Add indexed bounded SQLite search and keyset cursors](https://github.com/scott/hearthlog/issues/6) | [04](https://github.com/scott/hearthlog/issues/5) |
| 06 | [[Slice 06] Build server-rendered htmx investigation interface](https://github.com/scott/hearthlog/issues/7) | [01](https://github.com/scott/hearthlog/issues/2), [05](https://github.com/scott/hearthlog/issues/6) |
| 07 | [[Slice 07] Add bounded htmx Live polling with commit-safe cursor](https://github.com/scott/hearthlog/issues/8) | [04](https://github.com/scott/hearthlog/issues/5), [05](https://github.com/scott/hearthlog/issues/6), [06](https://github.com/scott/hearthlog/issues/7) |
| 08 | [[Slice 08] Harden identity, workspace scope and source lifecycle](https://github.com/scott/hearthlog/issues/9) | [04](https://github.com/scott/hearthlog/issues/5), [05](https://github.com/scott/hearthlog/issues/6) |
| 09 | [[Slice 09] Verify scoped OTel Go logs interoperability](https://github.com/scott/hearthlog/issues/10) | [03](https://github.com/scott/hearthlog/issues/4), [04](https://github.com/scott/hearthlog/issues/5), [08](https://github.com/scott/hearthlog/issues/9) |
| 10 | [[Slice 10] Implement SQLite retention, checkpoint and online recovery](https://github.com/scott/hearthlog/issues/11) | [04](https://github.com/scott/hearthlog/issues/5), [08](https://github.com/scott/hearthlog/issues/9) |
| 11 | [[Slice 11] Prove SQLite/htmx readiness and prepare opt-in onboarding](https://github.com/scott/hearthlog/issues/12) | [06](https://github.com/scott/hearthlog/issues/7), [07](https://github.com/scott/hearthlog/issues/8), [08](https://github.com/scott/hearthlog/issues/9), [09](https://github.com/scott/hearthlog/issues/10), [10](https://github.com/scott/hearthlog/issues/11) |
