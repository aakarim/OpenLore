# Commit journal property tests

The source under test is `history/commits.jsonl`: file mutations enter the
serialized write applier, which records commits and pre-image metadata; the
analytics `ScalarProcessor` reads those records. This is **not** the separate
`analytics/events` log or its S3 adapter. These tests use real temporary files.

Run the focused suite:

```sh
go test -race ./pkg/openlore -run 'Test(CommitJournal|HistoryCursor|WriteLogAnalyticsReplayProperties)' -count=1
```

## Properties

Each property runs 1,000 Hegel cases in `pkg/openlore/history_blobs_test.go`.

| Test | Contract and competing failure |
| --- | --- |
| `TestCommitJournalReplayProperties` | An independent append-order list must match exact records across lagging reads, repeated same-day/different-day rotations, compression, protected retention, JSON checkpoints and reopen. Also checks seeking and zero caught-up lag. Detects dropped, reordered, duplicated or altered records. |
| `TestCommitJournalPartialRecordProperties` | Arbitrary splits of JSON records, including binary content and Unicode paths, must not advance the cursor before the newline. Completing the record yields the exact record and byte offset. Cancellation must not consume it. |
| `TestWriteLogAnalyticsReplayProperties` | Real create/update/delete sequences must agree with an independent file map and exact byte deltas, hashes, action and attribution. Stale CAS writes must change neither files nor analytics. Varies pre-image capture on/off and checkpoints/restarts; duplicate notifications must not emit again. |

The bounds on record counts and byte/string lengths limit per-case filesystem
and compression work, not the permitted byte values. Separate scale validation
appended and checked **100,000 records**, rotated/compressed at the 50,000-read
checkpoint, reopened, and checked the exact remaining IDs/timestamps/principals
and zero final lag. It passed in 54.94 seconds in the orb; the temporary probe
was removed rather than adding that cost to every test run.

Final validation: `go test -race ./...` passed (the `pkg/openlore` package took
163.695 seconds), `go build ./...` passed, and `git diff --check` was clean.

## Failure ledger

All three findings have standalone regression tests. All three were also run
against the original `history_blobs.go` from Git HEAD using a Go source overlay
with the new tests, and failed there. No S3 code participates in these failures.

1. **Unread records pruned after multiple rotations.** Hegel shrank this to two
   records and two rotations with an idle reader. A protected active-file
   checkpoint was incorrectly mapped to the newest segment, even when it still
   belonged to an earlier segment. Retention now resolves the actual cursor
   position before pruning. The regression covers both an untouched cursor and
   one that has consumed the first record.
2. **Corruption skipped on retry.** A malformed JSON line returned an error, but
   the buffered reader had consumed it without advancing the checkpoint. The
   next call returned the following record with an incorrect offset. Failed
   reads/decodes now rewind to the checkpoint. The regression checks repeated
   failures in active and compressed files, with live and reopened cursors.
3. **Pruned sequence numbers reused.** A subsequent Hegel run exposed this after
   the first fixes. Rotation filled the first vacant filename, so a new segment
   could sort before surviving segments and be immediately pruned. New segment
   sequences now follow the highest retained sequence for that day. Its
   standalone three-rotation reproduction also failed against original HEAD.

## Coverage boundaries and tradeoffs

- New generated coverage targets `Submit` → journal/blob capture → cursor →
  `Process`/`Drain`, cursor `Next`/`Seek`/`Position`, checkpoint marshal/restore,
  and `RotateCommitJournal`. Cursor close is exercised as cleanup; pending-byte
  lag estimates are not modeled, only the caught-up result.
- Existing tests cover batch commits, metadata-only leaves, custom scalar
  providers, replay cutoffs, queue ordering/drain and post-commit failures, blob
  GC and corruption safety. They are retained, not duplicated as properties.
  Authorization (`SubmitIdentity`), arbitrary queued tasks (`Do`), standalone
  blob-store validation/statistics and all VFS mutation variants are not a new
  exhaustive fuzzing target in this journal-focused change.
- Resolving a lagging active checkpoint can scan/decompress retained segments
  during retention. Retaining unread history can exceed nominal retention.
- Corruption now blocks progress instead of being silently skipped; this does
  not repair the journal or add an operator-facing error-reporting API.
- The file write and journal append are **not one atomic transaction**. The
  existing applier logs journal-write failures after a durable file mutation;
  these tests do not establish crash/power-loss or disk-full atomicity.
- No new S3 journal implementation, real AWS validation, multi-process writer
  guarantee or packet-level fault simulation is included.
