# Analytics S3 integration tests

Run from the repository root with a running Docker daemon:

```sh
nix develop --command make test-analytics-s3
```

This runs `TestS3Analytics` with the race detector and `integration` build tag.
It starts an isolated `localstack/localstack:4.9.2` container using
Testcontainers, creates a disposable bucket with test-only credentials, and
removes the container at the end. It never contacts an AWS account. Missing
Docker is an error, not a skipped test. The Build workflow runs this target
in addition to the ordinary Go suite.

The MinIO images originally considered for the suite were unavailable from
both official registries; LocalStack is pinned rather than using `latest`.

## What is implemented

`analytics.NewS3EventStore(client, bucket, prefix)` implements `Put`, paginated
`List`, and streaming `Get` for `RemoteEventStore`. Inject it through
`analytics.Deps.Remote`. The caller configures the AWS SDK v2 client's region,
credentials and endpoint, and provisions the bucket. The adapter does not
create buckets or modify policies. Use HTTPS for production endpoints.

This is an internal adapter and test harness, **not a deployed S3 migration**.
The CLI's `ship.remote` configuration is unchanged. SQLite projections, local
logs and checkpoints still live on disk. Multipart uploads, IAM/KMS policies,
remote checkpoint coordination and S3-backed knowledge files are not added.

## Coverage and contracts

| Surface | Contract exercised |
| --- | --- |
| Shipper → S3 Put/List/Get → RemoteSource | Every original event survives byte-for-byte JSON normalization; overlapping objects do not duplicate events within a day. |
| Service.RebuildFromRemote | Fresh local storage reconstructs usage summaries and command tables; running rebuild twice preserves the same result. |
| Shipper checkpoints | A failed upload or failed acknowledgment/checkpoint does not prevent a safe retry by a new shipper. |
| Object identity | Equal-sized hourly segments from independent writers cannot overwrite each other, with or without compression. |
| Mixed raw/sealed history | Publishing one compressed segment does not hide other raw or late events for that day. |
| Retention | Unacknowledged data survives an outage; acknowledged expired data is pruned; subsequent late arrivals do not inherit deleted segments' offsets. |

The recovery property runs 1,000 generated cases, varying Unicode command
names (including controls), event counts and times, hourly/daily rotation and
JSONL/Zstd storage. A conflicting write/scalar pair straddles midnight. Local
files and handles are released after each case. Exact event contents and
analytics results are checked, not just object counts. The local aggregation,
write attribution and line-usage properties also run 1,000 cases each.

The generated recovery workload is a targeted valid-event subset, not an
exhaustive schema fuzzer. Collections and strings have materialization limits
to keep integration runs bounded. Negative cases here are failures before and
after real S3 PUTs and a failed local checkpoint write. These are deterministic
adapter-boundary faults, not simulated network packet loss. They test the
shipper's response to ambiguous success, not the SDK's transport retry policy.

A one-off scale probe (not retained in the regular suite) also restored
100,000 events from a 15,777,780-byte S3 object and checked each event's ID,
timestamp and character count. It verified all 1,001 keys across a paginated
listing, adjacent-prefix isolation, a missing-object GET error and canceled
LIST rejection. All passed. This is bounded scale evidence, not a claim of
unbounded performance or production throughput.

The S3 properties do not establish AWS IAM/KMS behavior, multi-region
replication, throttling/backoff, multipart correctness, whole-bucket snapshot
isolation, or unbounded-size behavior. These need separate tests as those
features are introduced. Event deduplication remains scoped to a day in
RemoteSource; Service rebuild also deduplicates IDs against the local log.

## Failure ledger

All findings below were observed in executed LocalStack tests before their
fixes, then rerun against the changes. No failure was converted into a skip
or a weaker assertion.

| Executed repro | Finding | Resolution |
| --- | --- | --- |
| `equal-size-segments-and-writers/none` | Only one of four events survived: range keys lacked segment/writer identity. | Keys include segment identity, offsets and payload SHA-256. |
| `equal-size-segments-and-writers/zstd` | Only two of four events survived: different writers reused compressed filenames. | Compressed object keys also include the payload digest. |
| `partial-seal-and-late-event` | One event restored instead of three: any sealed object suppressed all raw objects for its day. | Read both formats and deduplicate event IDs. |
| `outage-beyond-retention`, both formats | The unshipped event disappeared and shipping returned success without attempting PUT. | Pruning requires a matching durable upload checkpoint when remote shipping is enabled. |
| `outage-beyond-retention/none`, post-pruning late append | A same-length replacement inherited the old offset and vanished. | Appends do not reuse segment names present in retained checkpoints. |

Round-trip recovery and lost-acknowledgment tests passed before the fixes.
The first late-arrival fixture used different-length IDs and did not exercise
offset equality; it was corrected to equal-length IDs with an explicit fixture
assertion, after which it exposed the checkpoint reuse bug.

## Tradeoffs

Payload hashing adds one sequential local read per uploaded range but avoids
buffering whole segments or introducing a new persistent writer identity.
Reading raw and compressed objects costs additional GETs until a future
manifest/compaction design can prove which raw objects are fully covered.
During a prolonged outage, local disk usage may exceed configured retention:
preserving unshipped data takes precedence over silently losing it.
