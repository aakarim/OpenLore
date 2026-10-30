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

// lineCoverage is the union of line ranges read at one content hash. Reads
// append ranges; normalize sorts and coalesces them once, before the
// coverage is stored, merged or used.
type lineCoverage struct {
	Whole  bool     `json:"whole,omitempty"`
	Ranges [][2]int `json:"ranges,omitempty"`
}

func (c *lineCoverage) add(start, end int) { c.Ranges = append(c.Ranges, [2]int{start, end}) }

func (c *lineCoverage) normalize() {
	sort.Slice(c.Ranges, func(i, j int) bool { return c.Ranges[i][0] < c.Ranges[j][0] })
	c.Ranges = coalesceRanges(c.Ranges[:0], c.Ranges)
}

// coalesceRanges appends sorted ranges to dst, joining overlapping and
// adjacent ones. dst may alias ranges.
func coalesceRanges(dst, ranges [][2]int) [][2]int {
	for _, r := range ranges {
		if last := len(dst) - 1; last >= 0 && r[0] <= dst[last][1]+1 {
			dst[last][1] = max(dst[last][1], r[1])
		} else {
			dst = append(dst, r)
		}
	}
	return dst
}

func (c lineCoverage) MarshalJSON() ([]byte, error) {
	c.Ranges = append([][2]int(nil), c.Ranges...)
	c.normalize()
	type plain lineCoverage
	return json.Marshal(plain(c))
}

// merge takes the union with later in linear time over sorted ranges.
func (c *lineCoverage) merge(later *lineCoverage) {
	c.Whole = c.Whole || later.Whole
	c.normalize()
	later.normalize()
	sorted := make([][2]int, 0, len(c.Ranges)+len(later.Ranges))
	i, j := 0, 0
	for i < len(c.Ranges) || j < len(later.Ranges) {
		if j == len(later.Ranges) || i < len(c.Ranges) && c.Ranges[i][0] <= later.Ranges[j][0] {
			sorted = append(sorted, c.Ranges[i])
			i++
		} else {
			sorted = append(sorted, later.Ranges[j])
			j++
		}
	}
	c.Ranges = coalesceRanges(sorted[:0], sorted)
}

type scalarObservation struct {
	Time   time.Time `json:"time"`
	Hash   string    `json:"hash"`
	Lines  int       `json:"lines"`
	Tokens *float64  `json:"tokens,omitempty"`
}

type fileReads struct {
	// Origin describes the usage inherited from the beginning of this partial.
	// nil means this path, an empty string means no inheritance (a moved-away
	// path), and another path means that file was moved here. This compact
	// transformation composes across windows without retaining move events.
	Origin   *string                  `json:"origin,omitempty"`
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
	prefix      string
	summaryOnly bool
	pathBytes   int
	Files       map[string]*fileReads `json:"files"`
	Overflow    bool                  `json:"overflow,omitempty"`
}

// Rankings retain only per-file summaries. Bound both their cardinality and
// path storage, including deleted files that only exist in recorded events.
const maxFileUsagePaths = 25_000
const maxFileUsagePathBytes = 4 * 1024 * 1024

func fileUsageBudgetError() error {
	return fmt.Errorf("file usage exceeds memory budget; narrow --param path or --since")
}

func newFileUsagePartial(prefix string) *fileUsagePartial {
	return &fileUsagePartial{prefix: vfs.CleanPath(prefix), Files: map[string]*fileReads{}}
}

func (f *fileUsagePartial) file(path string) *fileReads {
	// A restored checkpoint does not contain the running byte count. Add can
	// resume a checkpoint too, without first calling Merge.
	if f.pathBytes == 0 {
		for path := range f.Files {
			f.pathBytes += len(path)
		}
	}
	file := f.Files[path]
	if file == nil {
		if f.summaryOnly && (len(f.Files) >= maxFileUsagePaths || len(path) > maxFileUsagePathBytes-f.pathBytes) {
			f.Overflow = true
			return nil
		}
		file = &fileReads{}
		f.Files[path] = file
		f.pathBytes += len(path)
	}
	return file
}

func (f *fileUsagePartial) Add(e Event) {
	if f.Overflow {
		return
	}
	if e.Type != "doc.read" && e.Type != "doc.hit" && e.Type != "doc.scalars" && e.Type != "doc.write" {
		return
	}
	if e.Type == "doc.write" && fieldString(e, "action") != "move" && (!fieldBool(e, "generated") || e.Fields["after"] == nil) {
		return
	}
	filePath := vfs.CleanPath(fieldString(e, "path"))
	if !pathWithin(f.prefix, filePath) {
		return
	}
	if e.Type == "doc.write" && fieldString(e, "action") == "move" {
		source, ok := e.Fields["from"].(string)
		if !ok || source == "" {
			return
		}
		source = vfs.CleanPath(source)
		// Do not carry usage across query scopes, even if an unfiltered source
		// supplies the event. ACL-aware sources must also authorize both ends.
		if source == filePath || !pathWithin(f.prefix, source) {
			return
		}
		previous := f.file(source)
		if previous == nil || f.file(filePath) == nil {
			return
		}
		moved := *previous
		if moved.Origin == nil {
			moved.Origin = &source
		}
		f.Files[filePath] = &moved
		f.Files[source] = &fileReads{Origin: new(string)}
		return
	}
	// Scalar replay can arrive after the destination has moved again or been
	// reused. Only live doc.write establishes lineage; replay must neither
	// transfer usage twice nor replace the scalars of a later occupant.
	// The source's inventory removal can also precede the live move, and
	// must not erase the scalars that the destination is about to inherit.
	action := fieldString(e, "action")
	if e.Type == "doc.scalars" && (action == "move" || action == "move_source") {
		return
	}
	file := f.file(filePath)
	if file == nil {
		return
	}
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
		if f.summaryOnly {
			file.ReadHash = ""
			return
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
	case "doc.scalars", "doc.write":
		// Generated writes carry final scalars at the observer's timestamp,
		// after move lineage is established, unlike commit-time inventory.
		if file.Scalars == nil || !e.Time.Before(file.Scalars.Time) {
			after := scalarFields(e.Fields["after"])
			observation := &scalarObservation{Time: e.Time, Hash: fieldString(e, "content_hash"), Lines: int(after["lines"])}
			if f.summaryOnly {
				observation.Hash, observation.Lines = "", 0
			}
			if tokens, ok := after["tokens"]; ok {
				observation.Tokens = &tokens
			}
			file.Scalars = observation
		}
	}
}

func (f *fileUsagePartial) Merge(later Partial) {
	nextPartial := later.(*fileUsagePartial)
	if f.Overflow || nextPartial.Overflow {
		f.Overflow = true
		return
	}
	// Reserve all touched paths before composing. Tombstones count against
	// the same budget as reads, so arbitrarily long move chains stay bounded.
	for path := range nextPartial.Files {
		if f.file(path) == nil {
			return
		}
	}
	// Resolve every inherited input before replacing any output: a window
	// can contain chains, cycles, source reuse, or destination overwrites.
	composed := make(map[string]*fileReads, len(nextPartial.Files))
	for path, next := range nextPartial.Files {
		origin := path
		if next.Origin != nil {
			origin = *next.Origin
		}
		file := &fileReads{Origin: next.Origin}
		if previous := f.Files[origin]; origin != "" && previous != nil {
			*file = *previous
			if file.Origin == nil && origin != path {
				file.Origin = &origin
			}
			// Coverage unions below must not mutate the input snapshot.
			if !f.summaryOnly && len(previous.Coverage) > 0 {
				file.Coverage = make(map[string]*lineCoverage, len(previous.Coverage))
				for hash, coverage := range previous.Coverage {
					copied := *coverage
					copied.Ranges = append([][2]int(nil), coverage.Ranges...)
					file.Coverage[hash] = &copied
				}
			}
		}
		file.Reads += next.Reads
		file.Hits += next.Hits
		if next.LastRead != nil && (file.LastRead == nil || next.LastRead.After(*file.LastRead)) {
			file.LastRead, file.ReadHash = next.LastRead, next.ReadHash
		}
		if next.Scalars != nil && (file.Scalars == nil || !next.Scalars.Time.Before(file.Scalars.Time)) {
			file.Scalars = next.Scalars
		}
		for hash, coverage := range next.Coverage {
			if f.summaryOnly {
				break
			}
			if file.Coverage == nil {
				file.Coverage = map[string]*lineCoverage{}
			}
			if current := file.Coverage[hash]; current != nil {
				current.merge(coverage)
			} else {
				copied := *coverage
				copied.Ranges = append([][2]int(nil), coverage.Ranges...)
				file.Coverage[hash] = &copied
			}
		}
		composed[path] = file
	}
	for path, file := range composed {
		f.Files[path] = file
	}
}

// usage joins recorded reads with the current inventory under prefix.
func (f *fileUsagePartial) usage(ctx context.Context, facts ContentFacts) ([]DocUsage, error) {
	if f.summaryOnly {
		bytes := 0
		for path := range f.Files {
			bytes += len(path)
		}
		f.Overflow = f.Overflow || len(f.Files) > maxFileUsagePaths || bytes > maxFileUsagePathBytes
	}
	if f.Overflow {
		return nil, fileUsageBudgetError()
	}
	var result []DocUsage
	pathBytes := 0
	if err := facts.Walk(ctx, f.prefix, WalkOptions{StatOnly: true}, func(d DocScalars) error {
		if f.summaryOnly && (len(result) >= maxFileUsagePaths || len(d.Path) > maxFileUsagePathBytes-pathBytes) {
			return fileUsageBudgetError()
		}
		pathBytes += len(d.Path)
		u := DocUsage{Path: d.Path, Scalars: map[string]float64{"bytes": d.Scalars["bytes"]}}
		if file := f.Files[d.Path]; file != nil {
			u.Reads, u.Hits, u.LastReadAt, u.ContentHash = file.Reads, file.Hits, file.LastRead, file.ReadHash
			if state := file.Scalars; state != nil {
				if state.Tokens != nil {
					u.Scalars["tokens"] = *state.Tokens
				}
				if !f.summaryOnly && state.Hash != "" && state.Lines > 0 {
					u.ContentHash = state.Hash
					covered := make([]bool, state.Lines)
					if coverage := file.Coverage[state.Hash]; coverage != nil {
						coverage.normalize()
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

var fileUsageTypes = []string{"doc.read", "doc.hit", "doc.scalars", "doc.write"}

func newPrefixFileUsage(p Params) Partial {
	prefix := p.Extra["path"]
	if prefix == "" {
		prefix = "/"
	}
	partial := newFileUsagePartial(prefix)
	partial.summaryOnly = true
	return partial
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
		total := len(usage)
		if p.Limit > 0 && len(usage) > p.Limit {
			usage = usage[:p.Limit]
		}
		rows := make([][]any, 0, len(usage))
		note := ""
		for _, u := range usage {
			// Preserve in-window observations, but do not require a write in
			// the window to report context cost. Stat uses the current facts cache.
			if _, ok := u.Scalars["tokens"]; !ok {
				current, err := facts.Stat(ctx, u.Path)
				if ctx.Err() != nil {
					return Table{}, ctx.Err()
				}
				if err != nil {
					// Keep usage visible even if the content cannot be read.
					// Do not expose errors that may contain host paths.
					if note == "" {
						note = "Token counts unavailable for unreadable files, first: " + u.Path
					}
				} else if value, ok := current.Scalars["tokens"]; ok {
					u.Scalars["tokens"] = value
				}
			}
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
		return Table{Columns: []string{"path", "last_read_at", "reads", "hits", "tokens", "reads_per_ktok"}, Rows: rows, Total: total, Note: note}, nil
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

// lineReads counts reads of one range of one content hash.
type lineReads struct {
	Hash   string    `json:"hash"`
	Start  int       `json:"start"`
	End    int       `json:"end"`
	Ranged bool      `json:"ranged,omitempty"`
	Count  int       `json:"count"`
	Last   time.Time `json:"last"`
}

// lineReadsPartial counts reads of one file by content hash and range, so
// repeated reads of the same range are kept once. Only reads of the current
// content hash count, which is decided when the table is built.
type lineReadsPartial struct {
	path   string
	Ranges map[string]*lineReads `json:"ranges"`
}

func (l *lineReadsPartial) add(read lineReads) {
	key := fmt.Sprintf("%s\x00%d\x00%d\x00%t", read.Hash, read.Start, read.End, read.Ranged)
	current := l.Ranges[key]
	if current == nil {
		l.Ranges[key] = &read
		return
	}
	current.Count += read.Count
	if read.Last.After(current.Last) {
		current.Last = read.Last
	}
}

func (l *lineReadsPartial) Add(e Event) {
	if vfs.CleanPath(fieldString(e, "path")) != l.path {
		return
	}
	start, end, ranged := eventLineRange(e)
	if !ranged {
		start, end = 0, 0
	}
	l.add(lineReads{Hash: fieldString(e, "content_hash"), Start: start, End: end, Ranged: ranged, Count: 1, Last: e.Time})
}

func (l *lineReadsPartial) Merge(later Partial) {
	for _, read := range later.(*lineReadsPartial).Ranges {
		l.add(*read)
	}
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
		New: func(p Params) Partial {
			return &lineReadsPartial{path: vfs.CleanPath(p.Extra["path"]), Ranges: map[string]*lineReads{}}
		},
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
			for _, read := range partial.(*lineReadsPartial).Ranges {
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
					lines[i-1].reads += read.Count
					if lines[i-1].last == nil || read.Last.After(*lines[i-1].last) {
						last := read.Last
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
