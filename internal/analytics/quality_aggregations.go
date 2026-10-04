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

type queryStats struct {
	Count      int             `json:"count"`
	Filled     int             `json:"filled"`
	Last       time.Time       `json:"last"`
	Principals map[string]bool `json:"principals"`
}

// queriesPartial rolls up search patterns, optionally only filled or only
// unfilled searches.
type queriesPartial struct {
	filled  *bool
	Queries map[string]*queryStats `json:"queries"`
}

func newQueriesPartial(filled *bool) *queriesPartial {
	return &queriesPartial{filled: filled, Queries: map[string]*queryStats{}}
}

func (q *queriesPartial) Add(e Event) {
	isFilled := fieldBool(e, "filled")
	if q.filled != nil && isFilled != *q.filled {
		return
	}
	stats := &queryStats{Count: 1, Last: e.Time, Principals: map[string]bool{e.Principal: true}}
	if isFilled {
		stats.Filled = 1
	}
	q.merge(strings.TrimSpace(fieldString(e, "pattern")), stats)
}

func (q *queriesPartial) merge(pattern string, later *queryStats) {
	current := q.Queries[pattern]
	if current == nil {
		q.Queries[pattern] = later
		return
	}
	current.Count += later.Count
	current.Filled += later.Filled
	if later.Last.After(current.Last) {
		current.Last = later.Last
	}
	current.Principals = mergeSet(current.Principals, later.Principals)
}

func (q *queriesPartial) Merge(later Partial) {
	for pattern, stats := range later.(*queriesPartial).Queries {
		q.merge(pattern, stats)
	}
}

func (q *queriesPartial) counts(limit int) []QueryCount {
	result := make([]QueryCount, 0, len(q.Queries))
	for pattern, r := range q.Queries {
		result = append(result, QueryCount{Pattern: pattern, Count: r.Count, FilledRatio: float64(r.Filled) / float64(r.Count), LastSeen: r.Last, Principals: len(r.Principals)})
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
	return result
}

func TopSearchQueries(ctx context.Context, src EventSource, f EventFilter, filled *bool, limit int) ([]QueryCount, error) {
	partial := newQueriesPartial(filled)
	f.Types = []string{"search.query"}
	if err := src.Scan(ctx, f, func(e Event) error {
		partial.Add(e)
		return nil
	}); err != nil {
		return nil, err
	}
	return partial.counts(limit), nil
}

func searchQueriesTable(filled *bool) Incremental {
	return Incremental{
		Types: []string{"search.query"},
		New:   func(Params) Partial { return newQueriesPartial(filled) },
		Table: func(_ context.Context, partial Partial, _ ContentFacts, p Params) (Table, error) {
			queries := partial.(*queriesPartial).counts(0)
			rows := make([][]any, 0, len(queries))
			for _, query := range queries {
				if filled == nil {
					rows = append(rows, []any{query.Pattern, query.Count, query.FilledRatio})
				} else {
					rows = append(rows, []any{query.Pattern, query.Count, query.LastSeen, query.Principals})
				}
			}
			columns := []string{"pattern", "count", "filled_ratio"}
			if filled != nil {
				columns = []string{"pattern", "count", "last_seen", "principals"}
			}
			return Table{Columns: columns, Rows: limitRows(rows, p), Total: len(queries)}, nil
		},
	}
}

// lineCoverage is the union of line ranges read at one content hash.
type lineCoverage struct {
	Whole  bool     `json:"whole,omitempty"`
	Ranges [][2]int `json:"ranges,omitempty"`
}

func (c *lineCoverage) add(start, end int) {
	merged := make([][2]int, 0, len(c.Ranges)+1)
	for _, r := range c.Ranges {
		if r[1]+1 < start || end+1 < r[0] {
			merged = append(merged, r)
			continue
		}
		start, end = min(start, r[0]), max(end, r[1])
	}
	merged = append(merged, [2]int{start, end})
	sort.Slice(merged, func(i, j int) bool { return merged[i][0] < merged[j][0] })
	c.Ranges = merged
}

func (c *lineCoverage) merge(later *lineCoverage) {
	c.Whole = c.Whole || later.Whole
	for _, r := range later.Ranges {
		c.add(r[0], r[1])
	}
}

type scalarObservation struct {
	Time   time.Time `json:"time"`
	Hash   string    `json:"hash"`
	Lines  int       `json:"lines"`
	Tokens *float64  `json:"tokens,omitempty"`
}

type fileReads struct {
	Reads    int                      `json:"reads"`
	Hits     int                      `json:"hits"`
	LastRead *time.Time               `json:"last_read,omitempty"`
	ReadHash string                   `json:"read_hash,omitempty"`
	Scalars  *scalarObservation       `json:"scalars,omitempty"`
	Coverage map[string]*lineCoverage `json:"coverage,omitempty"`
}

// fileUsagePartial records reads and the latest scalars of files under a
// prefix. Whether a file still exists is decided against current facts.
type fileUsagePartial struct {
	prefix string
	Files  map[string]*fileReads `json:"files"`
}

func newFileUsagePartial(prefix string) *fileUsagePartial {
	return &fileUsagePartial{prefix: vfs.CleanPath(prefix), Files: map[string]*fileReads{}}
}

func (f *fileUsagePartial) file(path string) *fileReads {
	file := f.Files[path]
	if file == nil {
		file = &fileReads{}
		f.Files[path] = file
	}
	return file
}

func (f *fileUsagePartial) Add(e Event) {
	filePath := vfs.CleanPath(fieldString(e, "path"))
	if !pathWithin(f.prefix, filePath) {
		return
	}
	file := f.file(filePath)
	switch e.Type {
	case "doc.read", "doc.hit":
		if e.Type == "doc.read" {
			file.Reads++
		} else {
			file.Hits++
		}
		hash := fieldString(e, "content_hash")
		if file.LastRead == nil || e.Time.After(*file.LastRead) {
			last := e.Time
			file.LastRead, file.ReadHash = &last, hash
		}
		if file.Coverage == nil {
			file.Coverage = map[string]*lineCoverage{}
		}
		coverage := file.Coverage[hash]
		if coverage == nil {
			coverage = &lineCoverage{}
			file.Coverage[hash] = coverage
		}
		if start, end, ranged := eventLineRange(e); ranged {
			coverage.add(start, end)
		} else {
			coverage.Whole = true
		}
	case "doc.scalars":
		if file.Scalars == nil || !e.Time.Before(file.Scalars.Time) {
			after := scalarFields(e.Fields["after"])
			observation := &scalarObservation{Time: e.Time, Hash: fieldString(e, "content_hash"), Lines: int(after["lines"])}
			if tokens, ok := after["tokens"]; ok {
				observation.Tokens = &tokens
			}
			file.Scalars = observation
		}
	}
}

func (f *fileUsagePartial) Merge(later Partial) {
	for path, next := range later.(*fileUsagePartial).Files {
		file := f.file(path)
		file.Reads += next.Reads
		file.Hits += next.Hits
		if next.LastRead != nil && (file.LastRead == nil || next.LastRead.After(*file.LastRead)) {
			file.LastRead, file.ReadHash = next.LastRead, next.ReadHash
		}
		if next.Scalars != nil && (file.Scalars == nil || !next.Scalars.Time.Before(file.Scalars.Time)) {
			file.Scalars = next.Scalars
		}
		for hash, coverage := range next.Coverage {
			if file.Coverage == nil {
				file.Coverage = map[string]*lineCoverage{}
			}
			if current := file.Coverage[hash]; current != nil {
				current.merge(coverage)
			} else {
				file.Coverage[hash] = coverage
			}
		}
	}
}

// usage joins recorded reads with the current inventory under prefix.
func (f *fileUsagePartial) usage(ctx context.Context, facts ContentFacts) ([]DocUsage, error) {
	var result []DocUsage
	if err := facts.Walk(ctx, f.prefix, WalkOptions{StatOnly: true}, func(d DocScalars) error {
		u := DocUsage{Path: d.Path, Scalars: map[string]float64{"bytes": d.Scalars["bytes"]}}
		if file := f.Files[d.Path]; file != nil {
			u.Reads, u.Hits, u.LastReadAt, u.ContentHash = file.Reads, file.Hits, file.LastRead, file.ReadHash
			if state := file.Scalars; state != nil {
				if state.Tokens != nil {
					u.Scalars["tokens"] = *state.Tokens
				}
				if state.Hash != "" && state.Lines > 0 {
					u.ContentHash = state.Hash
					covered := make([]bool, state.Lines)
					if coverage := file.Coverage[state.Hash]; coverage != nil {
						for _, r := range coverage.Ranges {
							for line := max(r[0], 1); line <= min(r[1], state.Lines); line++ {
								covered[line-1] = true
							}
						}
						if coverage.Whole {
							for line := range covered {
								covered[line] = true
							}
						}
					}
					u.ColdUnits = coldLineUnits(covered)
				}
			}
		}
		result = append(result, u)
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func FileUsage(ctx context.Context, src EventSource, facts ContentFacts, prefix string, f EventFilter) ([]DocUsage, error) {
	partial := newFileUsagePartial(prefix)
	f.Types = fileUsageTypes
	if err := src.Scan(ctx, f, func(e Event) error {
		partial.Add(e)
		return nil
	}); err != nil {
		return nil, err
	}
	return partial.usage(ctx, facts)
}

var fileUsageTypes = []string{"doc.read", "doc.hit", "doc.scalars"}

func newPrefixFileUsage(p Params) Partial {
	prefix := p.Extra["path"]
	if prefix == "" {
		prefix = "/"
	}
	return newFileUsagePartial(prefix)
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

func fileUsageTable(order string) Incremental {
	return Incremental{Types: fileUsageTypes, New: newPrefixFileUsage, Table: func(ctx context.Context, partial Partial, facts ContentFacts, p Params) (Table, error) {
		sortOrder := order
		if requested := p.Extra["order"]; requested == "asc" || requested == "desc" {
			sortOrder = requested
		}
		usage, err := partial.(*fileUsagePartial).usage(ctx, facts)
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
	}}
}

type folderUsage struct {
	path      string
	last      *time.Time
	files     int
	neverRead int
}

var leastUsedFolders = Incremental{Types: fileUsageTypes, New: newPrefixFileUsage, Table: func(ctx context.Context, partial Partial, facts ContentFacts, p Params) (Table, error) {
	usage := partial.(*fileUsagePartial)
	prefix := usage.prefix
	depth := 1
	if raw := p.Extra["depth"]; raw != "" {
		depth, _ = strconv.Atoi(raw)
	}
	files, err := usage.usage(ctx, facts)
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
}}

// Bound both the per-line working set and the worst-case number of result
// groups. A byte-size limit alone cannot bound newline-heavy files adequately.
const maxLineUsageLines = 100_000

type lineRead struct {
	Hash   string    `json:"hash"`
	Start  int       `json:"start"`
	End    int       `json:"end"`
	Ranged bool      `json:"ranged,omitempty"`
	Time   time.Time `json:"time"`
}

// lineReadsPartial records reads of one file; only reads of its current
// content hash count, which is decided when the table is built.
type lineReadsPartial struct {
	path  string
	Reads []lineRead `json:"reads"`
}

func (l *lineReadsPartial) Add(e Event) {
	if vfs.CleanPath(fieldString(e, "path")) != l.path {
		return
	}
	start, end, ranged := eventLineRange(e)
	l.Reads = append(l.Reads, lineRead{Hash: fieldString(e, "content_hash"), Start: start, End: end, Ranged: ranged, Time: e.Time})
}

func (l *lineReadsPartial) Merge(later Partial) {
	l.Reads = append(l.Reads, later.(*lineReadsPartial).Reads...)
}

func checkLineUsage(ctx context.Context, facts ContentFacts, p Params) (DocScalars, error) {
	filePath := p.Extra["path"]
	if filePath == "" {
		return DocScalars{}, fmt.Errorf("path is required")
	}
	current, err := facts.Stat(ctx, filePath)
	if err != nil {
		return DocScalars{}, err
	}
	if current.ContentHash == "" {
		return DocScalars{}, fmt.Errorf("path must identify a file")
	}
	if current.Scalars["lines"] > maxLineUsageLines {
		return DocScalars{}, fmt.Errorf("line usage is unavailable for files exceeding %d lines", maxLineUsageLines)
	}
	return current, nil
}

func usedLines(most bool) Incremental {
	return Incremental{
		Types: []string{"doc.read", "doc.hit"},
		New:   func(p Params) Partial { return &lineReadsPartial{path: vfs.CleanPath(p.Extra["path"])} },
		Check: func(ctx context.Context, facts ContentFacts, p Params) error {
			_, err := checkLineUsage(ctx, facts, p)
			return err
		},
		Table: func(ctx context.Context, partial Partial, facts ContentFacts, p Params) (Table, error) {
			current, err := checkLineUsage(ctx, facts, p)
			if err != nil {
				return Table{}, err
			}
			filePath := vfs.CleanPath(p.Extra["path"])
			lineCount := int(current.Scalars["lines"])
			type lineUsage struct {
				reads int
				last  *time.Time
			}
			lines := make([]lineUsage, lineCount)
			for _, read := range partial.(*lineReadsPartial).Reads {
				if read.Hash != current.ContentHash {
					continue
				}
				start, end := read.Start, read.End
				if !read.Ranged {
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
					if lines[i-1].last == nil || read.Time.After(*lines[i-1].last) {
						last := read.Time
						lines[i-1].last = &last
					}
				}
			}
			var rows [][]any
			for start := 0; start < len(lines); {
				end := start
				for end+1 < len(lines) && sameLineUsage(lines[start], lines[end+1]) {
					end++
				}
				rows = append(rows, []any{filePath, start + 1, end + 1, lines[start].last, lines[start].reads})
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
		},
	}
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
