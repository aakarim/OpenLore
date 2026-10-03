package analytics

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

// Summary is a cache-free, scoped usage summary. EstimatedTokens only covers
// read ranges whose character count was captured when the read occurred.
type Summary struct {
	Reads            int               `json:"reads"`
	Hits             int               `json:"hits"`
	Writes           int               `json:"writes"`
	HumanWrites      int               `json:"human_writes"`
	AgentWrites      int               `json:"agent_writes"`
	UnknownWrites    int               `json:"unknown_writes"`
	Commands         int               `json:"commands"`
	EstimatedTokens  int64             `json:"estimated_tokens"`
	EstimatedReads   int               `json:"estimated_reads"`
	UnestimatedReads int               `json:"unestimated_reads"`
	Activity         []SummaryActivity `json:"activity"`
	ComputedAt       time.Time         `json:"computed_at"`
	Note             string            `json:"note,omitempty"`
}

type SummaryActivity struct {
	Date    string `json:"date"`
	Human   int    `json:"human"`
	Agent   int    `json:"agent"`
	Unknown int    `json:"unknown"`
	Reads   int    `json:"reads"`
	Writes  int    `json:"writes"`
}

// UsageSummary scans only source and never consults shared materializations.
// doc.write is preferred over doc.scalars for the same commit/path so one
// committed leaf contributes exactly one write.
func UsageSummary(ctx context.Context, source EventSource, p Params, charsPerToken int) (Summary, error) {
	partial, err := scanUsage(ctx, source, p.Since, p.Until, charsPerToken)
	if err != nil {
		return Summary{}, err
	}
	return partial.summary(), nil
}

// usagePartial is the mergeable state of a usage summary over one time range.
// Partials of adjacent ranges merge into the summary of their union: counters
// add, and writes stay keyed by commit/path so a commit split across ranges
// still contributes one write.
type usagePartial struct {
	Reads            int                         `json:"reads"`
	Hits             int                         `json:"hits"`
	Commands         int                         `json:"commands"`
	EstimatedTokens  int64                       `json:"estimated_tokens"`
	EstimatedReads   int                         `json:"estimated_reads"`
	UnestimatedReads int                         `json:"unestimated_reads"`
	Activity         map[string]*SummaryActivity `json:"activity"`
	Writes           map[string]usageWrite       `json:"writes"`
	// Overflow records that a read was left unestimated because the running
	// token total would overflow. That depends on every earlier read, so such
	// a partial cannot be merged exactly.
	Overflow bool `json:"overflow,omitempty"`
}

type usageWrite struct {
	Doc  bool   `json:"doc,omitempty"` // doc.write rather than doc.scalars
	Date string `json:"date"`
	Kind Writer `json:"kind"`
}

func newUsagePartial() *usagePartial {
	return &usagePartial{Activity: map[string]*SummaryActivity{}, Writes: map[string]usageWrite{}}
}

func (u *usagePartial) activity(day string) *SummaryActivity {
	row := u.Activity[day]
	if row == nil {
		row = &SummaryActivity{Date: day}
		u.Activity[day] = row
	}
	return row
}

// scanUsage streams events in [since, until]; zero bounds are open.
func scanUsage(ctx context.Context, source EventSource, since, until time.Time, charsPerToken int) (*usagePartial, error) {
	if source == nil {
		return nil, fmt.Errorf("analytics event source is unavailable")
	}
	if charsPerToken <= 0 {
		return nil, fmt.Errorf("characters per token must be positive")
	}
	u := newUsagePartial()
	err := source.Scan(ctx, EventFilter{From: since, To: until}, func(event Event) error {
		day := event.Time.UTC().Format("2006-01-02")
		switch event.Type {
		case "doc.scalars", "doc.write":
			u.addWrite(summaryWriteKey(event), usageWrite{Doc: event.Type == "doc.write", Date: day, Kind: eventKind(event)})
		case "doc.read", "doc.hit":
			row := u.activity(day)
			if event.Type == "doc.read" {
				u.Reads++
			} else {
				u.Hits++
			}
			row.Reads++
			incrementActivityKind(row, eventKind(event))
			estimated := false
			if characters, ok := numericField(event.Fields, "characters"); ok {
				estimate := math.Ceil(characters / float64(charsPerToken))
				if estimate < math.Exp2(63) {
					tokens := int64(estimate)
					if tokens <= math.MaxInt64-u.EstimatedTokens {
						u.EstimatedReads++
						u.EstimatedTokens += tokens
						estimated = true
					} else {
						u.Overflow = true
					}
				}
			}
			if !estimated {
				u.UnestimatedReads++
			}
		case "command.exec":
			u.Commands++
			incrementActivityKind(u.activity(day), eventKind(event))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return u, nil
}

// addWrite applies events in time order: a later doc.write replaces any
// earlier event, and a later doc.scalars replaces only another doc.scalars.
func (u *usagePartial) addWrite(key string, write usageWrite) {
	if existing, ok := u.Writes[key]; !ok || write.Doc || !existing.Doc {
		u.Writes[key] = write
	}
}

// merge adds a partial covering a later time range. It reports false, leaving
// u unchanged, when the token total overflows: which reads a single scan would
// leave unestimated then depends on the order of every read.
func (u *usagePartial) merge(later *usagePartial) bool {
	if u.Overflow || later.Overflow || later.EstimatedTokens > math.MaxInt64-u.EstimatedTokens {
		return false
	}
	u.Reads += later.Reads
	u.Hits += later.Hits
	u.Commands += later.Commands
	u.EstimatedReads += later.EstimatedReads
	u.UnestimatedReads += later.UnestimatedReads
	u.EstimatedTokens += later.EstimatedTokens
	for day, row := range later.Activity {
		current := u.activity(day)
		current.Human += row.Human
		current.Agent += row.Agent
		current.Unknown += row.Unknown
		current.Reads += row.Reads
		current.Writes += row.Writes
	}
	for key, write := range later.Writes {
		u.addWrite(key, write)
	}
	return true
}

func (u *usagePartial) summary() Summary {
	summary := Summary{Reads: u.Reads, Hits: u.Hits, Commands: u.Commands, EstimatedTokens: u.EstimatedTokens, EstimatedReads: u.EstimatedReads, UnestimatedReads: u.UnestimatedReads, Activity: []SummaryActivity{}, ComputedAt: time.Now().UTC()}
	activity := make(map[string]SummaryActivity, len(u.Activity))
	for day, row := range u.Activity {
		activity[day] = *row
	}
	for _, write := range u.Writes {
		row := activity[write.Date]
		row.Date = write.Date
		summary.Writes++
		row.Writes++
		incrementActivityKind(&row, write.Kind)
		activity[write.Date] = row
		switch write.Kind {
		case WriterHuman:
			summary.HumanWrites++
		case WriterAgent:
			summary.AgentWrites++
		default:
			summary.UnknownWrites++
		}
	}
	keys := make([]string, 0, len(activity))
	for day := range activity {
		keys = append(keys, day)
	}
	sort.Strings(keys)
	for _, day := range keys {
		summary.Activity = append(summary.Activity, activity[day])
	}
	if summary.UnestimatedReads > 0 {
		summary.Note = fmt.Sprintf("Token estimate covers %d of %d retained read ranges; %d legacy or invalid ranges without usable recorded character counts are omitted.", summary.EstimatedReads, summary.EstimatedReads+summary.UnestimatedReads, summary.UnestimatedReads)
	}
	return summary
}

func summaryWriteKey(event Event) string {
	commit, commitOK := event.Fields["commit_id"].(string)
	path, pathOK := event.Fields["path"].(string)
	if !commitOK || !pathOK || commit == "" || path == "" {
		return "event\x00" + event.ID
	}
	return commit + "\x00" + path
}

func numericField(fields map[string]any, name string) (float64, bool) {
	if fields == nil {
		return 0, false
	}
	value, ok := fields[name]
	if !ok {
		return 0, false
	}
	switch number := value.(type) {
	case int:
		if number < 0 {
			return 0, false
		}
		return float64(number), true
	case int64:
		if number < 0 {
			return 0, false
		}
		return float64(number), true
	case float64:
		if number < 0 || math.IsNaN(number) || math.IsInf(number, 0) || number >= math.Exp2(63) {
			return 0, false
		}
		return number, true
	default:
		return 0, false
	}
}

func eventKind(event Event) Writer {
	kind, _ := event.Fields["writer"].(string)
	if kind == "" {
		kind, _ = event.Fields["actor_kind"].(string)
	}
	switch Writer(kind) {
	case WriterHuman, WriterAgent:
		return Writer(kind)
	default:
		return WriterUnknown
	}
}

func incrementActivityKind(row *SummaryActivity, kind Writer) {
	switch kind {
	case WriterHuman:
		row.Human++
	case WriterAgent:
		row.Agent++
	default:
		row.Unknown++
	}
}
