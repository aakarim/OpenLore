package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

func fieldString(e Event, key string) string {
	v := e.Fields[key]
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
func fieldFloat(e Event, key string) float64 {
	switch v := e.Fields[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		n, _ := v.Float64()
		return n
	default:
		n, _ := strconv.ParseFloat(fmt.Sprint(v), 64)
		return n
	}
}
func scalarEventKey(e Event) string {
	key := fieldString(e, "commit_id") + "\x00" + fieldString(e, "path")
	if key == "\x00" {
		return e.ID
	}
	return key
}
func limitRows(rows [][]any, p Params) [][]any {
	offset, _ := strconv.Atoi(p.Extra["_offset"])
	if offset < 0 {
		offset = 0
	}
	if offset >= len(rows) {
		return nil
	}
	rows = rows[offset:]
	if p.Limit > 0 && len(rows) > p.Limit {
		rows = rows[:p.Limit]
	}
	return rows
}

func BuiltinAggregations() []Aggregation {
	return []Aggregation{
		{Name: "top-commands", Title: "Top commands", Description: "Most frequently executed commands", Requires: []string{"command.exec"}, Compute: topCommands},
		{Name: "unknown-commands", Title: "Unknown commands", Description: "Commands and syntax OpenLore did not recognise", Requires: []string{"command.unknown"}, Compute: unknownCommands},
		{Name: "commands-by-principal", Title: "Commands by principal", Description: "Command usage by principal and transport", Requires: []string{"command.exec"}, Compute: commandsByPrincipal},
		{Name: "sessions-over-time", Title: "Sessions over time", Description: "Session and command activity", Requires: []string{"session.start", "command.exec"}, Compute: sessionsOverTime},
		{Name: "tree-size", Title: "Tree size", Description: "Current content size", Requires: []string{"facts"}, Params: []ParamSpec{{Name: "path", Default: "/"}, {Name: "depth", Default: "1"}}, Compute: treeSize},
		{Name: "largest-docs", Title: "Largest documents", Description: "Documents with the greatest context cost", Requires: []string{"facts"}, Params: []ParamSpec{{Name: "path", Default: "/"}}, Compute: largestDocs},
		{Name: "size-over-time", Title: "Size over time", Description: "Knowledge-base growth", Requires: []string{"doc.scalars"}, Compute: sizeOverTime},
		{Name: "write-ratio", Title: "Write ratio", Description: "Human and agent writes", Requires: []string{"doc.scalars"}, Compute: writeRatio},
		{Name: "top-search-queries", Title: "Top search queries", Description: "Most frequent search patterns", Requires: []string{"search.query"}, Compute: topSearchQueriesTable},
		{Name: "top-unfilled-queries", Title: "Top unfilled queries", Description: "Search patterns that returned no results", Requires: []string{"search.query"}, Compute: topUnfilledQueriesTable},
		{Name: "least-used-files", Title: "Least-used files", Description: "Files read least recently", Requires: []string{"facts", "doc.read", "doc.hit"}, Params: []ParamSpec{{Name: "path", Default: "/"}, {Name: "order", Default: "asc"}}, Compute: fileUsageTable("asc")},
		{Name: "most-used-files", Title: "Most-used files", Description: "Files read most recently", Requires: []string{"facts", "doc.read", "doc.hit"}, Params: []ParamSpec{{Name: "path", Default: "/"}, {Name: "order", Default: "desc"}}, Compute: fileUsageTable("desc")},
		{Name: "least-used-folders", Title: "Least-used folders", Description: "Folders whose documents were read least recently", Requires: []string{"facts", "doc.read", "doc.hit"}, Params: []ParamSpec{{Name: "path", Default: "/"}, {Name: "depth", Default: "1"}, {Name: "order", Default: "asc"}}, Compute: leastUsedFolders},
		{Name: "least-used-lines", Title: "Least-used lines", Description: "Line ranges read least recently at the current content hash", Requires: []string{"facts", "doc.read", "doc.hit"}, Params: []ParamSpec{{Name: "path", Required: true}}, Compute: leastUsedLines},
	}
}

type commandRollup struct {
	count, errors        int
	principals, sessions map[string]bool
	durations            []float64
}

func topCommands(ctx context.Context, src EventSource, _ ContentFacts, p Params) (Table, error) {
	roll := map[string]*commandRollup{}
	err := src.Scan(ctx, EventFilter{From: p.Since, To: p.Until, Types: []string{"command.exec"}}, func(e Event) error {
		if tr := p.Extra["transport"]; tr != "" && e.Transport != tr {
			return nil
		}
		name := fieldString(e, "command")
		r := roll[name]
		if r == nil {
			r = &commandRollup{principals: map[string]bool{}, sessions: map[string]bool{}}
			roll[name] = r
		}
		r.count++
		r.principals[e.Principal] = true
		r.sessions[e.SessionID] = true
		if fieldFloat(e, "exit_code") != 0 {
			r.errors++
		}
		r.durations = append(r.durations, fieldFloat(e, "duration_ms"))
		return nil
	})
	if err != nil {
		return Table{}, err
	}
	rows := make([][]any, 0, len(roll))
	for name, r := range roll {
		sort.Float64s(r.durations)
		var p50 float64
		if len(r.durations) > 0 {
			p50 = r.durations[(len(r.durations)-1)/2]
		}
		rows = append(rows, []any{name, r.count, len(r.principals), len(r.sessions), float64(r.errors) / float64(r.count), p50})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i][1].(int) > rows[j][1].(int) })
	return Table{Columns: []string{"command", "count", "principals", "sessions", "error_rate", "p50_ms"}, Rows: limitRows(rows, p), Total: len(rows)}, nil
}
func unknownCommands(ctx context.Context, src EventSource, _ ContentFacts, p Params) (Table, error) {
	type row struct {
		count      int
		principals map[string]bool
		last       time.Time
	}
	roll := map[string]*row{}
	err := src.Scan(ctx, EventFilter{From: p.Since, To: p.Until, Types: []string{"command.unknown", "syntax.unknown"}}, func(e Event) error {
		name := fieldString(e, "command")
		if name == "" {
			name = fieldString(e, "syntax")
		}
		r := roll[name]
		if r == nil {
			r = &row{principals: map[string]bool{}}
			roll[name] = r
		}
		r.count++
		r.principals[e.Principal] = true
		if e.Time.After(r.last) {
			r.last = e.Time
		}
		return nil
	})
	if err != nil {
		return Table{}, err
	}
	rows := make([][]any, 0, len(roll))
	for n, r := range roll {
		rows = append(rows, []any{n, r.count, len(r.principals), r.last})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i][1].(int) > rows[j][1].(int) })
	return Table{Columns: []string{"command", "count", "principals", "last_seen"}, Rows: limitRows(rows, p), Total: len(rows)}, nil
}
func commandsByPrincipal(ctx context.Context, src EventSource, _ ContentFacts, p Params) (Table, error) {
	rollups := map[string]*commandRollup{}
	err := src.Scan(ctx, EventFilter{From: p.Since, To: p.Until, Types: []string{"command.exec"}}, func(e Event) error {
		key := e.Principal + "\x00" + e.Transport + "\x00" + fieldString(e, "command")
		rollup := rollups[key]
		if rollup == nil {
			rollup = &commandRollup{}
			rollups[key] = rollup
		}
		rollup.count++
		if fieldFloat(e, "exit_code") != 0 {
			rollup.errors++
		}
		rollup.durations = append(rollup.durations, fieldFloat(e, "duration_ms"))
		return nil
	})
	if err != nil {
		return Table{}, err
	}
	var rows [][]any
	for key, rollup := range rollups {
		parts := strings.Split(key, "\x00")
		sort.Float64s(rollup.durations)
		p50 := float64(0)
		if len(rollup.durations) > 0 {
			p50 = rollup.durations[(len(rollup.durations)-1)/2]
		}
		rows = append(rows, []any{parts[0], parts[1], parts[2], rollup.count, p50, float64(rollup.errors) / float64(rollup.count)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i][3].(int) > rows[j][3].(int) })
	return Table{Columns: []string{"principal", "transport", "command", "count", "p50_ms", "error_rate"}, Rows: limitRows(rows, p), Total: len(rows)}, nil
}
func sessionsOverTime(ctx context.Context, src EventSource, _ ContentFacts, p Params) (Table, error) {
	type row struct {
		sessions, commands, unknown int
		principals                  map[string]bool
	}
	roll := map[string]*row{}
	err := src.Scan(ctx, EventFilter{From: p.Since, To: p.Until}, func(e Event) error {
		if e.Type != "session.start" && e.Type != "command.exec" && e.Type != "command.unknown" && e.Type != "syntax.unknown" {
			return nil
		}
		key := e.Time.UTC().Truncate(24 * time.Hour).Format(time.RFC3339)
		r := roll[key]
		if r == nil {
			r = &row{principals: map[string]bool{}}
			roll[key] = r
		}
		r.principals[e.Principal] = true
		switch e.Type {
		case "session.start":
			r.sessions++
		case "command.exec":
			r.commands++
		default:
			r.unknown++
		}
		return nil
	})
	if err != nil {
		return Table{}, err
	}
	var keys []string
	for k := range roll {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var rows [][]any
	for _, k := range keys {
		r := roll[k]
		rows = append(rows, []any{k, r.sessions, len(r.principals), r.commands, r.unknown})
	}
	return Table{Columns: []string{"bucket", "sessions", "new_principals", "commands", "unknown_commands"}, Rows: limitRows(rows, p), Total: len(rows)}, nil
}
func treeSize(ctx context.Context, _ EventSource, facts ContentFacts, p Params) (Table, error) {
	root := p.Extra["path"]
	if root == "" {
		root = "/"
	}
	depth, _ := strconv.Atoi(p.Extra["depth"])
	var rows [][]any
	err := facts.Walk(ctx, root, WalkOptions{Depth: depth}, func(d DocScalars) error {
		rows = append(rows, []any{d.Path, 1, d.Scalars["bytes"], d.Scalars["lines"], d.Scalars["tokens"], d.Tokenizer})
		return nil
	})
	return Table{Columns: []string{"path", "files", "bytes", "lines", "tokens", "tokenizer"}, Rows: limitRows(rows, p), Total: len(rows)}, err
}
func largestDocs(ctx context.Context, src EventSource, facts ContentFacts, p Params) (Table, error) {
	all := p
	all.Limit = 0
	t, err := treeSize(ctx, src, facts, all)
	if err != nil {
		return t, err
	}
	sort.Slice(t.Rows, func(i, j int) bool { return t.Rows[i][4].(float64) > t.Rows[j][4].(float64) })
	rows := make([][]any, 0, len(t.Rows))
	for _, row := range t.Rows {
		rows = append(rows, []any{row[0], row[2], row[3], row[4]})
	}
	t.Columns = []string{"path", "bytes", "lines", "tokens"}
	t.Rows = limitRows(rows, p)
	return t, nil
}
func sizeOverTime(ctx context.Context, src EventSource, _ ContentFacts, p Params) (Table, error) {
	roll := map[string]map[string]float64{}
	seen := map[string]struct{}{}
	err := src.Scan(ctx, EventFilter{From: p.Since, To: p.Until, Types: []string{"doc.scalars"}}, func(e Event) error {
		id := scalarEventKey(e)
		if _, exists := seen[id]; exists {
			return nil
		}
		seen[id] = struct{}{}
		key := e.Time.UTC().Truncate(24*time.Hour).Format(time.RFC3339) + "\x00" + fieldString(e, "docset")
		r := roll[key]
		if r == nil {
			r = map[string]float64{}
			roll[key] = r
		}
		r["writes"]++
		if delta, ok := e.Fields["delta"].(map[string]any); ok {
			for _, k := range []string{"bytes", "lines", "tokens"} {
				if v, ok := delta[k].(float64); ok {
					r[k] += v
				}
			}
		}
		return nil
	})
	if err != nil {
		return Table{}, err
	}
	var keys []string
	for k := range roll {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var rows [][]any
	for _, k := range keys {
		parts := strings.Split(k, "\x00")
		r := roll[k]
		rows = append(rows, []any{parts[0], parts[1], r["writes"], r["bytes"], r["lines"], r["tokens"]})
	}
	return Table{Columns: []string{"bucket", "docset", "writes", "bytes_delta", "lines_delta", "tokens_delta"}, Rows: limitRows(rows, p), Total: len(rows)}, nil
}
func writeRatio(ctx context.Context, src EventSource, _ ContentFacts, p Params) (Table, error) {
	var human, agent int
	var hb, ab float64
	seen := map[string]struct{}{}
	err := src.Scan(ctx, EventFilter{From: p.Since, To: p.Until, Types: []string{"doc.scalars"}}, func(e Event) error {
		id := scalarEventKey(e)
		if _, exists := seen[id]; exists {
			return nil
		}
		seen[id] = struct{}{}
		bytes := float64(0)
		if d, ok := e.Fields["delta"].(map[string]any); ok {
			bytes, _ = d["bytes"].(float64)
		}
		if fieldString(e, "writer") == string(WriterHuman) {
			human++
			hb += bytes
		} else {
			agent++
			ab += bytes
		}
		return nil
	})
	total := human + agent
	ratio := float64(0)
	if total > 0 {
		ratio = float64(human) / float64(total)
	}
	return Table{Columns: []string{"path", "human_writes", "agent_writes", "human_ratio", "human_bytes_delta", "agent_bytes_delta"}, Rows: [][]any{{p.Extra["path"], human, agent, ratio, hb, ab}}, Total: 1}, err
}
