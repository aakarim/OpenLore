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
	if p.Limit > 0 && len(rows) > p.Limit {
		rows = rows[:p.Limit]
	}
	return rows
}

func BuiltinAggregations() []Aggregation {
	return []Aggregation{
		incremental(Aggregation{Name: "top-commands", Title: "Top commands", Description: "Most frequently executed commands", Requires: []string{"command.exec"}}, topCommands),
		incremental(Aggregation{Name: "unknown-commands", Title: "Unknown commands", Description: "Commands and syntax OpenLore did not recognise", Requires: []string{"command.unknown"}}, unknownCommands),
		incremental(Aggregation{Name: "commands-by-principal", Title: "Commands by principal", Description: "Command usage by principal and transport", Requires: []string{"command.exec"}}, commandsByPrincipal),
		// Whether a session is visible depends on all of its activity in the
		// window, which a day's partial cannot know, so sessions are scanned
		// over the whole window instead of cached per day.
		singleScan(Aggregation{Name: "sessions-over-time", Title: "Sessions over time", Description: "Session and command activity", Requires: []string{"session.start", "command.exec"}}, sessionsOverTime),
		{Name: "tree-size", Title: "Tree size", Description: "Current content size", Requires: []string{"facts"}, Params: []ParamSpec{{Name: "path", Default: "/"}, {Name: "depth", Default: "1"}}, Compute: treeSize},
		{Name: "largest-docs", Title: "Largest documents", Description: "Documents with the greatest context cost", Requires: []string{"facts"}, Params: []ParamSpec{{Name: "path", Default: "/"}}, Compute: largestDocs},
		incremental(Aggregation{Name: "size-over-time", Title: "Size over time", Description: "Knowledge-repository growth", Requires: []string{"doc.scalars"}}, sizeOverTime),
		incremental(Aggregation{Name: "write-ratio", Title: "Write ratio", Description: "Human and agent writes", Requires: []string{"doc.scalars"}}, writeRatio),
		incremental(Aggregation{Name: "top-search-queries", Title: "Top search queries", Description: "Most frequent search patterns", Requires: []string{"search.query"}}, searchQueriesTable(nil)),
		incremental(Aggregation{Name: "top-unfilled-queries", Title: "Top unfilled queries", Description: "Search patterns that returned no results", Requires: []string{"search.query"}}, searchQueriesTable(new(false))),
		incremental(Aggregation{Name: "least-used-files", Title: "Least-used files", Description: "Files read least recently", Requires: []string{"facts", "doc.read", "doc.hit"}, Params: []ParamSpec{{Name: "path", Default: "/"}, {Name: "order", Default: "asc"}}}, fileUsageTable("asc")),
		incremental(Aggregation{Name: "most-used-files", Title: "Most-used files", Description: "Files read most recently", Requires: []string{"facts", "doc.read", "doc.hit"}, Params: []ParamSpec{{Name: "path", Default: "/"}, {Name: "order", Default: "desc"}}}, fileUsageTable("desc")),
		incremental(Aggregation{Name: "least-used-folders", Title: "Least-used folders", Description: "Folders whose documents were read least recently", Requires: []string{"facts", "doc.read", "doc.hit"}, Params: []ParamSpec{{Name: "path", Default: "/"}, {Name: "depth", Default: "1"}, {Name: "order", Default: "asc"}}}, leastUsedFolders),
		incremental(Aggregation{Name: "least-used-lines", Title: "Least-used lines", Description: "Line ranges read least recently at the current content hash", Requires: []string{"facts", "doc.read", "doc.hit"}, Params: []ParamSpec{{Name: "path", Required: true}}}, usedLines(false)),
		incremental(Aggregation{Name: "most-used-lines", Title: "Most-used lines", Description: "Line ranges read most often at the current content hash", Requires: []string{"facts", "doc.read", "doc.hit"}, Params: []ParamSpec{{Name: "path", Required: true}}}, usedLines(true)),
	}
}

// durationCounts is an exact, mergeable multiset of durations keyed by their
// shortest decimal form.
type durationCounts map[string]int

func (d durationCounts) add(value float64) { d[strconv.FormatFloat(value, 'g', -1, 64)]++ }

func (d durationCounts) merge(later durationCounts) {
	for value, count := range later {
		d[value] += count
	}
}

// median returns element (n-1)/2 of the sorted durations.
func (d durationCounts) median() float64 {
	values := make([]float64, 0, len(d))
	total := 0
	counts := make(map[float64]int, len(d))
	for key, count := range d {
		value, _ := strconv.ParseFloat(key, 64)
		if _, seen := counts[value]; !seen {
			values = append(values, value)
		}
		counts[value] += count
		total += count
	}
	if total == 0 {
		return 0
	}
	sort.Float64s(values)
	index := (total - 1) / 2
	for _, value := range values {
		if index < counts[value] {
			return value
		}
		index -= counts[value]
	}
	return 0
}

// sortByCount orders rows by the count column, most first, breaking ties by
// the given string columns so results do not depend on map iteration.
func sortByCount(rows [][]any, count int, keys ...int) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i][count].(int) != rows[j][count].(int) {
			return rows[i][count].(int) > rows[j][count].(int)
		}
		for _, key := range keys {
			if rows[i][key].(string) != rows[j][key].(string) {
				return rows[i][key].(string) < rows[j][key].(string)
			}
		}
		return false
	})
}

func mergeSet(into map[string]bool, later map[string]bool) map[string]bool {
	if into == nil && len(later) > 0 {
		into = map[string]bool{}
	}
	for value := range later {
		into[value] = true
	}
	return into
}

type commandStats struct {
	Count      int             `json:"count"`
	Errors     int             `json:"errors"`
	Principals map[string]bool `json:"principals,omitempty"`
	Sessions   map[string]bool `json:"sessions,omitempty"`
	Durations  durationCounts  `json:"durations"`
}

func (c *commandStats) add(e Event, people bool) {
	c.Count++
	if people {
		if c.Principals == nil {
			c.Principals, c.Sessions = map[string]bool{}, map[string]bool{}
		}
		c.Principals[e.Principal] = true
		c.Sessions[e.SessionID] = true
	}
	if fieldFloat(e, "exit_code") != 0 {
		c.Errors++
	}
	c.Durations.add(fieldFloat(e, "duration_ms"))
}

func (c *commandStats) merge(later *commandStats) {
	c.Count += later.Count
	c.Errors += later.Errors
	c.Principals = mergeSet(c.Principals, later.Principals)
	c.Sessions = mergeSet(c.Sessions, later.Sessions)
	c.Durations.merge(later.Durations)
}

type commandsPartial struct {
	transport string
	people    bool
	keyOf     func(Event) string
	Commands  map[string]*commandStats `json:"commands"`
}

func (c *commandsPartial) Add(e Event) {
	if c.transport != "" && e.Transport != c.transport {
		return
	}
	key := c.keyOf(e)
	stats := c.Commands[key]
	if stats == nil {
		stats = &commandStats{Durations: durationCounts{}}
		c.Commands[key] = stats
	}
	stats.add(e, c.people)
}

func (c *commandsPartial) Merge(later Partial) {
	for key, stats := range later.(*commandsPartial).Commands {
		if current := c.Commands[key]; current != nil {
			current.merge(stats)
		} else {
			c.Commands[key] = stats
		}
	}
}

var topCommands = Incremental{
	Types: []string{"command.exec"},
	New: func(p Params) Partial {
		return &commandsPartial{transport: p.Extra["transport"], people: true, keyOf: func(e Event) string { return fieldString(e, "command") }, Commands: map[string]*commandStats{}}
	},
	Table: func(_ context.Context, partial Partial, _ ContentFacts, p Params) (Table, error) {
		commands := partial.(*commandsPartial).Commands
		rows := make([][]any, 0, len(commands))
		for name, r := range commands {
			rows = append(rows, []any{name, r.Count, len(r.Principals), len(r.Sessions), float64(r.Errors) / float64(r.Count), r.Durations.median()})
		}
		sortByCount(rows, 1, 0)
		return Table{Columns: []string{"command", "count", "principals", "sessions", "error_rate", "p50_ms"}, Rows: limitRows(rows, p), Total: len(rows)}, nil
	},
}

var commandsByPrincipal = Incremental{
	Types: []string{"command.exec"},
	New: func(Params) Partial {
		return &commandsPartial{keyOf: func(e Event) string { return e.Principal + "\x00" + e.Transport + "\x00" + fieldString(e, "command") }, Commands: map[string]*commandStats{}}
	},
	Table: func(_ context.Context, partial Partial, _ ContentFacts, p Params) (Table, error) {
		var rows [][]any
		for key, rollup := range partial.(*commandsPartial).Commands {
			parts := strings.Split(key, "\x00")
			rows = append(rows, []any{parts[0], parts[1], parts[2], rollup.Count, rollup.Durations.median(), float64(rollup.Errors) / float64(rollup.Count)})
		}
		sortByCount(rows, 3, 0, 1, 2)
		return Table{Columns: []string{"principal", "transport", "command", "count", "p50_ms", "error_rate"}, Rows: limitRows(rows, p), Total: len(rows)}, nil
	},
}

type unknownCommand struct {
	Count      int             `json:"count"`
	Principals map[string]bool `json:"principals"`
	Last       time.Time       `json:"last"`
}

type unknownCommandsPartial struct {
	Commands map[string]*unknownCommand `json:"commands"`
}

func (u *unknownCommandsPartial) Add(e Event) {
	name := fieldString(e, "command")
	if name == "" {
		name = fieldString(e, "syntax")
	}
	u.merge(name, &unknownCommand{Count: 1, Principals: map[string]bool{e.Principal: true}, Last: e.Time})
}

func (u *unknownCommandsPartial) merge(name string, later *unknownCommand) {
	current := u.Commands[name]
	if current == nil {
		u.Commands[name] = later
		return
	}
	current.Count += later.Count
	current.Principals = mergeSet(current.Principals, later.Principals)
	if later.Last.After(current.Last) {
		current.Last = later.Last
	}
}

func (u *unknownCommandsPartial) Merge(later Partial) {
	for name, command := range later.(*unknownCommandsPartial).Commands {
		u.merge(name, command)
	}
}

var unknownCommands = Incremental{
	Types: []string{"command.unknown", "syntax.unknown"},
	New:   func(Params) Partial { return &unknownCommandsPartial{Commands: map[string]*unknownCommand{}} },
	Table: func(_ context.Context, partial Partial, _ ContentFacts, p Params) (Table, error) {
		commands := partial.(*unknownCommandsPartial).Commands
		rows := make([][]any, 0, len(commands))
		for n, r := range commands {
			rows = append(rows, []any{n, r.Count, len(r.Principals), r.Last})
		}
		sortByCount(rows, 1, 0)
		return Table{Columns: []string{"command", "count", "principals", "last_seen"}, Rows: limitRows(rows, p), Total: len(rows)}, nil
	},
}

type sessionBucket struct {
	Sessions   int             `json:"sessions"`
	Commands   int             `json:"commands"`
	Unknown    int             `json:"unknown"`
	Principals map[string]bool `json:"principals"`
}

type sessionsPartial struct {
	Buckets map[string]*sessionBucket `json:"buckets"`
}

func (s *sessionsPartial) bucket(key string) *sessionBucket {
	b := s.Buckets[key]
	if b == nil {
		b = &sessionBucket{Principals: map[string]bool{}}
		s.Buckets[key] = b
	}
	return b
}

func (s *sessionsPartial) Add(e Event) {
	b := s.bucket(e.Time.UTC().Truncate(24 * time.Hour).Format(time.RFC3339))
	b.Principals[e.Principal] = true
	switch e.Type {
	case "session.start":
		b.Sessions++
	case "command.exec":
		b.Commands++
	default:
		b.Unknown++
	}
}

func (s *sessionsPartial) Merge(later Partial) {
	for key, b := range later.(*sessionsPartial).Buckets {
		current := s.bucket(key)
		current.Sessions += b.Sessions
		current.Commands += b.Commands
		current.Unknown += b.Unknown
		current.Principals = mergeSet(current.Principals, b.Principals)
	}
}

var sessionsOverTime = Incremental{
	Types: []string{"session.start", "command.exec", "command.unknown", "syntax.unknown"},
	New:   func(Params) Partial { return &sessionsPartial{Buckets: map[string]*sessionBucket{}} },
	Table: func(_ context.Context, partial Partial, _ ContentFacts, p Params) (Table, error) {
		buckets := partial.(*sessionsPartial).Buckets
		keys := make([]string, 0, len(buckets))
		for k := range buckets {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var rows [][]any
		for _, k := range keys {
			r := buckets[k]
			rows = append(rows, []any{k, r.Sessions, len(r.Principals), r.Commands, r.Unknown})
		}
		return Table{Columns: []string{"bucket", "sessions", "new_principals", "commands", "unknown_commands"}, Rows: limitRows(rows, p), Total: len(rows)}, nil
	},
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

// scalarWrite is the first doc.scalars event seen for one commit and path.
type scalarWrite struct {
	Bucket string             `json:"bucket"`
	Docset string             `json:"docset"`
	Writer string             `json:"writer"`
	Delta  map[string]float64 `json:"delta,omitempty"`
}

// scalarWritesPartial keeps one write per commit/path, preferring the first.
type scalarWritesPartial struct {
	Writes map[string]scalarWrite `json:"writes"`
}

func (w *scalarWritesPartial) Add(e Event) {
	id := scalarEventKey(e)
	if _, exists := w.Writes[id]; exists {
		return
	}
	write := scalarWrite{Bucket: e.Time.UTC().Truncate(24 * time.Hour).Format(time.RFC3339), Docset: fieldString(e, "docset"), Writer: fieldString(e, "writer")}
	if delta, ok := e.Fields["delta"].(map[string]any); ok {
		for _, k := range []string{"bytes", "lines", "tokens"} {
			if v, ok := delta[k].(float64); ok {
				if write.Delta == nil {
					write.Delta = map[string]float64{}
				}
				write.Delta[k] = v
			}
		}
	}
	w.Writes[id] = write
}

func (w *scalarWritesPartial) Merge(later Partial) {
	for id, write := range later.(*scalarWritesPartial).Writes {
		if _, exists := w.Writes[id]; !exists {
			w.Writes[id] = write
		}
	}
}

func newScalarWrites(Params) Partial { return &scalarWritesPartial{Writes: map[string]scalarWrite{}} }

var sizeOverTime = Incremental{
	Types: []string{"doc.scalars"},
	New:   newScalarWrites,
	Table: func(_ context.Context, partial Partial, _ ContentFacts, p Params) (Table, error) {
		roll := map[string]map[string]float64{}
		for _, write := range partial.(*scalarWritesPartial).Writes {
			key := write.Bucket + "\x00" + write.Docset
			r := roll[key]
			if r == nil {
				r = map[string]float64{}
				roll[key] = r
			}
			r["writes"]++
			for k, v := range write.Delta {
				r[k] += v
			}
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
	},
}

var writeRatio = Incremental{
	Types: []string{"doc.scalars"},
	New:   newScalarWrites,
	Table: func(_ context.Context, partial Partial, _ ContentFacts, p Params) (Table, error) {
		var human, agent, unknown int
		var hb, ab, ub float64
		for _, write := range partial.(*scalarWritesPartial).Writes {
			bytes := write.Delta["bytes"]
			switch Writer(write.Writer) {
			case WriterHuman:
				human++
				hb += bytes
			case WriterAgent:
				agent++
				ab += bytes
			default:
				unknown++
				ub += bytes
			}
		}
		total := human + agent + unknown
		ratio := float64(0)
		if total > 0 {
			ratio = float64(human) / float64(total)
		}
		return Table{Columns: []string{"path", "human_writes", "agent_writes", "unknown_writes", "human_ratio", "human_bytes_delta", "agent_bytes_delta", "unknown_bytes_delta"}, Rows: [][]any{{p.Extra["path"], human, agent, unknown, ratio, hb, ab, ub}}, Total: 1}, nil
	},
}
