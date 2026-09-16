package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

type QueryCount struct {
	Pattern     string
	Count       int
	FilledRatio float64
	LastSeen    time.Time
	Principals  int
}

type DocUsage struct {
	Path        string
	ContentHash string
	LastReadAt  *time.Time
	Reads       int
	Hits        int
	Scalars     map[string]float64
	ColdUnits   []ContentUnit
}

func TopSearchQueries(ctx context.Context, src EventSource, f EventFilter, filled *bool, limit int) ([]QueryCount, error) {
	type rollup struct {
		count, filled int
		last          time.Time
		principals    map[string]struct{}
	}
	rollups := map[string]*rollup{}
	f.Types = []string{"search.query"}
	err := src.Scan(ctx, f, func(e Event) error {
		isFilled := fieldBool(e, "filled")
		if filled != nil && isFilled != *filled {
			return nil
		}
		pattern := strings.TrimSpace(fieldString(e, "pattern"))
		r := rollups[pattern]
		if r == nil {
			r = &rollup{principals: map[string]struct{}{}}
			rollups[pattern] = r
		}
		r.count++
		if isFilled {
			r.filled++
		}
		if e.Time.After(r.last) {
			r.last = e.Time
		}
		r.principals[e.Principal] = struct{}{}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := make([]QueryCount, 0, len(rollups))
	for pattern, r := range rollups {
		result = append(result, QueryCount{Pattern: pattern, Count: r.count, FilledRatio: float64(r.filled) / float64(r.count), LastSeen: r.last, Principals: len(r.principals)})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].Pattern < result[j].Pattern
	})
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func FileUsage(ctx context.Context, src EventSource, facts ContentFacts, prefix string, f EventFilter) ([]DocUsage, error) {
	type observedUnit struct {
		hash       string
		start, end int
		ranged     bool
	}
	type scalarState struct {
		time  time.Time
		hash  string
		lines int
	}
	usage := map[string]*DocUsage{}
	if err := facts.Walk(ctx, prefix, WalkOptions{StatOnly: true}, func(d DocScalars) error {
		usage[d.Path] = &DocUsage{Path: d.Path, Scalars: map[string]float64{"bytes": d.Scalars["bytes"]}}
		return nil
	}); err != nil {
		return nil, err
	}
	scalars := map[string]scalarState{}
	units := map[string][]observedUnit{}
	f.Types = []string{"doc.read", "doc.hit", "doc.scalars"}
	if err := src.Scan(ctx, f, func(e Event) error {
		filePath := vfs.CleanPath(fieldString(e, "path"))
		u := usage[filePath]
		if u == nil {
			return nil
		}
		switch e.Type {
		case "doc.read", "doc.hit":
			if e.Type == "doc.read" {
				u.Reads++
			} else {
				u.Hits++
			}
			if u.LastReadAt == nil || e.Time.After(*u.LastReadAt) {
				last := e.Time
				u.LastReadAt = &last
				u.ContentHash = fieldString(e, "content_hash")
			}
			start, end, ranged := eventLineRange(e)
			units[filePath] = append(units[filePath], observedUnit{hash: fieldString(e, "content_hash"), start: start, end: end, ranged: ranged})
		case "doc.scalars":
			if state := scalars[filePath]; !e.Time.Before(state.time) {
				after := scalarFields(e.Fields["after"])
				state = scalarState{time: e.Time, hash: fieldString(e, "content_hash"), lines: int(after["lines"])}
				scalars[filePath] = state
				delete(u.Scalars, "tokens")
				if tokens, ok := after["tokens"]; ok {
					u.Scalars["tokens"] = tokens
				}
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	result := make([]DocUsage, 0, len(usage))
	for _, u := range usage {
		if state := scalars[u.Path]; state.hash != "" && state.lines > 0 {
			u.ContentHash = state.hash
			covered := make([]bool, state.lines)
			for _, unit := range units[u.Path] {
				if unit.hash != state.hash {
					continue
				}
				start, end := unit.start, unit.end
				if !unit.ranged {
					start, end = 1, state.lines
				}
				if start < 1 {
					start = 1
				}
				if end > state.lines {
					end = state.lines
				}
				for line := start; line <= end; line++ {
					covered[line-1] = true
				}
			}
			u.ColdUnits = coldLineUnits(covered)
		}
		result = append(result, *u)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func coldLineUnits(covered []bool) []ContentUnit {
	var units []ContentUnit
	for start := 0; start < len(covered); {
		if covered[start] {
			start++
			continue
		}
		end := start
		for end+1 < len(covered) && !covered[end+1] {
			end++
		}
		units = append(units, ContentUnit{Lines: &LineRange{Start: start + 1, End: end + 1}})
		start = end + 1
	}
	return units
}

func topSearchQueriesTable(ctx context.Context, src EventSource, _ ContentFacts, p Params) (Table, error) {
	queries, err := TopSearchQueries(ctx, src, EventFilter{From: p.Since, To: p.Until}, nil, 0)
	if err != nil {
		return Table{}, err
	}
	rows := make([][]any, 0, len(queries))
	for _, query := range queries {
		rows = append(rows, []any{query.Pattern, query.Count, query.FilledRatio})
	}
	return Table{Columns: []string{"pattern", "count", "filled_ratio"}, Rows: limitRows(rows, p), Total: len(queries)}, nil
}

func topUnfilledQueriesTable(ctx context.Context, src EventSource, _ ContentFacts, p Params) (Table, error) {
	filled := false
	queries, err := TopSearchQueries(ctx, src, EventFilter{From: p.Since, To: p.Until}, &filled, 0)
	if err != nil {
		return Table{}, err
	}
	rows := make([][]any, 0, len(queries))
	for _, query := range queries {
		rows = append(rows, []any{query.Pattern, query.Count, query.LastSeen, query.Principals})
	}
	return Table{Columns: []string{"pattern", "count", "last_seen", "principals"}, Rows: limitRows(rows, p), Total: len(queries)}, nil
}

func fileUsageTable(order string) func(context.Context, EventSource, ContentFacts, Params) (Table, error) {
	return func(ctx context.Context, src EventSource, facts ContentFacts, p Params) (Table, error) {
		sortOrder := order
		if requested := p.Extra["order"]; requested == "asc" || requested == "desc" {
			sortOrder = requested
		}
		prefix := p.Extra["path"]
		if prefix == "" {
			prefix = "/"
		}
		usage, err := FileUsage(ctx, src, facts, prefix, EventFilter{From: p.Since, To: p.Until})
		if err != nil {
			return Table{}, err
		}
		sort.Slice(usage, func(i, j int) bool {
			left, right := usage[i].LastReadAt, usage[j].LastReadAt
			if left == nil || right == nil {
				if left == nil && right == nil {
					return usage[i].Path < usage[j].Path
				}
				if sortOrder == "desc" {
					return right == nil
				}
				return left == nil
			}
			if left.Equal(*right) {
				return usage[i].Path < usage[j].Path
			}
			if sortOrder == "desc" {
				return left.After(*right)
			}
			return left.Before(*right)
		})
		rows := make([][]any, 0, len(usage))
		for _, u := range usage {
			var tokens any
			var readsPerKTok any
			if value, ok := u.Scalars["tokens"]; ok {
				tokens = value
				if value > 0 {
					readsPerKTok = float64(u.Reads) / (value / 1000)
				}
			}
			rows = append(rows, []any{u.Path, u.LastReadAt, u.Reads, u.Hits, tokens, readsPerKTok})
		}
		return Table{Columns: []string{"path", "last_read_at", "reads", "hits", "tokens", "reads_per_ktok"}, Rows: limitRows(rows, p), Total: len(rows)}, nil
	}
}

type folderUsage struct {
	path      string
	last      *time.Time
	files     int
	neverRead int
}

func leastUsedFolders(ctx context.Context, src EventSource, facts ContentFacts, p Params) (Table, error) {
	prefix := p.Extra["path"]
	if prefix == "" {
		prefix = "/"
	}
	prefix = vfs.CleanPath(prefix)
	depth := 1
	if raw := p.Extra["depth"]; raw != "" {
		depth, _ = strconv.Atoi(raw)
	}
	files, err := FileUsage(ctx, src, facts, prefix, EventFilter{From: p.Since, To: p.Until})
	if err != nil {
		return Table{}, err
	}
	folders := map[string]*folderUsage{}
	for _, file := range files {
		for folder := path.Dir(file.Path); ; folder = path.Dir(folder) {
			if !pathWithin(prefix, folder) {
				break
			}
			relative := strings.Trim(strings.TrimPrefix(folder, prefix), "/")
			level := 0
			if relative != "" {
				level = strings.Count(relative, "/") + 1
			}
			if depth <= 0 || level <= depth {
				r := folders[folder]
				if r == nil {
					r = &folderUsage{path: folder}
					folders[folder] = r
				}
				r.files++
				if file.LastReadAt == nil {
					r.neverRead++
				} else if r.last == nil || file.LastReadAt.After(*r.last) {
					last := *file.LastReadAt
					r.last = &last
				}
			}
			if folder == prefix || folder == "/" {
				break
			}
		}
	}
	result := make([]*folderUsage, 0, len(folders))
	for _, folder := range folders {
		result = append(result, folder)
	}
	order := p.Extra["order"]
	sort.Slice(result, func(i, j int) bool {
		if result[i].last == nil || result[j].last == nil {
			if result[i].last == nil && result[j].last == nil {
				return result[i].path < result[j].path
			}
			if order == "desc" {
				return result[j].last == nil
			}
			return result[i].last == nil
		}
		if result[i].last.Equal(*result[j].last) {
			return result[i].path < result[j].path
		}
		if order == "desc" {
			return result[i].last.After(*result[j].last)
		}
		return result[i].last.Before(*result[j].last)
	})
	rows := make([][]any, 0, len(result))
	for _, folder := range result {
		rows = append(rows, []any{folder.path, folder.last, folder.files, folder.neverRead})
	}
	return Table{Columns: []string{"path", "last_read_at", "files", "never_read"}, Rows: limitRows(rows, p), Total: len(rows)}, nil
}

func leastUsedLines(ctx context.Context, src EventSource, facts ContentFacts, p Params) (Table, error) {
	return usedLines(ctx, src, facts, p, false)
}

func mostUsedLines(ctx context.Context, src EventSource, facts ContentFacts, p Params) (Table, error) {
	return usedLines(ctx, src, facts, p, true)
}

func usedLines(ctx context.Context, src EventSource, facts ContentFacts, p Params, most bool) (Table, error) {
	filePath := p.Extra["path"]
	if filePath == "" {
		return Table{}, fmt.Errorf("path is required")
	}
	current, err := facts.Stat(ctx, filePath)
	if err != nil {
		return Table{}, err
	}
	if current.ContentHash == "" {
		return Table{}, fmt.Errorf("path must identify a file")
	}
	lineCount := int(current.Scalars["lines"])
	type lineUsage struct {
		reads int
		last  *time.Time
	}
	lines := make([]lineUsage, lineCount)
	err = src.Scan(ctx, EventFilter{From: p.Since, To: p.Until, Types: []string{"doc.read", "doc.hit"}}, func(e Event) error {
		if vfs.CleanPath(fieldString(e, "path")) != vfs.CleanPath(filePath) || fieldString(e, "content_hash") != current.ContentHash {
			return nil
		}
		start, end, ok := eventLineRange(e)
		if !ok {
			start, end = 1, lineCount
		}
		if start < 1 {
			start = 1
		}
		if end > lineCount {
			end = lineCount
		}
		for i := start; i <= end; i++ {
			lines[i-1].reads++
			if lines[i-1].last == nil || e.Time.After(*lines[i-1].last) {
				last := e.Time
				lines[i-1].last = &last
			}
		}
		return nil
	})
	if err != nil {
		return Table{}, err
	}
	var rows [][]any
	for start := 0; start < len(lines); {
		end := start
		for end+1 < len(lines) && sameLineUsage(lines[start], lines[end+1]) {
			end++
		}
		rows = append(rows, []any{vfs.CleanPath(filePath), start + 1, end + 1, lines[start].last, lines[start].reads})
		start = end + 1
	}
	sort.Slice(rows, func(i, j int) bool {
		if most && rows[i][4].(int) != rows[j][4].(int) {
			return rows[i][4].(int) > rows[j][4].(int)
		}
		left, _ := rows[i][3].(*time.Time)
		right, _ := rows[j][3].(*time.Time)
		if left == nil || right == nil {
			if left == nil && right == nil {
				return rows[i][1].(int) < rows[j][1].(int)
			}
			if most {
				return right == nil
			}
			return left == nil
		}
		if left.Equal(*right) {
			return rows[i][1].(int) < rows[j][1].(int)
		}
		if most {
			return left.After(*right)
		}
		return left.Before(*right)
	})
	return Table{Columns: []string{"path", "start", "end", "last_read_at", "reads"}, Rows: limitRows(rows, p), Total: len(rows)}, nil
}

func fieldBool(e Event, key string) bool {
	switch value := e.Fields[key].(type) {
	case bool:
		return value
	case string:
		parsed, _ := strconv.ParseBool(value)
		return parsed
	default:
		return false
	}
}

func scalarFields(value any) map[string]float64 {
	result := map[string]float64{}
	switch values := value.(type) {
	case map[string]float64:
		return values
	case map[string]any:
		for key, value := range values {
			switch number := value.(type) {
			case float64:
				result[key] = number
			case int:
				result[key] = float64(number)
			}
		}
	}
	return result
}

func eventLineRange(e Event) (int, int, bool) {
	if unit, ok := e.Fields["unit"].(ContentUnit); ok && unit.Lines != nil {
		return unit.Lines.Start, unit.Lines.End, unit.Lines.Start > 0 && unit.Lines.End >= unit.Lines.Start
	}
	unit, ok := e.Fields["unit"].(map[string]any)
	if !ok {
		return 0, 0, false
	}
	lineMap, ok := unit["lines"].(map[string]any)
	if !ok {
		return 0, 0, false
	}
	startValue, endValue := lineMap["start"], lineMap["end"]
	if startValue == nil {
		startValue = lineMap["Start"]
	}
	if endValue == nil {
		endValue = lineMap["End"]
	}
	start := int(anyFloat(startValue))
	end := int(anyFloat(endValue))
	return start, end, start > 0 && end >= start
}

func anyFloat(value any) float64 {
	switch number := value.(type) {
	case float64:
		return number
	case int:
		return float64(number)
	case int64:
		return float64(number)
	case json.Number:
		parsed, _ := number.Float64()
		return parsed
	default:
		parsed, _ := strconv.ParseFloat(fmt.Sprint(value), 64)
		return parsed
	}
}

func sameLineUsage(left, right struct {
	reads int
	last  *time.Time
}) bool {
	if left.reads != right.reads || (left.last == nil) != (right.last == nil) {
		return false
	}
	return left.last == nil || left.last.Equal(*right.last)
}

func pathWithin(prefix, candidate string) bool {
	prefix, candidate = vfs.CleanPath(prefix), vfs.CleanPath(candidate)
	return prefix == "/" || candidate == prefix || strings.HasPrefix(candidate, prefix+"/")
}
