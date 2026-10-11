//go:build integration

package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/localstack"
	"hegel.dev/go/hegel"
)

// Fail rather than skip if Docker is unavailable: this suite is explicitly
// selected with -tags=integration and must actually exercise S3 in CI.
func TestS3Analytics(t *testing.T) {
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
	client := s3.NewFromConfig(aws.Config{
		Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	}, func(o *s3.Options) { o.BaseEndpoint = aws.String(endpoint); o.UsePathStyle = true })
	const bucket = "analytics-tests"
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}
	store := func(t *testing.T) *S3EventStore {
		t.Helper()
		s, err := NewS3EventStore(client, bucket, NewID())
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	t.Run("round-trip-rebuild", func(t *testing.T) {
		day := time.Now().UTC().Truncate(24 * time.Hour).Add(-3 * 24 * time.Hour)
		hegel.Test(t, func(ht *hegel.T) {
			remote := store(t)
			// Release each generated case's files and handles before the next
			// case (testing.T.Cleanup would retain them until all cases finish).
			dir, err := os.MkdirTemp("", "analytics-s3-case-")
			if err != nil {
				ht.Fatal(err)
			}
			defer os.RemoveAll(dir)
			log, err := OpenEventLog(filepath.Join(dir, "source"), LogOptions{
				Compress: hegel.Draw(ht, hegel.SampledFrom([]string{"none", "zstd"})),
				Rotate:   hegel.Draw(ht, hegel.SampledFrom([]time.Duration{time.Hour, 24 * time.Hour})),
			})
			if err != nil {
				ht.Fatal(err)
			}
			defer log.Close()
			var events summarySource
			for i := range hegel.Draw(ht, hegel.Integers(0, 30)) {
				at := day.Add(time.Duration(hegel.Draw(ht, hegel.Integers(0, 3*1440))) * time.Minute)
				e := Event{ID: fmt.Sprint(i), Time: at, Type: "command.exec", Principal: "person", SessionID: "session", Transport: "mcp", Fields: map[string]any{"command": hegel.Draw(ht, hegel.Text().MaxSize(128)), "duration_ms": hegel.Draw(ht, hegel.Integers(0, 100)), "exit_code": hegel.Draw(ht, hegel.Integers(0, 1))}}
				events = append(events, e)
			}
			// One logical write straddles midnight and disagrees on attribution.
			events = append(events,
				Event{ID: "scalar", Time: day.Add(24*time.Hour - time.Second), Type: "doc.scalars", Fields: map[string]any{"commit_id": "commit", "path": "/a.md", "writer": "agent"}},
				Event{ID: "write", Time: day.Add(24*time.Hour + time.Second), Type: "doc.write", Fields: map[string]any{"commit_id": "commit", "path": "/a.md", "writer": "human"}},
			)
			for _, event := range events {
				if err := log.Append(ctx, event); err != nil {
					ht.Fatal(err)
				}
			}
			if err := NewShipper(log, remote, 0).ShipNow(ctx); err != nil {
				ht.Fatal(err)
			}
			assertS3Events(ht, remote, events)
			service, err := New(config.AnalyticsConfig{Dir: filepath.Join(dir, "restored"), Log: config.AnalyticsLogConfig{Compress: "none"}}, Deps{Remote: remote})
			if err != nil {
				ht.Fatal(err)
			}
			defer service.Close(ctx)
			wantSummary, err := UsageSummary(ctx, events, Params{}, 4)
			if err != nil {
				ht.Fatal(err)
			}
			wantTable, err := builtin("top-commands")(ctx, events, nil, Params{})
			if err != nil {
				ht.Fatal(err)
			}
			for range 2 {
				if err := service.RebuildFromRemote(ctx); err != nil {
					ht.Fatal(err)
				}
				got, err := UsageSummary(ctx, service.EventSource(), Params{}, 4)
				if err != nil {
					ht.Fatal(err)
				}
				got.ComputedAt = wantSummary.ComputedAt
				if !reflect.DeepEqual(got, wantSummary) {
					ht.Fatalf("summary got %+v want %+v", got, wantSummary)
				}
				view, err := service.Registry().Run(ctx, "top-commands", Params{}, RunOptions{})
				if err != nil {
					ht.Fatal(err)
				}
				a, err := json.Marshal(view.Table)
				if err != nil {
					ht.Fatal(err)
				}
				b, err := json.Marshal(wantTable)
				if err != nil {
					ht.Fatal(err)
				}
				if !bytes.Equal(a, b) {
					ht.Fatalf("table got %s want %s", a, b)
				}
			}
		}, hegel.WithTestCases(1000))
	})

	t.Run("lost-ack-and-checkpoint", func(t *testing.T) {
		for _, crash := range []string{"lost-ack", "checkpoint-failure"} {
			t.Run(crash, func(t *testing.T) {
				remote := store(t)
				log := s3TestLog(t, LogOptions{Compress: "none"})
				e := Event{ID: "one", Time: time.Now().UTC(), Type: "doc.read"}
				appendEvents(t, log, e)
				checkpoint := filepath.Join(log.(*fileEventLog).dir, "shipper.checkpoint")
				fault := &s3PutFault{RemoteEventStore: remote, afterPut: func() error {
					if crash == "lost-ack" {
						return context.DeadlineExceeded
					}
					return os.Mkdir(checkpoint, 0o700) // PUT succeeds but checkpoint rename fails
				}}
				if err := NewShipper(log, fault, 0).ShipNow(ctx); err == nil {
					t.Fatal("expected interrupted shipping")
				}
				assertS3Events(t, remote, []Event{e})
				if crash == "checkpoint-failure" {
					if err := os.Remove(checkpoint); err != nil {
						t.Fatal(err)
					}
				} else if _, err := os.Stat(checkpoint); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("checkpoint advanced: %v", err)
				}
				before, err := remote.List(ctx, "events/")
				if err != nil {
					t.Fatal(err)
				}
				// A new shipper loads only durable state, not in-memory offsets.
				if err := NewShipper(log, remote, 0).ShipNow(ctx); err != nil {
					t.Fatal(err)
				}
				after, err := remote.List(ctx, "events/")
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("retry changed object keys: %v -> %v", before, after)
				}
				assertS3Events(t, remote, []Event{e})
			})
		}
	})

	t.Run("equal-size-segments-and-writers", func(t *testing.T) {
		for _, compression := range []string{"none", "zstd"} {
			t.Run(compression, func(t *testing.T) {
				remote := store(t)
				day := time.Now().UTC().Truncate(24 * time.Hour).Add(-48 * time.Hour)
				var want []Event
				for writer := range 2 {
					log := s3TestLog(t, LogOptions{Rotate: time.Hour, Compress: compression})
					for hour := range 2 {
						e := Event{ID: fmt.Sprintf("w%d-h%d", writer, hour), Time: day.Add(time.Duration(hour) * time.Hour), Type: "doc.read"}
						appendEvents(t, log, e)
						want = append(want, e)
					}
					if err := NewShipper(log, remote, 0).ShipNow(ctx); err != nil {
						t.Fatal(err)
					}
				}
				assertS3Events(t, remote, want)
			})
		}
	})

	t.Run("partial-seal-and-late-event", func(t *testing.T) {
		remote := store(t)
		day := time.Now().UTC().Truncate(24 * time.Hour).Add(-48 * time.Hour)
		log := s3TestLog(t, LogOptions{Rotate: time.Hour, Compress: "zstd"})
		original := Event{ID: "original", Time: day, Type: "doc.read"}
		late := Event{ID: "late", Time: day.Add(time.Minute), Type: "doc.hit"}
		other := Event{ID: "other", Time: day.Add(time.Hour), Type: "command.exec"}
		appendEvents(t, log, original)
		if err := NewShipper(log, remote, 0).ShipNow(ctx); err != nil {
			t.Fatal(err)
		}
		appendEvents(t, log, late, other)
		// Raw uploads coexist with the original sealed segment. This is also
		// the publication state seen when only part of a day has been sealed.
		for _, e := range []Event{original, late, other} {
			b, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			b = append(b, '\n')
			if err := remote.Put(ctx, "events/"+day.Format("2006-01-02")+"/"+e.ID+".jsonl", bytes.NewReader(b), int64(len(b))); err != nil {
				t.Fatal(err)
			}
		}
		want := []Event{original, late, other}
		assertS3Events(t, remote, want)
		if err := NewShipper(log, remote, 0).ShipNow(ctx); err != nil {
			t.Fatal(err)
		}
		assertS3Events(t, remote, want)
	})

	t.Run("outage-beyond-retention", func(t *testing.T) {
		for _, compression := range []string{"none", "zstd"} {
			t.Run(compression, func(t *testing.T) {
				remote := store(t)
				log := s3TestLog(t, LogOptions{Compress: compression, Retention: 24 * time.Hour})
				e := Event{ID: "unshipped", Time: time.Now().UTC().Truncate(24 * time.Hour).Add(-72 * time.Hour), Type: "doc.read"}
				appendEvents(t, log, e)
				fault := &s3PutFault{RemoteEventStore: remote, beforePut: func() error { return errors.New("S3 unavailable") }}
				if err := NewShipper(log, fault, 0).ShipNow(ctx); err == nil {
					t.Error("outage should be reported")
				}
				var retained []Event
				if err := log.Scan(ctx, EventFilter{}, func(e Event) error { retained = append(retained, e); return nil }); err != nil {
					t.Fatal(err)
				}
				if len(retained) != 1 || retained[0].ID != e.ID {
					t.Fatalf("unshipped event lost to retention: %v", retained)
				}
				shipper := NewShipper(log, remote, 0)
				if err := shipper.ShipNow(ctx); err != nil {
					t.Fatal(err)
				}
				assertS3Events(t, remote, []Event{e})
				if err := shipper.ShipNow(ctx); err != nil {
					t.Fatal(err)
				}
				if len(log.Segments()) != 0 {
					t.Fatal("acknowledged expired segments were not pruned")
				}
				// A late event must not inherit the deleted segment's checkpoint,
				// even when the replacement has exactly the same byte length.
				late := e
				late.ID = "late-next"
				beforeBytes, _ := json.Marshal(e)
				lateBytes, _ := json.Marshal(late)
				if len(beforeBytes) != len(lateBytes) {
					t.Fatal("fixture must reuse the byte length")
				}
				appendEvents(t, log, late)
				if err := shipper.ShipNow(ctx); err != nil {
					t.Fatal(err)
				}
				assertS3Events(t, remote, []Event{e, late})
			})
		}
	})
}

type s3PutFault struct {
	RemoteEventStore
	beforePut func() error
	afterPut  func() error
}

func (s *s3PutFault) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	if s.beforePut != nil {
		if err := s.beforePut(); err != nil {
			return err
		}
	}
	if err := s.RemoteEventStore.Put(ctx, key, body, size); err != nil {
		return err
	}
	if s.afterPut != nil {
		return s.afterPut()
	}
	return nil
}

func s3TestLog(t *testing.T, opts LogOptions) EventLog {
	t.Helper()
	log, err := OpenEventLog(t.TempDir(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(); err != nil {
			t.Error(err)
		}
	})
	return log
}

func assertS3Events(t interface {
	Helper()
	Fatal(...any)
	Fatalf(string, ...any)
}, store RemoteEventStore, want []Event) {
	t.Helper()
	var got []string
	if err := RemoteSource(store).Scan(context.Background(), EventFilter{}, func(e Event) error {
		b, err := json.Marshal(e)
		got = append(got, string(b))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var expected []string
	for _, e := range want {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		expected = append(expected, string(b))
	}
	slices.Sort(got)
	slices.Sort(expected)
	if !slices.Equal(got, expected) {
		t.Fatalf("remote events:\n got %s\nwant %s", strings.Join(got, "\n"), strings.Join(expected, "\n"))
	}
}
