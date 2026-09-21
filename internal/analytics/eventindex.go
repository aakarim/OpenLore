package analytics

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// sqliteEventIndex is the durable, idempotent projection used by dashboard
// analytics. The append-only event log remains the source of truth.
type sqliteEventIndex struct{ db *sql.DB }

func (x *sqliteEventIndex) Consume(ctx context.Context, event Event) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	_, _ = x.db.ExecContext(ctx, `INSERT INTO analytics_events(id,time_ns,type,principal,value) VALUES(?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET time_ns=excluded.time_ns,type=excluded.type,principal=excluded.principal,value=excluded.value`,
		event.ID, event.Time.UTC().UnixNano(), event.Type, event.Principal, data)
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

var _ Consumer = (*sqliteEventIndex)(nil)
var _ EventSource = (*sqliteEventIndex)(nil)
