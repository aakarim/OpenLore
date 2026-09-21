package analytics

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// sqliteEventIndex is the durable, idempotent projection used by dashboard
// analytics. The append-only event log remains the source of truth.
type sqliteEventIndex struct {
	db         *sql.DB
	caughtUp   atomic.Bool
	done       chan struct{}
	mu         sync.Mutex
	cursor     logCursor
	log        EventLog
	checkpoint string
}

func (x *sqliteEventIndex) consume(ctx context.Context, event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = x.db.ExecContext(ctx, `INSERT INTO analytics_events(id,time_ns,type,principal,value) VALUES(?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET time_ns=excluded.time_ns,type=excluded.type,principal=excluded.principal,value=excluded.value`,
		event.ID, event.Time.UTC().UnixNano(), event.Type, event.Principal, data)
	return err
}

func (x *sqliteEventIndex) run(ctx context.Context, log EventLog, checkpoint string) {
	defer close(x.done)
	x.mu.Lock()
	x.cursor, x.log, x.checkpoint = loadLogCursor(checkpoint), log, checkpoint
	x.mu.Unlock()
	if err := x.catchUp(ctx); err != nil {
		x.caughtUp.Store(false)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := x.catchUp(ctx); err != nil {
				x.caughtUp.Store(false)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (x *sqliteEventIndex) catchUp(ctx context.Context) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.log == nil {
		return errors.New("event index is not started")
	}
	if incremental, ok := x.log.(*fileEventLog); ok {
		if err := incremental.scanIncremental(ctx, x.cursor, func(event Event) error { return x.consume(ctx, event) }); err != nil {
			return err
		}
	} else if err := x.log.Scan(ctx, EventFilter{}, func(event Event) error { return x.consume(ctx, event) }); err != nil {
		return err
	}
	if err := writeLogCursor(x.checkpoint, x.cursor); err != nil {
		return err
	}
	x.caughtUp.Store(true)
	return nil
}

func loadLogCursor(path string) logCursor {
	cursor := logCursor{}
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &cursor)
	}
	return cursor
}

func writeLogCursor(path string, cursor logCursor) error {
	data, err := json.Marshal(cursor)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (x *sqliteEventIndex) Scan(ctx context.Context, filter EventFilter, fn func(Event) error) error {
	from, to := int64(0), int64(^uint64(0)>>1)
	if !filter.From.IsZero() {
		from = filter.From.UTC().UnixNano()
	}
	if !filter.To.IsZero() {
		to = filter.To.UTC().UnixNano()
	}
	rows, err := x.db.QueryContext(ctx, `SELECT value FROM analytics_events WHERE time_ns>=? AND time_ns<=? ORDER BY time_ns,id`, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	types, principals := map[string]bool{}, map[string]bool{}
	for _, value := range filter.Types {
		types[value] = true
	}
	for _, value := range filter.Principals {
		principals[value] = true
	}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var data []byte
		var event Event
		if err := rows.Scan(&data); err != nil {
			return err
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		if len(types) > 0 && !types[event.Type] || len(principals) > 0 && !principals[event.Principal] {
			continue
		}
		if err := fn(event); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (x *sqliteEventIndex) latest(ctx context.Context) time.Time {
	var value sql.NullInt64
	if x.db.QueryRowContext(ctx, `SELECT MAX(time_ns) FROM analytics_events`).Scan(&value) != nil || !value.Valid {
		return time.Time{}
	}
	return time.Unix(0, value.Int64).UTC()
}

var _ EventSource = (*sqliteEventIndex)(nil)
