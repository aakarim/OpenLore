//go:build integration

package openlore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
	"github.com/aakarim/go-openlore/pkg/vfs"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/localstack"
	"hegel.dev/go/hegel"
)

type journalAppendFunc func(context.Context, CommitRecord) error

func (f journalAppendFunc) Append(ctx context.Context, r CommitRecord) error { return f(ctx, r) }

// Keep every fault/operation combination in PRs; opt into deeper payload sampling locally.
func s3TestCases() int {
	if os.Getenv("OPENLORE_EXHAUSTIVE") == "1" {
		return 1000
	}
	return 25
}

// Faults wrap real S3 requests; "after" means the remote operation completed
// but the caller did not receive its acknowledgment. No in-memory S3 fake.
type observedS3Client struct {
	S3FileClient
	put func(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	get func(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

func (c observedS3Client) PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if c.put != nil {
		return c.put(ctx, in, opts...)
	}
	return c.S3FileClient.PutObject(ctx, in, opts...)
}
func (c observedS3Client) GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if c.get != nil {
		return c.get(ctx, in, opts...)
	}
	return c.S3FileClient.GetObject(ctx, in, opts...)
}

func TestS3Filesystem(t *testing.T) {
	if testing.Short() {
		t.Skip("S3 integration tests are excluded in short mode")
	}
	ctx := context.Background()
	container, err := localstack.Run(ctx, "localstack/localstack:4.9.2", testcontainers.WithEnv(map[string]string{"SERVICES": "s3"}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Error(err)
		}
	})
	endpoint, err := container.PortEndpoint(ctx, "4566/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}
	client := s3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")}, func(o *s3.Options) {
		o.BaseEndpoint, o.UsePathStyle, o.RetryMaxAttempts = aws.String(endpoint), true, 1
	})
	const bucket = "filesystem-tests"
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("remote-commit-journal", func(t *testing.T) {
		testS3CommitJournal(t, client, bucket)
	})

	t.Run("queued-canonical-path-change", func(t *testing.T) {
		for _, changed := range []bool{false, true} {
			t.Run(fmt.Sprintf("changed=%v", changed), func(t *testing.T) {
				hegel.Test(t, func(t *hegel.T) {
					roots := hegel.Draw(t, hegel.SampledFrom([][2]string{
						{"/docs", "/docs-next"}, {"/docs-next", "/docs"}, {"/user/é %25", "/channel/new"},
					}))
					rel := hegel.Draw(t, hegel.SampledFrom([]string{"note.bin", "sub/leaf.bin", "é %25/nested/leaf.bin"}))
					exists := hegel.Draw(t, hegel.Booleans())
					viaAlias := hegel.Draw(t, hegel.Booleans())
					before := hegel.Draw(t, hegel.Binary(0, 64))
					after := append(bytes.Clone(before), hegel.Draw(t, hegel.Binary(1, 32))...)
					destinationBefore := append([]byte("destination:"), after...)
					oldPath, newPath := path.Join(roots[0], rel), path.Join(roots[1], rel)
					prefix := analytics.NewID()
					fsys, err := NewS3FS(client, bucket, prefix)
					if err != nil {
						t.Fatal(err)
					}
					if err := fsys.SetWriteable(); err != nil {
						t.Fatal(err)
					}
					if exists {
						if _, err := fsys.WriteFileAtomic(oldPath, before, vfs.WriteOpts{}); err != nil {
							t.Fatal(err)
						}
					}
					if _, err := fsys.WriteFileAtomic(newPath, destinationBefore, vfs.WriteOpts{}); err != nil {
						t.Fatal(err)
					}
					dir, err := os.MkdirTemp("", "s3-canonical-reload-")
					if err != nil {
						t.Fatal(err)
					}
					defer os.RemoveAll(dir)
					auth := serverAuth(map[string]config.DocsetSpec{"docs": {
						Paths: []config.PathMapping{{Source: roots[0]}}, Aliases: []string{"/shortcut"},
					}}, nil)
					authFile := filepath.Join(dir, "lore.json")
					encoded, err := json.Marshal(auth)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(authFile, encoded, 0o600); err != nil {
						t.Fatal(err)
					}
					server, err := NewServerWithRootFS(fsys, WithReadonly(false), WithLogger(logger), config.WithDataDir(filepath.Join(dir, "data")), config.WithAuthFile(authFile))
					if err != nil {
						t.Fatal(err)
					}
					defer server.Shutdown(ctx)
					admitted, release := make(chan struct{}), make(chan struct{})
					var gate, unblock sync.Once
					server.writeMW = append(server.writeMW, func(next WriteHandler) WriteHandler {
						return func(ctx context.Context, op WriteOp) (WriteResult, error) {
							// Resolve the requested path before reload, then submit
							// the admitted write to the ordered applier afterward.
							gate.Do(func() { close(admitted); <-release })
							return next(ctx, op)
						}
					})
					writer := server.buildSessionFS(testIdentity("alice")).(vfs.WritableFS)
					requested := oldPath
					if viaAlias {
						requested = path.Join("/shortcut", rel)
					}
					result, done := make(chan error, 1), make(chan struct{})
					defer func() {
						unblock.Do(func() { close(release) })
						select {
						case <-done:
						case <-time.After(10 * time.Second):
							t.Error("pending write did not finish")
						}
					}()
					go func() {
						defer close(done)
						_, err := writer.WriteFileAtomic(requested, after, vfs.WriteOpts{})
						result <- err
					}()
					select {
					case <-admitted:
					case <-time.After(10 * time.Second):
						t.Fatal("write did not reach admission barrier")
					}
					ds := auth.Docsets["docs"]
					if changed {
						ds.Paths = []config.PathMapping{{Source: roots[1]}}
						ds.Aliases = append(ds.Aliases, roots[0])
					} else {
						// An unrelated alias update must not reject valid work.
						ds.Aliases = append(ds.Aliases, "/extra")
					}
					auth.Docsets["docs"] = ds
					encoded, err = json.Marshal(auth)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(authFile, encoded, 0o600); err != nil {
						t.Fatal(err)
					}
					if err := server.Reload(cmds.JobAttribution{Principal: "alice"}); err != nil {
						t.Fatal(err)
					}
					unblock.Do(func() { close(release) })
					select {
					case err := <-result:
						if changed && !errors.Is(err, os.ErrPermission) || !changed && err != nil {
							t.Fatalf("changed=%v: pending write returned %v", changed, err)
						}
					case <-time.After(10 * time.Second):
						t.Fatal("pending write did not return")
					}
					// A new backend instance reads durable S3 state, not a
					// session cache. Rejection may neither write the old path
					// nor silently redirect the bytes to the new destination.
					reopened, err := NewS3FS(client, bucket, prefix)
					if err != nil {
						t.Fatal(err)
					}
					got, err := reopened.ReadFile(oldPath)
					if changed && !exists {
						if !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("rejected create persisted at %s: %x %v", oldPath, got, err)
						}
					} else {
						want := after
						if changed {
							want = before
						}
						if err != nil || !bytes.Equal(got, want) {
							t.Fatalf("old S3 path %s: got=%x want=%x err=%v", oldPath, got, want, err)
						}
					}
					if got, err := reopened.ReadFile(newPath); err != nil || !bytes.Equal(got, destinationBefore) {
						t.Fatalf("new S3 destination changed: %x %v", got, err)
					}
					page, err := server.history.Query(ctx, HistoryQuery{Roots: roots[:]})
					wantRecords := 1
					if changed {
						wantRecords = 0
					}
					if err != nil || len(page.Records) != wantRecords || !changed && (page.Records[0].FileKey != oldPath || page.Records[0].ContentHash != hashContent(after)) {
						t.Fatalf("rejected/accepted write history: %+v %v", page, err)
					}
					if changed {
						// Resubmission through the existing session must resolve
						// the new canonical root and succeed, not ban all writes.
						if _, err := writer.WriteFileAtomic(requested, after, vfs.WriteOpts{}); err != nil {
							t.Fatal("retry through updated alias:", err)
						}
						if got, err := reopened.ReadFile(newPath); err != nil || !bytes.Equal(got, after) {
							t.Fatalf("retry missed new S3 destination: %x %v", got, err)
						}
					}
				}, hegel.WithTestCases(s3TestCases()))
			})
		}
	})

	t.Run("file-journal-fault-boundaries", func(t *testing.T) {
		for _, fault := range []string{"none", "blob-before", "blob-after", "manifest-before", "manifest-after", "journal-before", "journal-after"} {
			for _, op := range []string{"create", "update", "delete"} {
				t.Run(fault+"/"+op, func(t *testing.T) {
					hegel.Test(t, func(t *hegel.T) {
						before := hegel.Draw(t, hegel.Binary(1, 64))
						after := append(bytes.Clone(before), hegel.Draw(t, hegel.Binary(1, 32))...)
						prefix := analytics.NewID()
						dir, err := os.MkdirTemp("", "s3-file-journal-")
						if err != nil {
							t.Fatal(err)
						}
						defer os.RemoveAll(dir)
						journal := fileCommitJournal(filepath.Join(dir, "commits.jsonl"))
						blobs, err := OpenBlobStore(filepath.Join(dir, "objects"))
						if err != nil {
							t.Fatal(err)
						}
						fsys, err := NewS3FS(client, bucket, prefix)
						if err != nil {
							t.Fatal(err)
						}
						if err := fsys.SetWriteable(); err != nil {
							t.Fatal(err)
						}
						log := newWriteLog(fsys, nil, logger, 1)
						defer func() { log.Close(ctx) }()
						log.SetCommitJournal(journal, blobs, true)
						const name = "/docs/é %25.md"
						initialRecords := 0
						if op != "create" {
							if _, err := log.Submit(ctx, Attribution{Principal: "person"}, vfs.ChangeSet{Target: name, Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: before}}); err != nil {
								t.Fatal(err)
							}
							initialRecords = 1
						}
						if err := log.Close(ctx); err != nil {
							t.Fatal(err)
						}
						injected := errors.New("injected storage boundary failure")
						hit := false
						faultClient := observedS3Client{S3FileClient: client}
						faultClient.put = func(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
							key := aws.ToString(in.Key)
							matches := strings.HasPrefix(fault, "blob-") && strings.Contains(key, "/objects/") || strings.HasPrefix(fault, "manifest-") && strings.HasSuffix(key, "/manifest.json")
							if !matches {
								return client.PutObject(ctx, in, opts...)
							}
							hit = true
							if strings.HasSuffix(fault, "-after") {
								if _, err := client.PutObject(ctx, in, opts...); err != nil {
									return nil, err
								}
							}
							return nil, injected
						}
						fsys, err = NewS3FS(faultClient, bucket, prefix)
						if err != nil {
							t.Fatal(err)
						}
						if err := fsys.SetWriteable(); err != nil {
							t.Fatal(err)
						}
						log = newWriteLog(fsys, nil, logger, 1)
						log.SetCommitJournal(journalAppendFunc(func(ctx context.Context, record CommitRecord) error {
							if fault == "journal-before" {
								hit = true
								return injected
							}
							if err := journal.Append(ctx, record); err != nil {
								return err
							}
							if fault == "journal-after" {
								hit = true
								return injected
							}
							return nil
						}), blobs, true)
						cs := vfs.ChangeSet{Target: name, Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: after}}
						switch op {
						case "create":
							cs.Write.Opts.IfNoneMatch = true
						case "update":
							hash := fmt.Sprintf("%x", sha256.Sum256(before))
							cs.Write.Opts.IfMatch = &hash
						case "delete":
							cs.Action, cs.Write = vfs.ChangeActionRemove, nil
						}
						_, submitErr := log.Submit(ctx, Attribution{Principal: "person"}, cs)
						if err := log.Close(ctx); err != nil {
							t.Fatal(err)
						}
						// Deletes do not PUT content blobs. They must remain a successful
						// control when only a content-upload fault has been armed.
						expectedHit := fault != "none" && !(op == "delete" && strings.HasPrefix(fault, "blob-"))
						if hit != expectedHit {
							t.Fatalf("fault %s/%s hit=%v want=%v", fault, op, hit, expectedHit)
						}
						failedAtS3 := expectedHit && !strings.HasPrefix(fault, "journal-")
						if failedAtS3 != errors.Is(submitErr, injected) || !failedAtS3 && submitErr != nil {
							t.Fatalf("%s/%s submit=%v", fault, op, submitErr)
						}
						published := !failedAtS3 || fault == "manifest-after"
						recorded := !failedAtS3 && fault != "journal-before"
						// Recreate both sides: restart is not a reconciliation mechanism.
						fsys, err = NewS3FS(client, bucket, prefix)
						if err != nil {
							t.Fatal(err)
						}
						if err := fsys.SetWriteable(); err != nil {
							t.Fatal(err)
						}
						wantExists := op != "create"
						wantBytes := before
						if published {
							wantExists, wantBytes = op != "delete", after
						}
						got, readErr := fsys.ReadFile(name)
						if wantExists {
							if readErr != nil || !bytes.Equal(got, wantBytes) {
								t.Fatalf("%s/%s file=%x want=%x err=%v", fault, op, got, wantBytes, readErr)
							}
						} else if !errors.Is(readErr, os.ErrNotExist) {
							t.Fatalf("%s/%s expected absent file: %v", fault, op, readErr)
						}
						cursor, err := OpenHistoryCursor(string(journal), HistoryPosition{})
						if err != nil {
							t.Fatal(err)
						}
						defer cursor.(io.Closer).Close()
						var records []CommitRecord
						for {
							r, ok, err := cursor.Next(ctx)
							if err != nil {
								t.Fatal(err)
							}
							if !ok {
								break
							}
							records = append(records, r)
						}
						wantRecords := initialRecords
						if recorded {
							wantRecords++
						}
						if len(records) != wantRecords {
							t.Fatalf("%s/%s journal=%d want=%d", fault, op, len(records), wantRecords)
						}
						if recorded {
							last := records[len(records)-1]
							if len(last.Leaves) != 1 || last.Leaves[0].Target != name || last.Leaves[0].Action != cs.Action {
								t.Fatalf("incorrect committed leaf: %#v", last)
							}
						}
						if err := cursor.(historyCursorRestorer).RestorePosition(HistoryPosition{}); err != nil {
							t.Fatal(err)
						}
						processor := NewScalarProcessor(cursor, blobs, IdentityStoreClassifier(nil)).(*ScalarProcessor)
						events := processor.Drain(ctx)
						var analyticalBytes float64
						for _, e := range events {
							analyticalBytes += e.Fields["delta"].(map[string]any)["bytes"].(float64)
						}
						wantAnalyticsBytes := 0
						if initialRecords > 0 {
							wantAnalyticsBytes = len(before)
						}
						if recorded {
							wantAnalyticsBytes = len(after)
							if op == "delete" {
								wantAnalyticsBytes = 0
							}
						}
						if len(events) != wantRecords || analyticalBytes != float64(wantAnalyticsBytes) {
							t.Fatalf("%s/%s analytics=%v/%d want=%d/%d", fault, op, analyticalBytes, len(events), wantAnalyticsBytes, wantRecords)
						}
						visibleBytes := 0
						if wantExists {
							visibleBytes = len(wantBytes)
						}
						if drift := analyticalBytes != float64(visibleBytes); drift != (published && !recorded) {
							t.Fatalf("%s/%s drift=%v, analytics=%v S3=%d", fault, op, drift, analyticalBytes, visibleBytes)
						}
						// Retry only an operation reported as failed. Pre-publication failures
						// recover; lost manifest acknowledgments cannot be repaired by blindly
						// retrying the original create/CAS/delete operation.
						if failedAtS3 {
							log = newWriteLog(fsys, nil, logger, 1)
							log.SetCommitJournal(journal, blobs, true)
							_, retryErr := log.Submit(ctx, Attribution{Principal: "person"}, cs)
							if err := log.Close(ctx); err != nil {
								t.Fatal(err)
							}
							if published {
								if op == "delete" {
									if !errors.Is(retryErr, os.ErrNotExist) {
										t.Fatalf("delete retry=%v", retryErr)
									}
								} else {
									var precondition *vfs.PreconditionError
									if !errors.As(retryErr, &precondition) {
										t.Fatalf("CAS retry=%v", retryErr)
									}
								}
							} else if retryErr != nil {
								t.Fatalf("retry before publication: %v", retryErr)
							}
							extra := processor.Drain(ctx)
							if published && len(extra) != 0 || !published && len(extra) != 1 {
								t.Fatalf("%s/%s retry events=%d", fault, op, len(extra))
							}
							if !published {
								wantDelta := len(after)
								if op != "create" {
									wantDelta -= len(before)
								}
								if op == "delete" {
									wantDelta = -len(before)
								}
								if extra[0].Fields["delta"].(map[string]any)["bytes"] != float64(wantDelta) {
									t.Fatalf("retry delta=%#v want=%d", extra[0].Fields, wantDelta)
								}
							}
							got, err := fsys.ReadFile(name)
							if op == "delete" {
								if !errors.Is(err, os.ErrNotExist) {
									t.Fatalf("retry delete: %v", err)
								}
							} else if err != nil || !bytes.Equal(got, after) {
								t.Fatalf("retry file=%x want=%x err=%v", got, after, err)
							}
						}
					}, hegel.WithTestCases(s3TestCases()))
				})
			}
		}
	})

	t.Run("server-root-interface", func(t *testing.T) {
		prefix := analytics.NewID()
		fsys, err := NewS3FS(client, bucket, prefix)
		if err != nil {
			t.Fatal(err)
		}
		server := newSeamServer(t, fsys)
		defer server.Shutdown(ctx)
		content := []byte("S3 through the server write log")
		result, err := server.CommitChangeSet(ctx, Attribution{Principal: "person"}, vfs.ChangeSet{Target: "/doc.md", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: content}})
		if err != nil || result.Hash != fmt.Sprintf("%x", sha256.Sum256(content)) {
			t.Fatalf("server commit=%#v err=%v", result, err)
		}
		reopened, _ := NewS3FS(client, bucket, prefix)
		if got, err := reopened.ReadFile("/doc.md"); err != nil || !bytes.Equal(got, content) {
			t.Fatalf("server S3 content=%q err=%v", got, err)
		}
		cursor, err := OpenHistoryCursor(server.historyPath, HistoryPosition{})
		if err != nil {
			t.Fatal(err)
		}
		defer cursor.(io.Closer).Close()
		if record, ok, err := cursor.Next(ctx); err != nil || !ok || record.Hash != result.Hash || !bytes.Equal(record.ChangeSet.Write.Bytes, content) {
			t.Fatalf("server journal=%#v ok=%v err=%v", record, ok, err)
		}
	})

	t.Run("readonly-drains-publication", func(t *testing.T) {
		reached, release := make(chan struct{}), make(chan struct{})
		t.Cleanup(func() {
			select {
			case <-release:
			default:
				close(release)
			}
		})
		blocked := observedS3Client{S3FileClient: client, put: func(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
			if strings.HasSuffix(aws.ToString(in.Key), "/manifest.json") {
				close(reached)
				<-release
			}
			return client.PutObject(ctx, in, opts...)
		}}
		fsys, _ := NewS3FS(blocked, bucket, analytics.NewID())
		_ = fsys.SetWriteable()
		writeDone, readonlyDone, started := make(chan error, 1), make(chan error, 1), make(chan struct{})
		go func() { _, err := fsys.WriteFileAtomic("/file", []byte("complete"), vfs.WriteOpts{}); writeDone <- err }()
		select {
		case <-reached:
		case <-time.After(10 * time.Second):
			t.Fatal("write did not reach publication")
		}
		go func() { close(started); readonlyDone <- fsys.SetReadonly() }()
		<-started
		select {
		case err := <-readonlyDone:
			t.Fatalf("readonly returned before in-flight publication: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
		close(release)
		if err := <-writeDone; err != nil {
			t.Fatal(err)
		}
		if err := <-readonlyDone; err != nil {
			t.Fatal(err)
		}
		if got, err := fsys.ReadFile("/file"); err != nil || string(got) != "complete" {
			t.Fatalf("drained write=%q err=%v", got, err)
		}
		if _, err := fsys.WriteFileAtomic("/later", nil, vfs.WriteOpts{}); !errors.Is(err, vfs.ErrReadOnly) {
			t.Fatalf("post-drain write=%v", err)
		}
	})

	t.Run("conditional-manifest-publication", func(t *testing.T) {
		prefix := analytics.NewID()
		base, _ := NewS3FS(client, bucket, prefix)
		if err := base.SetWriteable(); err != nil {
			t.Fatal(err)
		}
		if _, err := base.WriteFileAtomic("/base", []byte("base"), vfs.WriteOpts{}); err != nil {
			t.Fatal(err)
		}
		ready, release := make(chan struct{}, 2), make(chan struct{})
		blocked := observedS3Client{S3FileClient: client, get: func(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			out, err := client.GetObject(ctx, in, opts...)
			if err == nil && strings.HasSuffix(aws.ToString(in.Key), "/manifest.json") {
				ready <- struct{}{}
				<-release
			}
			return out, err
		}}
		type result struct {
			name string
			err  error
		}
		results := make(chan result, 2)
		for _, name := range []string{"/one", "/two"} {
			go func() {
				fsys, _ := NewS3FS(blocked, bucket, prefix)
				_ = fsys.SetWriteable()
				_, err := fsys.WriteFileAtomic(name, []byte(name), vfs.WriteOpts{IfNoneMatch: true})
				results <- result{name, err}
			}()
		}
		for range 2 {
			select {
			case <-ready:
			case <-time.After(10 * time.Second):
				close(release)
				t.Fatal("concurrent reads did not reach barrier")
			}
		}
		close(release)
		var winner, loser string
		for range 2 {
			r := <-results
			if r.err == nil {
				if winner != "" {
					t.Fatal("both stale manifests published")
				}
				winner = r.name
			} else {
				var api smithy.APIError
				if !errors.As(r.err, &api) || api.ErrorCode() != "PreconditionFailed" {
					t.Fatalf("unexpected conflict: %v", r.err)
				}
				loser = r.name
			}
		}
		if winner == "" || loser == "" {
			t.Fatalf("winner=%q loser=%q", winner, loser)
		}
		if got, err := base.ReadFile(winner); err != nil || string(got) != winner {
			t.Fatalf("winner: %q %v", got, err)
		}
		if _, err := base.ReadFile(loser); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("loser visible: %v", err)
		}
		if _, err := base.WriteFileAtomic(loser, []byte(loser), vfs.WriteOpts{IfNoneMatch: true}); err != nil {
			t.Fatal(err)
		}
		if entries, err := base.ReadDir("/"); err != nil || len(entries) != 3 {
			t.Fatalf("lost concurrent file: %#v %v", entries, err)
		}
	})

	t.Run("namespace-contract", func(t *testing.T) {
		prefix := analytics.NewID()
		fsys, _ := NewS3FS(client, bucket, prefix)
		if _, err := fsys.WriteFileAtomic("/a", nil, vfs.WriteOpts{}); !errors.Is(err, vfs.ErrReadOnly) {
			t.Fatalf("readonly write=%v", err)
		}
		if err := fsys.SetWriteable(); err != nil {
			t.Fatal(err)
		}
		if err := fsys.Mkdir("/missing/child"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing parent=%v", err)
		}
		if err := fsys.MkdirAll("/a/nested"); err != nil {
			t.Fatal(err)
		}
		if err := fsys.Mkdir("/a/empty"); err != nil {
			t.Fatal(err)
		}
		if err := fsys.Remove("/a/empty"); err != nil {
			t.Fatal(err)
		}
		if _, err := fsys.WriteFileAtomic("/a/nested/file", []byte("abc"), vfs.WriteOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := fsys.WriteFileAtomic("/adjacent/file", []byte("keep"), vfs.WriteOpts{}); err != nil {
			t.Fatal(err)
		}
		if st, err := fsys.Stat("/a/nested/file"); err != nil || st.FileSize != 3 || st.Dir {
			t.Fatalf("stat=%#v %v", st, err)
		}
		if err := fsys.Remove("/a"); err == nil {
			t.Fatal("removed nonempty directory")
		}
		if _, err := fsys.ReadFile("/a"); err == nil {
			t.Fatal("read directory as file")
		}
		if _, err := fsys.ReadDir("/a/nested/file"); err == nil {
			t.Fatal("listed file as directory")
		}
		snapshot := &vfs.TreeSnapshot{Root: "/a", Ops: []vfs.TreeOp{{RelPath: ".", Kind: "dir"}, {RelPath: "nested", Kind: "dir"}, {RelPath: "nested/file", Kind: "file", Size: 3, Hash: fmt.Sprintf("%x", sha256.Sum256([]byte("abc")))}}}
		if _, err := fsys.WriteFileAtomic("/a/added", nil, vfs.WriteOpts{}); err != nil {
			t.Fatal(err)
		}
		if empty, err := fsys.ReadFile("/a/added"); err != nil || len(empty) != 0 {
			t.Fatalf("empty file=%x err=%v", empty, err)
		}
		var stale *vfs.TreeStaleError
		if err := fsys.RemoveAll("/a", vfs.RemoveOpts{Expected: snapshot}); !errors.As(err, &stale) {
			t.Fatalf("stale delete=%v", err)
		}
		if err := fsys.Remove("/a/added"); err != nil {
			t.Fatal(err)
		}
		if err := fsys.RemoveAll("/a", vfs.RemoveOpts{Expected: snapshot}); err != nil {
			t.Fatal(err)
		}
		if _, err := fsys.Stat("/a"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("tree still visible: %v", err)
		}
		if got, err := fsys.ReadFile("/adjacent/file"); err != nil || string(got) != "keep" {
			t.Fatalf("adjacent tree: %q %v", got, err)
		}
		other, _ := NewS3FS(client, bucket, prefix+"-other")
		if _, err := other.ReadFile("/adjacent/file"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("prefix leaked: %v", err)
		}
		for _, name := range []string{"../escape", "/.lore/config", "/nul\x00file", "/bad\\file", "/.openlore-trash", "/.openlore-trash/file", "/.openlore-trash/.lore/config.yaml"} {
			if _, err := fsys.WriteFileAtomic(name, nil, vfs.WriteOpts{}); err == nil {
				t.Fatalf("accepted path %q", name)
			}
			if _, err := fsys.ReadFile(name); err == nil {
				t.Fatalf("read path %q", name)
			}
			if _, err := fsys.Stat(name); err == nil {
				t.Fatalf("stat accepted path %q", name)
			}
			if _, err := fsys.ReadDir(name); err == nil {
				t.Fatalf("listed path %q", name)
			}
			if err := fsys.MkdirAll(name); err == nil {
				t.Fatalf("created directory %q", name)
			}
			if err := fsys.RemoveAll(name, vfs.RemoveOpts{}); err == nil {
				t.Fatalf("removed forbidden path %q", name)
			}
		}
		if err := fsys.RemoveAll("/", vfs.RemoveOpts{}); err == nil {
			t.Fatal("removed docset root")
		}
		if _, err := fsys.WriteFileAtomic("/protected/.lore/config.yaml", []byte("version: 1"), vfs.WriteOpts{}); err != nil {
			t.Fatal(err)
		}
		if entries, err := fsys.ReadDir("/protected"); err != nil || len(entries) != 1 || entries[0].FileName != ".lore" || !entries[0].Dir {
			t.Fatalf("config directory missing: %#v %v", entries, err)
		}
		if err := fsys.RemoveAll("/protected", vfs.RemoveOpts{}); err == nil {
			t.Fatal("deleted hidden metadata")
		}
		if got, err := fsys.ReadFile("/protected/.lore/config.yaml"); err != nil || string(got) != "version: 1" {
			t.Fatalf("hidden metadata changed: %q %v", got, err)
		}
		if err := fsys.SetReadonly(); err != nil {
			t.Fatal(err)
		}
		if err := fsys.Remove("/adjacent/file"); !errors.Is(err, vfs.ErrReadOnly) {
			t.Fatalf("readonly delete=%v", err)
		}
	})

	t.Run("config-directory-read-contract", func(t *testing.T) {
		fsys, err := NewS3FS(client, bucket, analytics.NewID())
		if err != nil {
			t.Fatal(err)
		}
		if err := fsys.SetWriteable(); err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{"/.lore", "/docs/.lore"} {
			if _, err := fsys.Stat(dir); err == nil {
				t.Fatalf("config directory exists without config: %s", dir)
			}
			if _, err := fsys.WriteFileAtomic(dir+"/config.yaml", []byte("version: 1"), vfs.WriteOpts{}); err != nil {
				t.Fatal(err)
			}
			if info, err := fsys.Stat(dir); err != nil || !info.Dir || info.FilePath != dir {
				t.Fatalf("config directory stat: %+v %v", info, err)
			}
			if entries, err := fsys.ReadDir(dir); err != nil || len(entries) != 1 || entries[0].FileName != "config.yaml" || entries[0].Dir || entries[0].FileSize != 10 {
				t.Fatalf("config directory listing: %+v %v", entries, err)
			}
			var visited []string
			if err := vfs.WalkDir(fsys, dir, func(name string, info *vfs.FileInfo, err error) error {
				visited = append(visited, name)
				return err
			}); err != nil || len(visited) != 2 || visited[0] != dir || visited[1] != dir+"/config.yaml" {
				t.Fatalf("config walk: %v %v", visited, err)
			}
			if _, err := fsys.WriteFileAtomic(dir+"/secret", nil, vfs.WriteOpts{}); err == nil {
				t.Fatal("opened other metadata for writes")
			}
			if err := fsys.RemoveAll(dir, vfs.RemoveOpts{}); err == nil {
				t.Fatal("opened config directory for recursive deletion")
			}
			if err := fsys.Remove(dir + "/config.yaml"); err != nil {
				t.Fatal(err)
			}
			if _, err := fsys.Stat(dir); err == nil {
				t.Fatal("empty metadata directory remains visible")
			}
			if _, err := fsys.ReadDir(dir); err == nil {
				t.Fatal("empty metadata directory remains listable")
			}
		}
	})

	t.Run("corrupt-storage-is-not-empty-storage", func(t *testing.T) {
		prefix := analytics.NewID()
		fsys, _ := NewS3FS(client, bucket, prefix)
		_ = fsys.SetWriteable()
		hash, err := fsys.WriteFileAtomic("/file", []byte("good"), vfs.WriteOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(prefix + "/objects/" + hash), Body: strings.NewReader("evil")}); err != nil {
			t.Fatal(err)
		}
		if _, err := fsys.ReadFile("/file"); err == nil {
			t.Fatal("accepted equal-length corrupted bytes")
		}
		if _, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(prefix + "/manifest.json"), Body: strings.NewReader("{broken")}); err != nil {
			t.Fatal(err)
		}
		if _, err := fsys.WriteFileAtomic("/new", nil, vfs.WriteOpts{}); err == nil {
			t.Fatal("overwrote corrupt manifest as empty")
		}
		missing, _ := NewS3FS(client, "nonexistent-bucket", prefix)
		if _, err := missing.Stat("/"); err == nil {
			t.Fatal("missing bucket treated as empty namespace")
		}
	})

	t.Run("batch-partial-commit-divergence", func(t *testing.T) {
		for _, fault := range []string{"first-leaf-ack-lost", "rollback-unavailable"} {
			t.Run(fault, func(t *testing.T) {
				prefix := analytics.NewID()
				base, _ := NewS3FS(client, bucket, prefix)
				_ = base.SetWriteable()
				for _, name := range []string{"/a", "/b"} {
					if _, err := base.WriteFileAtomic(name, []byte("old"+name), vfs.WriteOpts{}); err != nil {
						t.Fatal(err)
					}
				}
				puts := 0
				injected := errors.New("lost response or rollback outage")
				faulty := observedS3Client{S3FileClient: client, put: func(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
					if strings.HasSuffix(aws.ToString(in.Key), "/manifest.json") {
						puts++
						if fault == "first-leaf-ack-lost" && puts == 1 {
							if _, err := client.PutObject(ctx, in, opts...); err != nil {
								return nil, err
							}
							return nil, injected
						}
						if fault == "rollback-unavailable" && puts >= 2 {
							return nil, injected
						}
					}
					return client.PutObject(ctx, in, opts...)
				}}
				fsys, _ := NewS3FS(faulty, bucket, prefix)
				_ = fsys.SetWriteable()
				file := filepath.Join(t.TempDir(), "commits.jsonl")
				log := newWriteLog(fsys, nil, logger, 1)
				defer log.Close(ctx)
				log.SetCommitJournal(fileCommitJournal(file), nil, false)
				_, err := log.Submit(ctx, Attribution{}, vfs.ChangeSet{Changes: []vfs.Change{
					{Target: "/a", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("new/a")}},
					{Target: "/b", Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: []byte("new/b")}},
				}})
				if !errors.Is(err, injected) {
					t.Fatalf("batch failure=%v", err)
				}
				var rollback *vfs.RollbackError
				if errors.As(err, &rollback) != (fault == "rollback-unavailable") {
					t.Fatalf("rollback classification=%v", err)
				}
				for name, want := range map[string]string{"/a": "new/a", "/b": "old/b"} {
					got, err := base.ReadFile(name)
					if err != nil || string(got) != want {
						t.Fatalf("%s=%q want=%q err=%v", name, got, want, err)
					}
				}
				cursor, err := OpenHistoryCursor(file, HistoryPosition{})
				if err != nil {
					t.Fatal(err)
				}
				defer cursor.(io.Closer).Close()
				if _, ok, err := cursor.Next(ctx); err != nil || ok {
					t.Fatalf("unexpected journal record: %v %v", ok, err)
				}
			})
		}
	})
}
