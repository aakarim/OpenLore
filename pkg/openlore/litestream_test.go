//go:build integration

package openlore

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/pkg/vfs"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/localstack"
	"hegel.dev/go/hegel"
)

// This schema is a test fixture for the proposed transaction boundary, NOT a
// production filesystem backend or migration. Analytics uses its real store in
// a different file. No ATTACH, shared transaction, or shared backup prefix.
type sqliteFilesystemFixture struct {
	db     *sql.DB
	client S3FileClient
	bucket string
	prefix string
}

type sqliteFixtureChange struct {
	ID     string
	Path   string
	Body   []byte
	Delete bool
}

func openSQLiteFilesystemFixture(name string, client S3FileClient, bucket, prefix string) (*sqliteFilesystemFixture, error) {
	db, err := sql.Open("sqlite", name+"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=synchronous(FULL)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	_, err = db.Exec(`PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS fixture_files (path TEXT PRIMARY KEY, hash TEXT NOT NULL, size INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS fixture_commits (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, request_hash TEXT NOT NULL, record BLOB NOT NULL);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &sqliteFilesystemFixture{db: db, client: client, bucket: bucket, prefix: prefix}, nil
}

var errSQLiteFixtureFault = errors.New("injected before SQL commit")

func (f *sqliteFilesystemFixture) commit(ctx context.Context, change sqliteFixtureChange, rollback bool) error {
	request, err := json.Marshal(change)
	if err != nil {
		return err
	}
	// Content publication precedes metadata. An uncertain upload may leave an
	// orphan, but must never leave a visible reference or a commit event.
	if !change.Delete {
		_, err := f.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(f.bucket), Key: aws.String(f.prefix + "/objects/" + hashContent(change.Body)), Body: bytes.NewReader(change.Body)})
		if err != nil {
			return err
		}
	}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT request_hash FROM fixture_commits WHERE id=?`, change.ID).Scan(&previous)
	if err == nil {
		if previous != hashContent(request) {
			return errors.New("operation ID reused with different request")
		}
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	leaf := LeafRecord{Target: change.Path, Action: vfs.ChangeActionWrite}
	err = tx.QueryRowContext(ctx, `SELECT hash,size FROM fixture_files WHERE path=?`, change.Path).Scan(&leaf.BeforeHash, &leaf.BeforeSize)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	leaf.BeforeExists = err == nil
	record := CommitRecord{ID: change.ID, Time: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Attribution: Attribution{Principal: "test-person"}, ChangeSet: vfs.ChangeSet{Target: change.Path, Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{}}}
	if change.Delete {
		leaf.Action = vfs.ChangeActionRemove
		record.ChangeSet.Action, record.ChangeSet.Write = vfs.ChangeActionRemove, nil
		_, err = tx.ExecContext(ctx, `DELETE FROM fixture_files WHERE path=?`, change.Path)
	} else {
		leaf.AfterHash, leaf.AfterSize = hashContent(change.Body), int64(len(change.Body))
		record.Hash = leaf.AfterHash
		_, err = tx.ExecContext(ctx, `INSERT INTO fixture_files VALUES(?,?,?) ON CONFLICT(path) DO UPDATE SET hash=excluded.hash,size=excluded.size`, change.Path, leaf.AfterHash, leaf.AfterSize)
	}
	if err != nil {
		return err
	}
	record.Leaves = []LeafRecord{leaf}
	encoded, err := json.Marshal(record) // file contents stay in S3, not the DB
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO fixture_commits(id,request_hash,record) VALUES(?,?,?)`, change.ID, hashContent(request), encoded); err != nil {
		return err
	}
	if rollback {
		return errSQLiteFixtureFault
	}
	return tx.Commit()
}

func (f *sqliteFilesystemFixture) blob(ctx context.Context, hash string) ([]byte, error) {
	object, err := f.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(f.bucket), Key: aws.String(f.prefix + "/objects/" + hash)})
	if err != nil {
		return nil, err
	}
	defer object.Body.Close()
	body, err := io.ReadAll(object.Body)
	if err == nil && hashContent(body) != hash {
		err = errors.New("blob hash mismatch")
	}
	return body, err
}

// The cursor adapts the fixture to the real ScalarProcessor. Its sequence is a
// fixture checkpoint; it does not implement production recovery from DB rewind.
type sqliteFixtureCursor struct {
	f        *sqliteFilesystemFixture
	position HistoryPosition
	limit    int
}

func (c *sqliteFixtureCursor) Next(ctx context.Context) (CommitRecord, bool, error) {
	if c.limit > 0 && c.position.Offset >= int64(c.limit) {
		return CommitRecord{}, false, nil
	}
	var seq int64
	var body []byte
	err := c.f.db.QueryRowContext(ctx, `SELECT seq,record FROM fixture_commits WHERE seq>? ORDER BY seq LIMIT 1`, c.position.Offset).Scan(&seq, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return CommitRecord{}, false, nil
	}
	if err != nil {
		return CommitRecord{}, false, err
	}
	var record CommitRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return CommitRecord{}, false, err
	}
	if record.ChangeSet.Write != nil {
		record.ChangeSet.Write.Bytes, err = c.f.blob(ctx, record.Hash)
		if err != nil {
			return CommitRecord{}, false, err
		}
	}
	c.position = HistoryPosition{Offset: seq, LastID: record.ID}
	return record, true, nil
}
func (c *sqliteFixtureCursor) Position() HistoryPosition { return c.position }
func (c *sqliteFixtureCursor) Seek(ctx context.Context, id string) error {
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

func fixtureAnalytics(f *sqliteFilesystemFixture, count int) (analytics.Materialized, error) {
	cursor := &sqliteFixtureCursor{f: f, limit: count}
	processor := NewScalarProcessor(cursor, nil, IdentityStoreClassifier(nil)).(*ScalarProcessor)
	var total float64
	for _, event := range processor.Drain(context.Background()) {
		if delta, ok := event.Fields["delta"].(map[string]any)["bytes"].(float64); ok {
			total += delta
		}
	}
	if cursor.position.Offset != int64(count) {
		return analytics.Materialized{}, fmt.Errorf("processor stopped at %d, want %d", cursor.position.Offset, count)
	}
	// Store the derived value and its checkpoint together in the ANALYTICS DB.
	// This is a fixture consumer, not a change to the production pipeline schema.
	return analytics.Materialized{Status: analytics.StatusOK, Table: analytics.Table{Columns: []string{"bytes", "commit_id"}, Rows: [][]any{{total, cursor.position.LastID}}}}, nil
}

func fixtureModel(changes []sqliteFixtureChange) map[string][]byte {
	files := map[string][]byte{}
	for _, change := range changes {
		if change.Delete {
			delete(files, change.Path)
		} else {
			files[change.Path] = change.Body
		}
	}
	return files
}

func checkSQLiteFixture(ctx context.Context, f *sqliteFilesystemFixture, changes []sqliteFixtureChange) error {
	var integrity string
	if err := f.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		return fmt.Errorf("integrity=%s err=%v", integrity, err)
	}
	rows, err := f.db.QueryContext(ctx, `SELECT path,hash,size FROM fixture_files ORDER BY path`)
	if err != nil {
		return err
	}
	defer rows.Close()
	want := fixtureModel(changes)
	seen := 0
	for rows.Next() {
		var name, hash string
		var size int64
		if err := rows.Scan(&name, &hash, &size); err != nil {
			return err
		}
		body, err := f.blob(ctx, hash)
		expected, exists := want[name]
		if err != nil || !exists || size != int64(len(expected)) || !bytes.Equal(body, expected) {
			return fmt.Errorf("file %s: size=%d expected=%d exists=%v err=%v", name, size, len(expected), exists, err)
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if seen != len(want) {
		return fmt.Errorf("got %d files, want %d", seen, len(want))
	}
	rows.Close()
	cursor := &sqliteFixtureCursor{f: f}
	for _, change := range changes {
		record, ok, err := cursor.Next(ctx)
		if err != nil || !ok || record.ID != change.ID || record.ChangeSet.Target != change.Path {
			return fmt.Errorf("commit %s: got %s, ok=%v err=%v", change.ID, record.ID, ok, err)
		}
		if change.Delete {
			if record.ChangeSet.Action != vfs.ChangeActionRemove {
				return fmt.Errorf("commit %s is not a deletion", change.ID)
			}
		} else if record.ChangeSet.Write == nil || !bytes.Equal(record.ChangeSet.Write.Bytes, change.Body) {
			return fmt.Errorf("commit %s content differs", change.ID)
		}
	}
	if _, ok, err := cursor.Next(ctx); ok || err != nil {
		return fmt.Errorf("unexpected trailing commit: ok=%v err=%v", ok, err)
	}
	return nil
}

func fixtureExpectedAnalytics(changes []sqliteFixtureChange) analytics.Materialized {
	var total int
	for _, body := range fixtureModel(changes) {
		total += len(body)
	}
	return analytics.Materialized{Status: analytics.StatusOK, Table: analytics.Table{Columns: []string{"bytes", "commit_id"}, Rows: [][]any{{float64(total), changes[len(changes)-1].ID}}}}
}

func litestreamClient(endpoint string) *s3.Client {
	return s3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")}, func(o *s3.Options) {
		o.BaseEndpoint, o.UsePathStyle, o.RetryMaxAttempts = aws.String(endpoint), true, 1
	})
}

func litestreamConfig(dir, endpoint, bucket, prefix string, databases ...string) (string, error) {
	var config strings.Builder
	config.WriteString("dbs:\n")
	for _, name := range databases {
		fmt.Fprintf(&config, "  - path: %q\n    monitor-interval: 20ms\n    checkpoint-interval: 1s\n    busy-timeout: 10s\n    replica:\n      type: s3\n      bucket: %q\n      path: %q\n      endpoint: %q\n      region: us-east-1\n      access-key-id: test\n      secret-access-key: test\n      force-path-style: true\n      sync-interval: 20ms\n", filepath.Join(dir, name+".db"), bucket, prefix+"/"+name, endpoint)
	}
	name := filepath.Join(dir, strings.Join(databases, "-")+"-litestream.yml")
	return name, os.WriteFile(name, []byte(config.String()), 0o600)
}

func runLitestream(ctx context.Context, args ...string) error {
	output, err := exec.CommandContext(ctx, os.Getenv("LITESTREAM_BIN"), args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("litestream %v: %w\n%s", args, err, output)
	}
	return nil
}

func restoreFixture(ctx context.Context, config, sourceDir, destination string) error {
	for _, name := range []string{"events-filesystem", "analytics"} {
		if err := runLitestream(ctx, "restore", "-config", config, "-integrity-check", "full", "-o", filepath.Join(destination, name+".db"), filepath.Join(sourceDir, name+".db")); err != nil {
			return err
		}
	}
	return nil
}

func checkRestoredFixture(ctx context.Context, dir string, client S3FileClient, bucket, prefix string, changes []sqliteFixtureChange, consumed int) error {
	f, err := openSQLiteFilesystemFixture(filepath.Join(dir, "events-filesystem.db"), client, bucket, prefix)
	if err != nil {
		return err
	}
	defer f.db.Close()
	if err := checkSQLiteFixture(ctx, f, changes); err != nil {
		return err
	}
	store, err := analytics.OpenSQLiteAggregationStore(filepath.Join(dir, "analytics.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	got, ok, err := store.Get(ctx, "fixture-bytes", analytics.Params{})
	if err != nil || !ok || !reflect.DeepEqual(got, fixtureExpectedAnalytics(changes[:consumed])) {
		return fmt.Errorf("analytics differs: got=%+v ok=%v err=%v", got, ok, err)
	}
	// A fresh replay from the authoritative DB must reconstruct the full value,
	// regardless of how far behind the independently backed-up analytics is.
	replayed, err := fixtureAnalytics(f, len(changes))
	if err != nil || !reflect.DeepEqual(replayed, fixtureExpectedAnalytics(changes)) {
		return fmt.Errorf("replayed analytics differs: got=%+v err=%v", replayed, err)
	}
	return nil
}

func drawSQLiteFixtureChanges(t *hegel.T, min, max int) []sqliteFixtureChange {
	n := hegel.Draw(t, hegel.Integers(min, max))
	changes := make([]sqliteFixtureChange, n)
	for i := range changes {
		changes[i] = sqliteFixtureChange{ID: fmt.Sprintf("commit-%d", i), Path: hegel.Draw(t, hegel.SampledFrom([]string{"/a.md", "/nested/é %25.md", "/empty"})), Body: hegel.Draw(t, hegel.Binary(0, 128)), Delete: i > 0 && hegel.Draw(t, hegel.Booleans())}
	}
	return changes
}

func TestLitestreamSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("Litestream integration tests are excluded in short mode")
	}
	if os.Getenv("LITESTREAM_BIN") == "" {
		t.Fatal("run make test-litestream (requires Docker and downloads pinned Litestream)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()
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
	client := litestreamClient(endpoint)
	const bucket = "litestream-sqlite-tests"
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}

	t.Run("blob-and-transaction-faults", func(t *testing.T) {
		seen := map[string]bool{}
		hegel.Test(t, func(t *hegel.T) {
			change := drawSQLiteFixtureChanges(t, 1, 1)[0]
			op := hegel.Draw(t, hegel.SampledFrom([]string{"create", "update", "delete"}))
			fault := hegel.Draw(t, hegel.SampledFrom([]string{"none", "blob-before", "blob-after", "sql-rollback"}))
			dir, err := os.MkdirTemp("", "sqlite-transaction-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			prefix := analytics.NewID()
			f, err := openSQLiteFilesystemFixture(filepath.Join(dir, "events-filesystem.db"), client, bucket, prefix)
			if err != nil {
				t.Fatal(err)
			}
			defer f.db.Close()
			var initial []sqliteFixtureChange
			if op != "create" {
				before := sqliteFixtureChange{ID: "before", Path: change.Path, Body: append(bytes.Clone(change.Body), 0, 1, 2)}
				if err := f.commit(ctx, before, false); err != nil {
					t.Fatal(err)
				}
				initial = append(initial, before)
			}
			change.Delete = op == "delete"
			injected := errors.New("injected S3 acknowledgment failure")
			f.client = observedS3Client{S3FileClient: client, put: func(ctx context.Context, input *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
				if fault == "blob-before" {
					return nil, injected
				}
				out, err := client.PutObject(ctx, input, opts...)
				if err == nil && fault == "blob-after" {
					return nil, injected
				}
				return out, err
			}}
			err = f.commit(ctx, change, fault == "sql-rollback")
			want := append([]sqliteFixtureChange(nil), initial...)
			failed := fault == "sql-rollback" || !change.Delete && strings.HasPrefix(fault, "blob-")
			if failed {
				if !errors.Is(err, injected) && !errors.Is(err, errSQLiteFixtureFault) {
					t.Fatalf("expected injected error, got %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				want = append(want, change)
			}
			if err := checkSQLiteFixture(ctx, f, want); err != nil {
				t.Fatal(err)
			}
			f.client = client
			// Retry both known failures and a lost successful response. Exactly one
			// operation identity must survive; a different payload must be rejected.
			if err := f.commit(ctx, change, false); err != nil {
				t.Fatal(err)
			}
			want = append(initial, change)
			if err := checkSQLiteFixture(ctx, f, want); err != nil {
				t.Fatal(err)
			}
			conflict := change
			conflict.Body = append(bytes.Clone(change.Body), 7)
			if err := f.commit(ctx, conflict, false); err == nil {
				t.Fatal("accepted changed request with same operation ID")
			}
			if err := checkSQLiteFixture(ctx, f, want); err != nil {
				t.Fatal(err)
			}
			seen[fault+"/"+op] = true
		}, hegel.WithTestCases(1000))
		if !t.Failed() && len(seen) != 12 {
			t.Fatalf("fault coverage: got %v, want all 12 cells", seen)
		}
	})

	t.Run("replicate-crash-restore-separate-databases", func(t *testing.T) {
		outer := t
		hegel.Test(t, func(t *hegel.T) {
			changes := drawSQLiteFixtureChanges(t, 2, 16)
			consumed := hegel.Draw(t, hegel.Integers(1, len(changes)-1))
			if err := exerciseLitestreamCrash(ctx, endpoint, bucket, changes, consumed); err != nil {
				// Keep diagnostics even if an external-process failure does not
				// recur during Hegel's confirmation/shrinking executions.
				outer.Logf("crash scenario failed: %v", err)
				t.Fatal(err)
			}
		}, hegel.WithTestCases(24)) // Each case starts and SIGKILLs two real processes.
	})

	t.Run("independent-backup-skew", func(t *testing.T) {
		for _, ahead := range []bool{false, true} {
			t.Run(fmt.Sprintf("analytics-ahead=%v", ahead), func(t *testing.T) {
				hegel.Test(t, func(t *hegel.T) {
					body := hegel.Draw(t, hegel.Binary(0, 256))
					changes := []sqliteFixtureChange{
						{ID: "first", Path: "/doc.md", Body: body},
						{ID: "abandoned", Path: "/doc.md", Body: append(bytes.Clone(body), 1, 2, 3)},
					}
					if err := exerciseLitestreamSkew(ctx, endpoint, bucket, changes, ahead); err != nil {
						t.Fatal(err)
					}
				}, hegel.WithTestCases(16))
			})
		}
	})
}

type litestreamWriterScenario struct {
	Dir, Endpoint, Bucket, Prefix string
	Changes                       []sqliteFixtureChange
	Consumed                      int
}

// Re-exec the test binary so SIGKILL terminates the actual modernc writer,
// rather than merely closing its connection or killing a mock process.
func TestLitestreamWriterProcess(t *testing.T) {
	name := os.Getenv("OPENLORE_LITESTREAM_SCENARIO")
	if name == "" {
		return
	}
	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var scenario litestreamWriterScenario
	if err := json.Unmarshal(body, &scenario); err != nil {
		t.Fatal(err)
	}
	f, err := openSQLiteFilesystemFixture(filepath.Join(scenario.Dir, "events-filesystem.db"), litestreamClient(scenario.Endpoint), scenario.Bucket, scenario.Prefix)
	if err != nil {
		t.Fatal(err)
	}
	defer f.db.Close()
	store, err := analytics.OpenSQLiteAggregationStore(filepath.Join(scenario.Dir, "analytics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fmt.Println("initialized")
	input := bufio.NewReader(os.Stdin)
	for i, change := range scenario.Changes {
		if err := f.commit(context.Background(), change, false); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			initial, err := fixtureAnalytics(f, 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Put(context.Background(), "fixture-bytes", analytics.Params{}, initial); err != nil {
				t.Fatal(err)
			}
			fmt.Println("baseline")
			if _, err := input.ReadString('\n'); err != nil {
				t.Fatal(err)
			}
		}
	}
	materialized, err := fixtureAnalytics(f, scenario.Consumed)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), "fixture-bytes", analytics.Params{}, materialized); err != nil {
		t.Fatal(err)
	}
	fmt.Println("committed")
	if _, err := input.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR REPLACE INTO fixture_files VALUES('/uncommitted','missing-blob',999)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO fixture_commits(id,request_hash,record) VALUES('uncommitted','invalid','invalid-json')`); err != nil {
		t.Fatal(err)
	}
	// The analytics database has its OWN uncommitted transaction too.
	analyticsDB, err := sql.Open("sqlite", filepath.Join(scenario.Dir, "analytics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer analyticsDB.Close()
	analyticsTx, err := analyticsDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer analyticsTx.Rollback()
	if _, err := analyticsTx.Exec(`UPDATE materializations SET value='uncommitted'`); err != nil {
		t.Fatal(err)
	}
	fmt.Println("uncommitted")
	// The parent kills us here, without defer/Close/checkpoint/shutdown sync.
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func exerciseLitestreamCrash(ctx context.Context, endpoint, bucket string, changes []sqliteFixtureChange, consumed int) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	dir, err := os.MkdirTemp("", "litestream-crash-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	client, prefix := litestreamClient(endpoint), analytics.NewID()
	scenario := litestreamWriterScenario{Dir: dir, Endpoint: endpoint, Bucket: bucket, Prefix: prefix, Changes: changes, Consumed: consumed}
	body, err := json.Marshal(scenario)
	if err != nil {
		return err
	}
	input := filepath.Join(dir, "scenario.json")
	if err := os.WriteFile(input, body, 0o600); err != nil {
		return err
	}
	config, err := litestreamConfig(dir, endpoint, bucket, prefix, "events-filesystem", "analytics")
	if err != nil {
		return err
	}
	log, err := os.Create(filepath.Join(dir, "process.log"))
	if err != nil {
		return err
	}
	defer log.Close()
	replicator := exec.CommandContext(ctx, os.Getenv("LITESTREAM_BIN"), "replicate", "-config", config)
	replicator.Stdout, replicator.Stderr = log, log
	writer := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLitestreamWriterProcess$")
	writer.Env = append(os.Environ(), "OPENLORE_LITESTREAM_SCENARIO="+input)
	writer.Stderr = log
	out, err := writer.StdoutPipe()
	if err != nil {
		return err
	}
	in, err := writer.StdinPipe()
	if err != nil {
		return err
	}
	defer in.Close()
	if err := writer.Start(); err != nil {
		return err
	}
	defer func() { _ = writer.Process.Kill(); _ = writer.Wait() }()
	reader := bufio.NewReader(out)
	awaitWriter := func(message string) error {
		if line, err := reader.ReadString('\n'); err != nil || line != message {
			if !strings.HasPrefix(line, "--- FAIL:") {
				_ = writer.Process.Kill()
			}
			rest, _ := io.ReadAll(reader)
			logs, _ := os.ReadFile(log.Name())
			return fmt.Errorf("writer readiness: want %q got %q %v\n%s\n%s", message, line, err, rest, logs)
		}
		return nil
	}
	// Initial WAL/schema setup must finish before the sidecar opens the DBs.
	// Starting both against absent files reproduced SQLITE_BUSY in the real
	// analytics constructor. Do not hide it with retries or looser assertions.
	if err := awaitWriter("initialized\n"); err != nil {
		return err
	}
	if err := replicator.Start(); err != nil {
		return err
	}
	defer func() { _ = replicator.Process.Kill(); _ = replicator.Wait() }()
	// A successful SQL commit is NOT proof of remote durability. Wait until
	// both independently restored replicas contain exactly the acknowledged
	// prefix, then crash. A separate baseline round guarantees that later
	// writes happen AFTER the initial backup, not all inside one snapshot.
	for stage := 0; stage < 2; stage++ {
		message, expected, checkpoint := "committed\n", changes, consumed
		if stage == 0 {
			message, expected, checkpoint = "baseline\n", changes[:1], 1
		}
		if err := awaitWriter(message); err != nil {
			return err
		}
		deadline := time.Now().Add(30 * time.Second)
		for attempt := 0; ; attempt++ {
			probe := filepath.Join(dir, "probe-"+strconv.Itoa(attempt))
			if err := os.Mkdir(probe, 0o700); err != nil {
				return err
			}
			err = restoreFixture(ctx, config, dir, probe)
			if err == nil {
				err = checkRestoredFixture(ctx, probe, client, bucket, prefix, expected, checkpoint)
			}
			os.RemoveAll(probe)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				logs, _ := os.ReadFile(log.Name())
				return fmt.Errorf("replica failed to catch up: %w\n%s", err, logs)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
		if _, err := fmt.Fprintln(in, "continue"); err != nil {
			return err
		}
	}
	if err := awaitWriter("uncommitted\n"); err != nil {
		return err
	}
	if err := writer.Process.Kill(); err != nil {
		return err
	}
	_ = writer.Wait()
	if err := replicator.Process.Kill(); err != nil {
		return err
	}
	_ = replicator.Wait()
	// Validate actual local crash recovery as well as remote restore.
	if err := checkRestoredFixture(ctx, dir, client, bucket, prefix, changes, consumed); err != nil {
		return fmt.Errorf("local crash recovery: %w", err)
	}
	restored := filepath.Join(dir, "restored")
	if err := os.Mkdir(restored, 0o700); err != nil {
		return err
	}
	if err := restoreFixture(ctx, config, dir, restored); err != nil {
		return err
	}
	return checkRestoredFixture(ctx, restored, client, bucket, prefix, changes, consumed)
}

// Force unequal backup horizons without relying on a race against a sync
// timer. Both databases contain commit 2 locally, but only one backup does.
func exerciseLitestreamSkew(ctx context.Context, endpoint, bucket string, changes []sqliteFixtureChange, ahead bool) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	dir, err := os.MkdirTemp("", "litestream-skew-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	client, prefix := litestreamClient(endpoint), analytics.NewID()
	f, err := openSQLiteFilesystemFixture(filepath.Join(dir, "events-filesystem.db"), client, bucket, prefix)
	if err != nil {
		return err
	}
	defer f.db.Close()
	store, err := analytics.OpenSQLiteAggregationStore(filepath.Join(dir, "analytics.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	config, err := litestreamConfig(dir, endpoint, bucket, prefix, "events-filesystem", "analytics")
	if err != nil {
		return err
	}
	for i, change := range changes {
		if err := f.commit(ctx, change, false); err != nil {
			return err
		}
		materialized, err := fixtureAnalytics(f, i+1)
		if err != nil {
			return err
		}
		if err := store.Put(ctx, "fixture-bytes", analytics.Params{}, materialized); err != nil {
			return err
		}
		syncConfig := config
		if i == 1 {
			name := "events-filesystem"
			if ahead {
				name = "analytics"
			}
			syncConfig, err = litestreamConfig(dir, endpoint, bucket, prefix, name)
			if err != nil {
				return err
			}
		}
		if err := runLitestream(ctx, "replicate", "-once", "-config", syncConfig); err != nil {
			return err
		}
	}
	restored := filepath.Join(dir, "restored")
	if err := os.Mkdir(restored, 0o700); err != nil {
		return err
	}
	if err := restoreFixture(ctx, config, dir, restored); err != nil {
		return err
	}
	recovered, err := openSQLiteFilesystemFixture(filepath.Join(restored, "events-filesystem.db"), client, bucket, prefix)
	if err != nil {
		return err
	}
	defer recovered.db.Close()
	derived, err := analytics.OpenSQLiteAggregationStore(filepath.Join(restored, "analytics.db"))
	if err != nil {
		return err
	}
	defer derived.Close()
	sourceCount, analyticsCount := 2, 1
	if ahead {
		sourceCount, analyticsCount = 1, 2
	}
	if err := checkSQLiteFixture(ctx, recovered, changes[:sourceCount]); err != nil {
		return err
	}
	got, ok, err := derived.Get(ctx, "fixture-bytes", analytics.Params{})
	if err != nil || !ok || !reflect.DeepEqual(got, fixtureExpectedAnalytics(changes[:analyticsCount])) {
		return fmt.Errorf("skewed analytics differs: got=%+v ok=%v err=%v", got, ok, err)
	}
	checkpoint := got.Table.Rows[0][1].(string)
	var sequence int64
	err = recovered.db.QueryRowContext(ctx, `SELECT seq FROM fixture_commits WHERE id=?`, checkpoint).Scan(&sequence)
	if ahead {
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("expected missing analytics checkpoint %s after source rewind, got %v", checkpoint, err)
		}
		// The orphaned blob still exists, but does not make the lost event
		// authoritative. A new branch can reuse sequence 2, not its commit ID.
		if body, err := recovered.blob(ctx, hashContent(changes[1].Body)); err != nil || !bytes.Equal(body, changes[1].Body) {
			return fmt.Errorf("lost commit's orphaned blob: %v", err)
		}
		replacement := sqliteFixtureChange{ID: "replacement", Path: changes[0].Path, Body: append(bytes.Clone(changes[0].Body), 4)}
		if err := recovered.commit(ctx, replacement, false); err != nil {
			return err
		}
		if err := recovered.db.QueryRowContext(ctx, `SELECT seq FROM fixture_commits WHERE id='replacement'`).Scan(&sequence); err != nil || sequence != 2 {
			return fmt.Errorf("replacement sequence=%d err=%v", sequence, err)
		}
		changes = []sqliteFixtureChange{changes[0], replacement}
	} else if err != nil || sequence != 1 {
		return fmt.Errorf("behind checkpoint sequence=%d err=%v", sequence, err)
	}
	// Rebuild ONLY analytics from the restored authoritative history. Never
	// import the newer derived state back into the filesystem/events database.
	rebuilt, err := fixtureAnalytics(recovered, len(changes))
	if err != nil || !reflect.DeepEqual(rebuilt, fixtureExpectedAnalytics(changes)) {
		return fmt.Errorf("rebuild after skew: got=%+v err=%v", rebuilt, err)
	}
	if err := derived.Put(ctx, "fixture-bytes", analytics.Params{}, rebuilt); err != nil {
		return err
	}
	got, ok, err = derived.Get(ctx, "fixture-bytes", analytics.Params{})
	if err != nil || !ok || !reflect.DeepEqual(got, fixtureExpectedAnalytics(changes)) {
		return fmt.Errorf("persisted rebuild: got=%+v ok=%v err=%v", got, ok, err)
	}
	return checkSQLiteFixture(ctx, recovered, changes)
}
