# S3 filesystem and commit-journal consistency

**This is an opt-in experimental backend and a fault-testing harness, not a
completed migration or a transactional consistency fix.** The tests deliberately
characterize the current gaps between visible files and recorded commits. A
passing fault suite means those gaps were reproduced as specified, not eliminated.

## Interfaces and storage layout

`openlore.NewS3FS(client, bucket, prefix)` implements the existing
`vfs.WritableFS`. Use a caller-configured AWS SDK v2 client and a pre-provisioned,
dedicated prefix. The constructor does not create buckets or change policies.
It starts read-only; `NewServerWithRootFS(fsys, WithReadonly(false), ...)` enables
writes through the normal ordered write log. The integration test exercises that
actual server constructor and `Server.CommitChangeSet`, not only direct FS calls.

The layout is:

- `prefix/objects/<sha256>`: file content, addressed by its exact bytes.
- `prefix/manifest.json`: directory entries and file hashes/sizes/timestamps.

File objects are uploaded before a conditional manifest PUT publishes them.
Existing manifests use `If-Match` with the **S3 ETag**; the initial manifest uses
`If-None-Match: *`. These ETags are not confused with the VFS's SHA-256 content
preconditions. An unchanged manifest means orphaned uploads are invisible.
Updating one manifest makes whole-tree removal atomic instead of pretending a
sequence of S3 DELETEs is atomic. Competing publications return a conditional-write
error; there is no silent last-writer-wins replacement of a stale namespace.

`S3FileClient` is the narrow GetObject/PutObject boundary. `CommitJournal.Append`
is the independent journal-write boundary; the default `fileCommitJournal` still
uses the existing fsynced JSONL append. `HistoryCursor` remains the replay API.
The write log snapshots its journal/blob configuration under its existing lock.
This refactor preserves the existing **file first, journal second** ordering and
the existing behavior of logging journal errors without failing committed writes.

Journal segments, pre-image blobs, checkpoints and other server state remain in
the local data directory. The CLI configuration does not automatically select S3.

## Running the tests

```sh
nix develop --command make test-filesystem-s3
```

This runs `TestS3Filesystem` with the race detector and `integration` tag. It starts
and removes a pinned `localstack/localstack:4.9.2` Testcontainers container and
uses only disposable local S3 data and test credentials. Docker is required;
missing Docker fails instead of skipping. The Build workflow runs this target.

Final verification passed: `make test-filesystem-s3` (45.722 seconds),
`go test -race ./...` (`pkg/openlore`: 175.396 seconds), `go build ./...`, and
`git diff --check`. Test containers and temporary probes were removed afterward.

The Hegel property runs **1,000 cases** over all 21 combinations of seven fault
modes and create/update/delete. It verifies that every matrix cell was exercised.
It checks exact visible bytes, journal count/actions, analytics byte deltas,
restart behavior and conditional retries. Payloads are arbitrary binary; the
generated before/after sizes intentionally differ so a lost change cannot hide
behind a zero delta. Empty files are checked separately.

Faults occur before or after real SDK operations. An "after" fault means the
operation completed but its acknowledgment was lost to the caller. This tests
ambiguous outcomes at the adapter boundary, not packet-level faults or AWS SDK
retry internals. The test client disables SDK retries to keep each boundary exact.

## Observed consistency matrix

| Boundary | Visible S3 state | Journal/analytics | Caller and recovery |
| --- | --- | --- | --- |
| No fault | New state | New commit | Success; consistent after reopen. |
| Content PUT fails before or after storage | Old state; an after-failure may leave an orphan blob | No new commit | Error; original operation retries successfully. Deletes are unaffected controls because they upload no content. |
| Manifest PUT fails before storage | Old state | No new commit | Error; original operation retries successfully. |
| Manifest PUT succeeds, acknowledgment lost | **New state** | **Missing commit** | Error; reopening does not repair it. Original create-only/CAS/delete retries fail because the change already happened. |
| Journal append fails before storage | **New state** | **Missing commit** | **Success** under the existing write-log policy; reopening does not repair it. |
| Journal append succeeds, acknowledgment lost | New state | New commit | Success; consistent. An append error alone cannot tell whether a record exists. |

Two additional batch reproductions leave `/a` changed and `/b` unchanged with
**no batch journal record**:

- The first leaf's manifest PUT succeeds but its acknowledgment is lost. The
  generic batch applier treats the first-leaf error as no committed work.
- The second leaf fails before publication and restoring the first leaf also
  fails. The caller gets `vfs.RollbackError`, but partial state is not journaled.

These are limitations of the existing mutation/journaling protocol exposed by
remote storage. Conditional S3 writes protect namespace publication, not the
separate file-to-journal transition or a multi-leaf batch.

## Executed failure ledger

- **Strict consistency counterexample:** a temporary Go test overlay required
  analytics bytes to equal visible file bytes after every fault. Hegel shrank
  the failure to `manifest-after/create` with a two-NUL-byte file: **S3 = 2 bytes,
  analytics = 0**. This strict invariant genuinely failed. The retained tests
  explicitly characterize this unresolved gap; they are not presented as proof
  of transactional safety. The temporary overlay was removed.
- **New-adapter integration bug, fixed:** the first server-root test failed on
  `/.lore/config.yaml: access denied`. S3 path validation was missing the existing
  DirFS exception for directory-rule configuration. That exception is now shared;
  reserved directories remain hidden and recursive deletion refuses hidden
  descendants. The actual server-root test then passed.

## Other coverage and scale probe

- `Stat`, `ReadFile`, `ReadDir`, atomic writes, both mkdir variants, both remove
  variants, stale subtree snapshots, docset-root protection and adjacent prefixes.
- Read-only rejection and waiting for an in-flight publication before completing
  `SetReadonly`.
- Two clients read the same manifest, then publish concurrently: exactly one
  succeeds, the loser gets an S3 precondition error, and a fresh retry preserves
  both files and the original file.
- Equal-length content corruption is rejected by SHA-256, corrupt manifests
  are not overwritten as empty storage, and a missing bucket is not mistaken for
  an empty filesystem.
- A temporary scale probe loaded a **100,000-file, 13,600,260-byte manifest** into
  S3, verified every sorted directory entry, read content, published an additional
  file and removed the large subtree while preserving siblings. The probe passed
  in 1.28 seconds excluding container startup/teardown. This was a seeded large
  namespace, **not 100,000 sequential writes or a throughput benchmark**.

## Limits and next design decision

This simple manifest implementation reads the entire namespace per operation and
rewrites it per mutation. It is a useful correctness prototype, not a scalable
production layout for all files. It has no xattr support, blob garbage collection,
multi-docset boundary configuration, or DirFS extension/ignore-policy filtering;
server admission remains responsible for content policy. Direct filesystem access
is a trusted storage API, not an authorization API. Normal writes retain the
existing 8 MiB cap. VFS methods have no context parameter, so configure SDK request
timeouts on the supplied client. Tests do not establish real AWS IAM/KMS, throttling,
power-loss durability, or cross-service journal ordering.

The next consistency change should give each operation a durable identity and
publish its replayable commit/outbox record **in the same atomic namespace update
as its file changes**, then deliver journal/analytics records idempotently from
that durable state. Merely moving `commits.jsonl` to S3 would still leave two
independent writes. Multi-leaf commits need an atomic backend commit boundary or
an explicit durable recovery protocol too. Neither protocol is implemented here.
