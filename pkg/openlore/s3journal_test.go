//go:build integration

package openlore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/pkg/vfs"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"hegel.dev/go/hegel"
)

// Test-only transport experiment, not a scalable journal layout: S3 has no
// ordinary append, so replace one JSONL object with an ETag precondition. Keep
// the real JSONL reader and ScalarProcessor; only the storage boundary changes.
// Segmentation, retention, IAM and production recovery wiring are not modeled.
type s3JournalFixture struct {
	client S3FileClient
	bucket string
	key    string
}

func (j *s3JournalFixture) snapshot(ctx context.Context) ([]byte, *string, error) {
	out, err := j.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(j.bucket), Key: aws.String(j.key)})
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) && api.ErrorCode() == "NoSuchKey" {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	defer out.Body.Close()
	body, err := io.ReadAll(out.Body)
	return body, out.ETag, err
}

func (j *s3JournalFixture) Append(ctx context.Context, record CommitRecord) error {
	body, etag, err := j.snapshot(ctx)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	// Retry identity belongs to the caller. Reusing an ID with different data
	// is not a retry; a lost acknowledgment must not duplicate a record.
	for _, line := range bytes.SplitAfter(body, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var previous CommitRecord
		if line[len(line)-1] != '\n' {
			return io.ErrUnexpectedEOF
		}
		if err := json.Unmarshal(line, &previous); err != nil {
			return err
		}
		if previous.ID == record.ID {
			if !bytes.Equal(bytes.TrimSuffix(line, []byte{'\n'}), encoded) {
				return errors.New("commit ID reused with different record")
			}
			return nil
		}
	}
	input := &s3.PutObjectInput{Bucket: aws.String(j.bucket), Key: aws.String(j.key), Body: bytes.NewReader(append(append(body, encoded...), '\n')), IfMatch: etag}
	if etag == nil {
		input.IfNoneMatch = aws.String("*")
	}
	_, err = j.client.PutObject(ctx, input)
	return err
}

// A disposable local cache lets the production JSONL parser consume remote
// snapshots. Never turn a failed GET into an empty log or advance its checkpoint.
type s3JournalCursor struct {
	journal  *s3JournalFixture
	cache    string
	position HistoryPosition
}

func (c *s3JournalCursor) Next(ctx context.Context) (CommitRecord, bool, error) {
	body, _, err := c.journal.snapshot(ctx)
	if err != nil {
		return CommitRecord{}, false, err
	}
	if int64(len(body)) < c.position.Offset {
		return CommitRecord{}, false, errors.New("remote journal behind checkpoint")
	}
	if err := os.WriteFile(c.cache, body, 0o600); err != nil {
		return CommitRecord{}, false, err
	}
	cursor, err := OpenHistoryCursor(c.cache, c.position)
	if err != nil {
		return CommitRecord{}, false, err
	}
	defer cursor.(io.Closer).Close()
	record, ok, err := cursor.Next(ctx)
	if err == nil && ok {
		c.position = cursor.Position()
	}
	return record, ok, err
}

func (c *s3JournalCursor) Position() HistoryPosition { return c.position }
func (c *s3JournalCursor) RestorePosition(p HistoryPosition) error {
	c.position = p
	return nil
}
func (c *s3JournalCursor) Seek(ctx context.Context, id string) error {
	for {
		record, ok, err := c.Next(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return io.EOF
		}
		if record.ID == id {
			return nil
		}
	}
}

func testS3CommitJournal(t *testing.T, client *s3.Client, bucket string) {
	ctx := context.Background()
	t.Run("write-replay-faults", func(t *testing.T) {
		for _, fault := range []string{"none", "put-before", "put-after"} {
			for _, op := range []string{"create", "update", "delete"} {
				t.Run(fault+"/"+op, func(t *testing.T) {
					hegel.Test(t, func(t *hegel.T) {
						before := hegel.Draw(t, hegel.Binary(0, 128))
						after := append(bytes.Clone(before), hegel.Draw(t, hegel.Binary(1, 64))...)
						dir, err := os.MkdirTemp("", "s3-journal-")
						if err != nil {
							t.Fatal(err)
						}
						defer os.RemoveAll(dir)
						prefix := analytics.NewID()
						journal := &s3JournalFixture{client, bucket, prefix + "/commits.jsonl"}
						fsys, err := NewS3FS(client, bucket, prefix+"/files")
						if err != nil {
							t.Fatal(err)
						}
						if err := fsys.SetWriteable(); err != nil {
							t.Fatal(err)
						}
						log := newWriteLog(fsys, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
						defer log.Close(ctx)
						log.SetCommitJournal(journal, nil, false)
						cursor := &s3JournalCursor{journal: journal, cache: filepath.Join(dir, "commits.jsonl")}
						processor := NewScalarProcessor(cursor, nil, IdentityStoreClassifier(nil)).(*ScalarProcessor)
						const name = "/docs/é %25.md"
						if op != "create" {
							if _, err := log.Submit(ctx, Attribution{Principal: "person"}, vfs.ChangeSet{Target: name, Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: before}}); err != nil {
								t.Fatal(err)
							}
							if events := processor.Drain(ctx); len(events) != 1 || events[0].Fields["delta"].(map[string]any)["bytes"] != float64(len(before)) {
								t.Fatalf("initial replay: %+v", events)
							}
						} else {
							before = nil
						}
						injected := errors.New("S3 journal unavailable")
						hit := false
						faulty := *journal
						faulty.client = observedS3Client{S3FileClient: client, put: func(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
							hit = true
							if fault == "put-before" {
								return nil, injected
							}
							out, err := client.PutObject(ctx, in, opts...)
							if err == nil && fault == "put-after" {
								return nil, injected
							}
							return out, err
						}}
						var attempted CommitRecord
						log.SetCommitJournal(journalAppendFunc(func(ctx context.Context, r CommitRecord) error {
							attempted = r
							return faulty.Append(ctx, r)
						}), nil, false)
						change := vfs.ChangeSet{Target: name, Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: after}}
						if op == "delete" {
							change.Action, change.Write, after = vfs.ChangeActionRemove, nil, nil
						}
						if _, err := log.Submit(ctx, Attribution{Principal: "person"}, change); err != nil {
							t.Fatal(err)
						}
						if err := log.Close(ctx); err != nil {
							t.Fatal(err)
						}
						if !hit || attempted.ID == "" {
							t.Fatal("journal boundary not exercised")
						}
						got, err := fsys.ReadFile(name)
						if op == "delete" {
							if !errors.Is(err, os.ErrNotExist) {
								t.Fatalf("delete not visible: %v", err)
							}
						} else if err != nil || !bytes.Equal(got, after) {
							t.Fatalf("file=%x want=%x err=%v", got, after, err)
						}
						// GET failures must not consume history; the next successful GET
						// must still yield the same commit to the actual analytics processor.
						position := cursor.Position()
						journal.client = observedS3Client{S3FileClient: client, get: func(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
							return nil, injected
						}}
						if _, ok, err := cursor.Next(ctx); !errors.Is(err, injected) || ok || cursor.Position() != position {
							t.Fatalf("failed GET consumed history: ok=%v err=%v", ok, err)
						}
						journal.client = client
						checkEvents := func(events []analytics.Event, count int) {
							if len(events) != count {
								t.Fatalf("%s/%s replay count=%d want=%d", fault, op, len(events), count)
							}
							if count == 1 {
								fields := events[0].Fields
								if fields["commit_id"] != attempted.ID || fields["path"] != name || fields["action"] != op || fields["delta"].(map[string]any)["bytes"] != float64(len(after)-len(before)) {
									t.Fatalf("incorrect replay: %+v", events[0])
								}
							}
						}
						count := 1
						if fault == "put-before" {
							count = 0 // Existing dual-write gap: visible file, missing journal.
						}
						checkEvents(processor.Drain(ctx), count)
						checkpoint, err := processor.MarshalCheckpointState()
						if err != nil {
							t.Fatal(err)
						}
						// Explicitly retry the retained record, not Submit (which creates a
						// new ID). This is a sink contract, NOT automatic production repair.
						for range 2 {
							if err := journal.Append(ctx, attempted); err != nil {
								t.Fatal(err)
							}
						}
						conflict := attempted
						conflict.Attribution.Principal = "different-person"
						if err := journal.Append(ctx, conflict); err == nil {
							t.Fatal("accepted changed record with same ID")
						}
						// Remove all local cached bytes. A restored checkpoint must work
						// from S3 alone, with the journal's logical segment name unchanged.
						if err := os.Remove(cursor.cache); err != nil {
							t.Fatal(err)
						}
						cursor = &s3JournalCursor{journal: journal, cache: filepath.Join(dir, "commits.jsonl")}
						processor = NewScalarProcessor(cursor, nil, IdentityStoreClassifier(nil)).(*ScalarProcessor)
						if err := processor.RestoreCheckpointState(checkpoint); err != nil {
							t.Fatal(err)
						}
						checkEvents(processor.Drain(ctx), 1-count)
						// Inspect the cursor too: processor deduplication must not conceal
						// duplicate physical journal records after an ambiguous PUT.
						raw := &s3JournalCursor{journal: journal, cache: filepath.Join(dir, "raw.jsonl")}
						var ids []string
						for {
							r, ok, err := raw.Next(ctx)
							if err != nil {
								t.Fatal(err)
							}
							if !ok {
								break
							}
							if r.ID == attempted.ID {
								if err := checkReplayedCommit(r, attempted); err != nil {
									t.Fatal(err)
								}
							}
							ids = append(ids, r.ID)
						}
						want := 2
						if op == "create" {
							want = 1
						}
						if len(ids) != want || ids[len(ids)-1] != attempted.ID {
							t.Fatalf("physical journal IDs=%v want %d records ending in %s", ids, want, attempted.ID)
						}
					}, hegel.WithTestCases(1000))
				})
			}
		}
	})

	t.Run("incomplete-corrupt-and-missing-snapshots", func(t *testing.T) {
		journal := &s3JournalFixture{client, bucket, analytics.NewID() + "/commits.jsonl"}
		publish := func(body []byte) {
			t.Helper()
			if _, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(journal.key), Body: bytes.NewReader(body)}); err != nil {
				t.Fatal(err)
			}
		}
		for _, damaged := range []string{"{\"id\":", "not-json\n"} {
			t.Run(damaged, func(t *testing.T) {
				publish([]byte(damaged))
				cursor := &s3JournalCursor{journal: journal, cache: filepath.Join(t.TempDir(), "commits.jsonl")}
				for range 2 {
					_, ok, err := cursor.Next(ctx)
					if ok || (err != nil) != (damaged == "not-json\n") || cursor.Position() != (HistoryPosition{}) {
						t.Fatalf("damaged snapshot consumed: ok=%v err=%v position=%+v", ok, err, cursor.Position())
					}
				}
				if err := journal.Append(ctx, CommitRecord{ID: "next"}); err == nil {
					t.Fatal("append accepted damaged history")
				}
				body, _, err := journal.snapshot(ctx)
				if err != nil || string(body) != damaged {
					t.Fatalf("damaged history overwritten: %q %v", body, err)
				}
				publish([]byte("{\"id\":\"repaired\"}\n"))
				if r, ok, err := cursor.Next(ctx); err != nil || !ok || r.ID != "repaired" {
					t.Fatalf("repair skipped: %+v %v %v", r, ok, err)
				}
				position := cursor.Position()
				if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(journal.key)}); err != nil {
					t.Fatal(err)
				}
				if _, ok, err := cursor.Next(ctx); err == nil || ok || cursor.Position() != position {
					t.Fatalf("missing history accepted at nonzero checkpoint: %v %v", ok, err)
				}
			})
		}
		missingBucket := &s3JournalFixture{client, "missing-journal-bucket", journal.key}
		if err := missingBucket.Append(ctx, CommitRecord{ID: "x"}); err == nil {
			t.Fatal("missing bucket treated as empty journal")
		}
	})

	t.Run("stale-append-does-not-overwrite", func(t *testing.T) {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprint(existing), func(t *testing.T) {
				journal := &s3JournalFixture{client, bucket, analytics.NewID() + "/commits.jsonl"}
				record := func(id string) CommitRecord { return CommitRecord{ID: id, Time: time.Unix(1, 0).UTC()} }
				if existing {
					if err := journal.Append(ctx, record("base")); err != nil {
						t.Fatal(err)
					}
				}
				// Insert a competing append after the stale writer's GET and
				// before its PUT; deterministic for both initial and existing keys.
				stale := *journal
				stale.client = observedS3Client{S3FileClient: client, put: func(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
					if err := journal.Append(ctx, record("winner")); err != nil {
						return nil, err
					}
					return client.PutObject(ctx, in, opts...)
				}}
				var api smithy.APIError
				if err := stale.Append(ctx, record("retry")); !errors.As(err, &api) || api.ErrorCode() != "PreconditionFailed" {
					t.Fatalf("stale append: %v", err)
				}
				if err := journal.Append(ctx, record("retry")); err != nil {
					t.Fatal(err)
				}
				cursor := &s3JournalCursor{journal: journal, cache: filepath.Join(t.TempDir(), "commits.jsonl")}
				ids := []string{"winner", "retry"}
				if existing {
					ids = append([]string{"base"}, ids...)
				}
				for _, id := range ids {
					if r, ok, err := cursor.Next(ctx); err != nil || !ok || r.ID != id {
						t.Fatalf("next=%s ok=%v err=%v want=%s", r.ID, ok, err, id)
					}
				}
				if _, ok, err := cursor.Next(ctx); ok || err != nil {
					t.Fatalf("unexpected tail: ok=%v err=%v", ok, err)
				}
				if err := cursor.RestorePosition(HistoryPosition{}); err != nil {
					t.Fatal(err)
				}
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				if _, ok, err := cursor.Next(cancelled); !errors.Is(err, context.Canceled) || ok || cursor.Position() != (HistoryPosition{}) {
					t.Fatalf("cancellation consumed history: ok=%v err=%v", ok, err)
				}
				if err := cursor.Seek(ctx, "winner"); err != nil {
					t.Fatal(err)
				}
				if r, ok, err := cursor.Next(ctx); err != nil || !ok || r.ID != "retry" {
					t.Fatalf("seek skipped successor: %+v %v %v", r, ok, err)
				}
				if err := cursor.Seek(ctx, "missing"); !errors.Is(err, io.EOF) {
					t.Fatalf("seek missing ID: %v", err)
				}
			})
		}
	})
}
