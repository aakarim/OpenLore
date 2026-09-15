package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/klauspost/compress/zstd"
)

func TestSQLiteAggregationStoreConcurrentAndPersistent(t *testing.T) {
	path := t.TempDir() + "/materializations.db"
	store, err := OpenSQLiteAggregationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := Params{Limit: i}
			m := Materialized{Table: Table{Rows: [][]any{{strings.Repeat("x", 64*1024)}}}}
			if err := store.Put(ctx, "large", p, m); err != nil {
				t.Errorf("put %d: %v", i, err)
				return
			}
			if _, ok, err := store.Get(ctx, "large", p); err != nil || !ok {
				t.Errorf("get %d: ok=%v err=%v", i, ok, err)
			}
		}(i)
	}
	wg.Wait()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenSQLiteAggregationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got, ok, err := store.Get(ctx, "large", Params{Limit: 19}); err != nil || !ok || len(got.Table.Rows) != 1 {
		t.Fatalf("persistent get: ok=%v rows=%d err=%v", ok, len(got.Table.Rows), err)
	}
	if err := store.Invalidate(ctx, "large"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Get(ctx, "large", Params{Limit: 19}); err != nil || ok {
		t.Fatalf("invalidated value: ok=%v err=%v", ok, err)
	}
}

func TestS3EventStoreHTTPBoundary(t *testing.T) {
	objects := map[string][]byte{}
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/bucket/")
		if r.Method == http.MethodPut {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			objects[key] = body
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodGet && r.URL.Query().Has("list-type") {
			prefix := r.URL.Query().Get("prefix")
			mu.Lock()
			var keys []string
			for key := range objects {
				if strings.HasPrefix(key, prefix) {
					keys = append(keys, key)
				}
			}
			mu.Unlock()
			sort.Strings(keys)
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, `<?xml version="1.0"?><ListBucketResult><IsTruncated>false</IsTruncated>`)
			for _, key := range keys {
				fmt.Fprintf(w, "<Contents><Key>%s</Key><Size>%d</Size></Contents>", key, len(objects[key]))
			}
			fmt.Fprint(w, "</ListBucketResult>")
			return
		}
		if r.Method == http.MethodGet {
			mu.Lock()
			body, ok := objects[key]
			mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(body)
			return
		}
		http.Error(w, "unexpected request", http.StatusBadRequest)
	}))
	defer server.Close()
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	store, err := OpenS3EventStore(context.Background(), S3Config{Endpoint: server.URL, Region: "us-east-1", Bucket: "bucket", Prefix: "tenant", PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), "events/a.jsonl", strings.NewReader("payload"), 7); err != nil {
		t.Fatal(err)
	}
	keys, err := store.List(context.Background(), "events/")
	if err != nil || len(keys) != 1 || keys[0] != "events/a.jsonl" {
		t.Fatalf("list=%v err=%v", keys, err)
	}
	r, err := store.Get(context.Background(), keys[0])
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if body, _ := io.ReadAll(r); string(body) != "payload" {
		t.Fatalf("body=%q", body)
	}
}

type memoryRemote map[string][]byte

func (m memoryRemote) Put(_ context.Context, key string, r io.Reader, _ int64) error {
	b, err := io.ReadAll(r)
	m[key] = b
	return err
}
func (m memoryRemote) List(_ context.Context, prefix string) ([]string, error) {
	var out []string
	for key := range m {
		if strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
	}
	return out, nil
}
func (m memoryRemote) Get(_ context.Context, key string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(m[key])), nil
}

func TestRemoteSourceRangesSealedZstdDedupAndOrder(t *testing.T) {
	at := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	one := Event{ID: "one", Time: at.Add(time.Minute), Type: "read", Principal: "p"}
	two := Event{ID: "two", Time: at, Type: "read", Principal: "p"}
	line := func(events ...Event) []byte {
		var b bytes.Buffer
		for _, event := range events {
			json.NewEncoder(&b).Encode(event)
		}
		return b.Bytes()
	}
	remote := memoryRemote{"events/2026-09-15/000-100.jsonl": line(one)}
	var compressed bytes.Buffer
	zw, _ := zstd.NewWriter(&compressed)
	_, _ = zw.Write(line(one, two))
	_ = zw.Close()
	remote["events/2026-09-15/events-2026-09-15.jsonl.zst"] = compressed.Bytes()
	var got []string
	err := NewRemoteSource(remote, "events/").Scan(context.Background(), EventFilter{Types: []string{"read"}}, func(e Event) error { got = append(got, e.ID); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "one,two" {
		t.Fatalf("events=%v, want deterministic deduplicated order", got)
	}
}

func TestServiceRebuildsFromRemoteAfterLocalLoss(t *testing.T) {
	remote := memoryRemote{}
	event := Event{ID: "remote-command", Time: time.Now().UTC(), Type: "command.exec", Transport: "mcp", Fields: map[string]any{"command": "stat"}}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(event); err != nil {
		t.Fatal(err)
	}
	remote["events/2026-09-15/000-100.jsonl"] = body.Bytes()
	service, err := New(config.AnalyticsConfig{Dir: t.TempDir(), Log: config.AnalyticsLogConfig{Compress: "none"}}, Deps{Remote: remote})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	if err := service.RebuildFromRemote(context.Background()); err != nil {
		t.Fatal(err)
	}
	materialized, err := service.Registry().Run(context.Background(), "top-commands", Params{
		Since: time.Now().UTC().Add(-30 * 24 * time.Hour), Until: time.Now().UTC(), Limit: 100,
	}, RunOptions{})
	if err != nil || materialized.Status != StatusOK || len(materialized.Table.Rows) != 1 || materialized.Table.Rows[0][0] != "stat" {
		t.Fatalf("rebuilt aggregation = %#v, err=%v", materialized, err)
	}
	response := httptest.NewRecorder()
	service.Aggregator().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(response.Body.String(), `openlore_commands_total{command="stat",transport="mcp",exit_class="success"} 1`) {
		t.Fatalf("rebuilt metrics missing remote command:\n%s", response.Body.String())
	}
}
