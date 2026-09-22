package analytics

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aakarim/go-openlore/internal/config"
	"github.com/klauspost/compress/zstd"
)

func TestSQLiteLegacyOwnershipMigration(t *testing.T) {
	for _, scanState := range []string{"ready", "updating", "failed"} {
		t.Run(scanState, func(t *testing.T) {
			path := t.TempDir() + "/legacy.db"
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec(`CREATE TABLE facts_scan_state (
id INTEGER PRIMARY KEY, generation INTEGER NOT NULL, state TEXT NOT NULL,
started_at INTEGER NOT NULL, completed_at INTEGER NOT NULL DEFAULT 0,
error TEXT NOT NULL DEFAULT '', scope_hash TEXT NOT NULL);
INSERT INTO facts_scan_state VALUES(1,3,?,1,2,'','original')`, scanState)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			store, err := OpenSQLiteAggregationStore(path)
			if err != nil {
				t.Fatal(err)
			}
			state, err := newSQLiteFactsIndex(store).ScanState(context.Background())
			if err != nil || state.OwnershipCompatible != (scanState == "ready") {
				t.Fatalf("migrated %s: %+v err=%v", scanState, state, err)
			}
			if state.Processed != 0 || state.Skipped != 0 {
				t.Fatalf("legacy progress did not default to zero: %+v", state)
			}
			// A subsequent open must not backfill again and bless a new hash.
			if _, err := store.db.Exec(`UPDATE facts_scan_state SET scope_hash='changed',state='ready',processed=17,skipped=2`); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = OpenSQLiteAggregationStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			state, err = newSQLiteFactsIndex(store).ScanState(context.Background())
			if err != nil || state.OwnershipCompatible {
				t.Fatalf("reopen blessed incompatible ownership: %+v err=%v", state, err)
			}
			if state.Processed != 17 || state.Skipped != 2 {
				t.Fatalf("reopen lost durable progress: %+v", state)
			}
		})
	}
}

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

type trackingRemote struct {
	memoryRemote
	gets []string
}

func (r *trackingRemote) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	r.gets = append(r.gets, key)
	return r.memoryRemote.Get(ctx, key)
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
	remote := &trackingRemote{memoryRemote: memoryRemote{"events/2026-09-15/000-100.jsonl": line(one)}}
	var compressed bytes.Buffer
	zw, _ := zstd.NewWriter(&compressed)
	_, _ = zw.Write(line(one, two))
	_ = zw.Close()
	remote.memoryRemote["events/2026-09-15/events-2026-09-15.jsonl.zst"] = compressed.Bytes()
	remote.memoryRemote["events/2026-09-16/000-100.jsonl"] = line(one)
	var got []string
	err := NewRemoteSource(remote, "events/").Scan(context.Background(), EventFilter{Types: []string{"read"}}, func(e Event) error { got = append(got, e.ID); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "one,two,one" {
		t.Fatalf("events=%v, want deterministic deduplication scoped to each day", got)
	}
	if len(remote.gets) != 2 || !strings.HasSuffix(remote.gets[0], ".jsonl.zst") {
		t.Fatalf("remote reads = %v, want the sealed object instead of its day's deltas", remote.gets)
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
	params := Params{Since: event.Time.Add(-time.Hour), Until: event.Time.Add(time.Hour), Limit: 100}
	if err := service.store.Put(context.Background(), "top-commands", params, Materialized{Status: StatusOK, Table: Table{Rows: [][]any{{"stale"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := service.RebuildFromRemote(context.Background()); err != nil {
		t.Fatal(err)
	}
	materialized, err := service.Registry().Run(context.Background(), "top-commands", params, RunOptions{})
	if err != nil || materialized.Status != StatusOK || len(materialized.Table.Rows) != 1 || materialized.Table.Rows[0][0] != "stat" {
		t.Fatalf("rebuilt aggregation = %#v, err=%v", materialized, err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	service.Aggregator().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if strings.Contains(response.Body.String(), `openlore_commands_total{command="stat"`) {
		t.Fatalf("durable replay contaminated restart-reset live metrics:\n%s", response.Body.String())
	}
	var restored int
	if err := service.EventSource().Scan(context.Background(), EventFilter{Types: []string{"command.exec"}}, func(Event) error { restored++; return nil }); err != nil || restored != 1 {
		t.Fatalf("restored local events = %d, err=%v", restored, err)
	}
}
