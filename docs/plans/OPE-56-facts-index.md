# OPE-56 — Per-file current facts index

Status: agreed design, not yet implemented.
Linear: [OPE-56](https://linear.app/oiya/issue/OPE-56/cache-current-analytics-facts-per-file).
Related: OPE-55 (context unavailable at root), follow-up issue for event-log
columnar storage (see "Out of scope").

## Problem

`GET /dashboard/api/context` walks the identity-scoped filesystem, reads every
file in full, hashes and tokenizes it, and sums the results into folder nodes
(`pkg/openlore/dashboard.go`, `dashboardContextNodeFromInfo`). The per-identity
aggregations `tree-size`, `largest-docs` and `*-used-*` do the same through
`ContentFacts.Walk` (`internal/analytics/content.go`). Every root request
repeats all of that work even when nothing changed.

## Decisions

Each item below was settled in the design review; the reasoning is recorded so
it is not re-litigated during implementation.

1. **Engine: SQLite, not DuckDB.** Releases and the Railpack deploy build with
   `CGO_ENABLED=0` and cross-compile a GOOS/GOARCH matrix including Windows
   (`.github/workflows/release.yml`, `railpack-plan.json`). The DuckDB Go
   driver is CGO-only. `modernc.org/sqlite` is pure Go and already a
   dependency. The facts index is a key/value workload (point lookups by path,
   prefix scans) where columnar storage buys nothing; the workload that would
   benefit from columnar storage is the JSONL event log, which is a separate
   issue.
2. **One database, one switch.** The facts tables live in the existing
   aggregation database `<analytics.Dir>/aggregations.sqlite`. The default
   `analytics.aggregations.store` flips from `file` to `sqlite`; `file` remains
   as opt-in legacy. `analytics.enabled: false` disables the whole analytics
   app including the index; the dashboard then computes uncached exactly as
   today. No migration: the refresher repopulates materializations on its
   normal cycle.
3. **The SQLite database is the cache.** No in-process memory tier.
4. **Read-through everywhere.** Both the dashboard walk and `ContentFacts`
   consult the index; a miss computes from the filesystem and populates the
   index transparently. Correctness never depends on the background indexer.
5. **Change detection is `du`-shaped.** `ReadDir` already returns size and
   mtime per entry. A row is a hit when size and mtime match and every active
   scalar source is present. The content hash is stored (it is computed anyway)
   but is not on the hit path. Known blind spot: an external edit that
   preserves both size and mtime is not detected until the file changes again.
6. **Facts are computed on raw on-disk bytes.** Not on `ContentTransform`
   output. The only transform today injects remote-sync status into `SKILL.md`
   from in-memory plugin state, so transformed bytes can change with no file
   change. Misses read from `s.merge` (raw, unscoped) at the canonical path
   the scoped filesystem enumerated. This also bypasses read middleware
   (agent-skills sync trigger) and session CAS tracking on the miss path,
   which is intended: facts computation has no side effects.
7. **Permission filtering stays upstream.** The identity-scoped filesystem
   enumerates; the index is identity-agnostic; folder totals are folded in Go
   per request from the visible files. Folder totals are never stored
   (nested-docset carve-outs make them identity-specific).
8. **Post-commit hook only enqueues.** The write log's post-commit chain does
   no database work; it enqueues the committed leaf paths (a directory remove
   enqueues that subtree for pruning).
9. **Background indexer driven by the commit log, not fs-watch.** A bounded
   worker pool drains a deduplicated subtree queue: walk the subtree on
   `s.merge`, stat each file, compute misses, prune vanished rows. Startup
   enqueues `/` (warm + prune of orphan rows). External changes are reconciled
   lazily at request time by stat mismatch; that matches the issue text.
10. **Deletions**: post-commit remove deletes the row; `ErrNotExist` during a
    request walk deletes the row; the `/` walk prunes rows not seen.
11. **Scalars are stored by source; the tokenizer is not a column.** The
    tokenizer and plugin scalar providers change at runtime
    (`Service.SetTokenizer`, `RegisterScalarProvider`). Each scalar row is
    tagged with the provider name that produced it. A partial miss (new
    tokenizer or provider) reads the file once and fills only what is missing.
    Rows for inactive sources are kept until the file changes so switching
    back is a hit. `SetTokenizer` and `RegisterScalarProvider` enqueue `/` so
    the workers warm the new source.
12. **Concurrency**: duplicate misses upsert idempotently; SQLite WAL plus the
    existing `busy_timeout(10000)`; no singleflight.
13. **Failure semantics**: any index error falls back to uncached computation
    for that file and logs once; requests always return exact numbers. The
    filesystem is the authority; the index is an accelerator.
14. **Walk limits unchanged**: 10,000 nodes, depth 64, 64 MiB per file. The
    background indexer ignores the node limit (it is not a response).
15. **Dashboard responses stay `private, no-store`.**

## Schema

```sql
CREATE TABLE IF NOT EXISTS files (
  path         TEXT PRIMARY KEY,   -- canonical merge path
  size         INTEGER NOT NULL,
  mtime_ns     INTEGER NOT NULL,
  content_hash TEXT    NOT NULL,   -- sha256 hex, as ComputeScalars already emits
  computed_at  INTEGER NOT NULL    -- unix ns
);
CREATE TABLE IF NOT EXISTS file_scalars (
  path   TEXT NOT NULL REFERENCES files(path) ON DELETE CASCADE,
  source TEXT NOT NULL,            -- "size" | tokenizer Name() | provider Name()
  scalar TEXT NOT NULL,            -- "bytes" | "lines" | "words" | "characters" | "tokens" | plugin scalar
  value  REAL NOT NULL,
  PRIMARY KEY (path, source, scalar)
);
```

`characters` (rune count) is an index scalar under source `size`; it is not
added to `sizeProvider` because that would change the `doc.scalars` event
payload. Prefix scans for `tree-size`/`largest-docs` use the `files` primary
key B-tree (`path >= ? AND path < ?`).

## Access path

```
request walk (scoped FS)                index                        raw FS (s.merge)
────────────────────────                ─────                        ────────────────
ReadDir(dir) → entries ──────────────▶ Lookup(path, size, mtime,
   for each file                          activeSources)
                                          hit  → scalars ────────────▶ fold into folder node
                                          miss ─────────────────────▶ ReadFile(path)
                                                                       computeScalars(all providers)
                                          Upsert(files + file_scalars)
   ErrNotExist ─────────────────────────▶ Delete(path)
```

Hit condition: `files.size == stat.size && files.mtime_ns == stat.mtime &&
∀ source ∈ activeSources: ∃ file_scalars(path, source, *)`.

Miss handling: read raw bytes once; run all current providers; in one
transaction replace the `files` row and, if the content hash changed, delete
all `file_scalars` for the path; upsert rows for every current provider.

## Components

| Component | Location | Responsibility |
|---|---|---|
| `FactsIndex` interface + SQLite impl | `internal/analytics/factsindex.go` | `Lookup`, `Upsert`, `Delete`, `PrefixScan`, `Prune(seen)`, `Close`; opens tables in the aggregation DB |
| Read-through `ContentFacts` | `internal/analytics/content.go` | `Service.NewContentFacts(scoped)` returns a `contentFacts` that enumerates on `scoped`, looks up the index, computes misses on `s.fs` (raw) |
| Dashboard walk | `pkg/openlore/dashboard.go` | Replace inline read+compute with `index.Lookup`/miss path; delete on `ErrNotExist`; unchanged limits and folding |
| Indexer queue + workers | `internal/analytics/indexer.go` | Deduplicated subtree queue, `analytics.index.workers` (default 2), startup `/` warm + prune, ctx-cancelled on `Service.Close` |
| Post-commit enqueue | `pkg/openlore/analytics_plugin.go` | `PostCommitMiddleware` enqueues committed leaf paths / removed subtrees |
| Config | `internal/config/config.go`, `openlore.yml.example` | default `aggregations.store: sqlite`; new `index.workers` |
| Store wiring | `internal/analytics/service.go`, `storage.go` | `OpenSQLiteAggregationStore` also creates facts tables; `New` builds the index when the store is SQLite |
| Docs | `docs/dashboard.md`, `docs/configuration-and-identity.md` | behaviour, blind spot, config keys |

## Implementation order (each step runnable)

1. `FactsIndex` SQLite implementation with tests (schema, hit/miss/partial-miss,
   prune, prefix scan). Tables created by `OpenSQLiteAggregationStore`.
2. Dashboard walk reads through the index; counting-FS test proves unchanged
   files are not read. Flip default store to `sqlite`.
3. Read-through `ContentFacts` so `tree-size`/`largest-docs`/`*-used-*` benefit.
4. Indexer queue + workers; startup warm and prune; post-commit enqueue.
5. `SetTokenizer`/`RegisterScalarProvider` enqueue `/`.
6. Docs and example config.

## Tests that must fail on a plausible wrong implementation

- Cache hit skips `ReadFile`: counting FS wrapper; second root request reads 0 files.
- Same size, new mtime → recompute (catches "size-only" keys).
- Content hash change replaces all `file_scalars` rows (no stale tokens from a
  previous content under another source).
- Tokenizer switch → tokens recomputed for the new source, old source rows kept;
  switch back → hit without a read.
- Plugin provider registered later → only its scalar is missing; file read once,
  built-in scalars preserved.
- Nested-docset identity total < root identity total from the same index rows
  (permission filtering before folding).
- `ErrNotExist` mid-walk removes the row and the request succeeds.
- Startup `/` warm prunes a row for an externally deleted file.
- Index open failure → dashboard returns exact uncached totals; one log line.
- `SKILL.md` with injected remote status: dashboard bytes equal raw on-disk
  bytes, not transformed length.
- Node limit still trips at 10,001 nodes with a fully warm index.

## Out of scope (follow-up issue)

Columnar storage for the JSONL event log (`doc.read`/`doc.hit`/`doc.scalars`
scans behind `size-over-time`, `write-ratio`, `*-used-*`). The CGO constraint
applies there too, so DuckDB is not available without changing the release
pipeline; pure-Go options are a SQLite events table or Parquet files scanned
in Go.
