# SQLite, Litestream, and separate analytics recovery

## Scope

This is an executable **compatibility and architecture experiment**, not a
production SQLite filesystem backend, a JSONL migration, or a Litestream
deployment. The application still uses its existing commit-journal path.

The integration harness uses:

- The repository's pinned `modernc.org/sqlite` driver, not a substituted driver.
- Litestream **0.5.17** as a separate executable.
- Testcontainers with `localstack/localstack:4.9.2` and the real AWS SDK v2.
- A test-only `events-filesystem.db` containing current file references and
  append-only commit records, committed in one SQLite transaction.
- The **real** `analytics.OpenSQLiteAggregationStore` in `analytics.db` and
  the real `ScalarProcessor` to derive byte deltas from the fixture's events.
- Content-addressed S3 blobs, including historical content. Database event
  records carry hashes and sizes, not file bytes; the fixture cursor hydrates
  content from S3 for the existing processor contract.

Analytics is a separate application boundary: its database and backup prefix
are independent of the authoritative events/filesystem database. There is no
`ATTACH`, cross-database transaction, or promise of a common restore point.
The fixture saves its derived byte count and consumer checkpoint together in
one analytics materialization; this does **not** change the production
pipeline's checkpoint schema or implement an automatic recovery protocol.

## Run

```sh
nix develop --command make test-litestream
```

Docker is required. The script downloads the pinned official Litestream release,
checks its SHA-256 against the release checksums, and deletes it on exit. Set
`LITESTREAM_BIN` to reuse an existing **0.5.17** executable. The Build workflow
runs this target with the race detector; unavailable dependencies fail rather
than silently skipping the integration suite. No real AWS credentials or buckets
are used, and subprocesses, containers, and temporary databases are cleaned up.

## Properties

| Property | Cases | Failure it distinguishes |
| --- | ---: | --- |
| Blob upload + SQL transaction + retry | 1,000 | A failed upload or rolled-back transaction must not publish file references or an event. A lost successful response must not duplicate an operation on retry. Reusing its ID with changed input must fail. |
| Live replication, process crash, and restore | 24 | Backups must preserve exact committed file contents, ordered historical commits, and analytics values; uncommitted changes in either database must disappear. |
| Analytics backup behind source | 16 | The restored analytics checkpoint can lag independently; rebuilding from authoritative events must produce the exact current byte count. |
| Analytics backup ahead of source | 16 | An acknowledged but unreplicated source commit can disappear while analytics retains it. The stale checkpoint must not be mistaken for authoritative history. |

The transactional property exercises all 12 combinations of create/update/delete
and no fault, failure before S3 upload, lost upload acknowledgment, and SQL
rollback. Before/after payload sizes deliberately differ; arbitrary binary
contents include empty files. S3 faults wrap real SDK calls with retries disabled.
The model is an independent map of expected file bytes, not SQL results reused
as the expected answer.

Each crash case re-executes the Go test binary as the actual modernc writer.
Litestream runs concurrently. The parent first verifies a restored baseline,
then releases further generated writes, verifies that backups catch up, and
requests uncommitted writes in **both** databases. It sends SIGKILL to both
processes without graceful close or final synchronization. The test verifies
local WAL recovery and new restores into a different directory, including
`PRAGMA integrity_check`, exact history, every referenced content object, and
the real analytics store's `Get` result. Small generated histories keep these
expensive subprocess tests practical; the richer upload/retry matrix runs 1,000
cases. The tests do not claim process termination simulates power loss.

The skew cases deterministically replicate only one database's second commit;
they do not rely on winning a race against a timer. In the analytics-ahead case,
the source restores at commit 1 while analytics restores at commit 2. The orphan
blob for the lost commit still exists in S3. A new source branch reuses sequence
number 2 with a **different commit ID**, demonstrating why a numeric offset alone
cannot validate a consumer checkpoint after restore. The fixture then rebuilds
and persists only analytics, without importing derived data into the source.

## Additional validation

A temporary scale probe seeded **100,000 file references and 100,000 commit
records**, replicated and restored a **35,311,616-byte database**, and verified
every restored path, sequence, size, hash, record identity, and the shared S3
content object. It passed with the race detector in 29.692 seconds including
container lifecycle. This was bulk-seeded metadata, not 100,000 sequential
application writes or a production throughput benchmark. The probe was removed.

A temporary mutation made the fixture commit its transaction while returning
the injected rollback error. The retained property failed on the actual visible
file state, shrinking to **create `/a.md` with empty content**: the file existed
despite the rejected operation. The source overlay was removed; this is a check
of the test oracle, not a bug found in production SQLite code.

**Startup ordering matters.** Starting Litestream and the writer simultaneously
against absent database files intermittently failed in the real
`OpenSQLiteAggregationStore` constructor with `database is locked (5)
(SQLITE_BUSY)`, before the first application write. A focused repeated-startup
probe reproduced this independently of restore or blob contents. The harness
now waits for both WAL/schema initializations before starting the sidecar, then
tests live concurrent writes and replication as before. This establishes an
operational precondition; it does not change the production constructor to
support unordered concurrent initialization. Future deployment wiring must
initialize/migrate first and start replication afterward.

After this change, a temporary **1,000-case startup probe** passed with the race
detector (144.848 seconds including container lifecycle). The complete retained
`make test-litestream` suite also passed (110.742 seconds), including live
replication, process crashes, fresh restores, and both backup-skew directions.
The startup-only probe was removed; it was not a substitute for those recovery
assertions.

## What this means for the design

1. Keep `events-filesystem.db` authoritative for path-to-blob references and
   commit/version history. Upload blobs first, then transact references and
   commit records together. Orphaned uploads are preferable to missing content
   for published references.
2. Keep analytics in `analytics.db`. Give it an event-read API, stable commit
   identities, and a checkpoint validation/rebuild protocol, not write access
   to the source database. A future cross-process transport is not implemented
   by this in-process fixture cursor.
3. Replicate each database to a distinct S3 prefix. A healthy backup of one
   does not establish the durability or recovery point of the other.
4. Treat Litestream as asynchronous disaster recovery. A local SQL commit is
   not an S3 durability acknowledgment. The crash test explicitly waits for
   independently verified backups; the skew test explicitly shows unsynced
   acknowledged state missing from a restore.
5. Preserve blob references needed by retained version history **and all
   restorable source database backups**. No blob garbage collector is introduced
   here. An old valid database backup can become unusable if its blobs expire.
6. Preserve app-level immutable commit history for diff/rollback. Restoring an
   entire Litestream backup is disaster recovery, not per-file undo.

This keeps JSONL available as a readable export while avoiding the separate
file-write/journal-append transaction gap **in the proposed schema**. It does not
eliminate the current production gaps documented in
[the S3 filesystem fault tests](s3-filesystem-testing.md).

## Coverage limits

This suite covers the default Linux locking mode, WAL operation, real analytics
`Put`/`Get`, committed and aborted transactions, backup isolation, and recovery
with immutable S3 content. The fixture cursor's `Seek` is only an interface
adapter; it is not a proposed production cursor implementation. Other analytics
store APIs remain covered by their existing tests, not every API is re-tested
through Litestream. Filesystem path policy, directories, tree CAS, batches, and
concurrent manifest writers are covered by the separate S3 filesystem suite,
not by this deliberately small SQL schema.

Not established: real AWS IAM/KMS behavior, eventual production traffic rates,
multi-writer failover/fencing, network filesystems, optional modernc OFD locking,
remote corruption/retention of Litestream's own LTX files, replica outage disk
growth, a bounded recovery-point objective, or automated deployment/migration.
Those require separate operational tests before making SQLite the source of
truth in production.

References: [modernc driver](https://pkg.go.dev/modernc.org/sqlite@v1.59.0),
[Litestream caveats](https://litestream.io/tips/),
[replication and restore granularity](https://litestream.io/how-it-works/).
