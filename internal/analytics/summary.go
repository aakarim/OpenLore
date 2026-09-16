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
	if source == nil {
		return Summary{}, fmt.Errorf("analytics event source is unavailable")
	}
	if charsPerToken <= 0 {
		return Summary{}, fmt.Errorf("characters per token must be positive")
	}
	var events []Event
	err := source.Scan(ctx, EventFilter{From: p.Since, To: p.Until}, func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		return Summary{}, err
	}

	summary := Summary{Activity: []SummaryActivity{}, ComputedAt: time.Now().UTC()}
	activity := map[string]*SummaryActivity{}
	activityFor := func(event Event) *SummaryActivity {
		day := event.Time.UTC().Format("2006-01-02")
		row := activity[day]
		if row == nil {
			row = &SummaryActivity{Date: day}
			activity[day] = row
		}
		return row
	}
	writes := map[string]Event{}
	for _, event := range events {
		if event.Type != "doc.scalars" {
			continue
		}
		writes[summaryWriteKey(event)] = event
	}
	for _, event := range events {
		if event.Type == "doc.write" {
			writes[summaryWriteKey(event)] = event
		}
	}
	for _, event := range events {
		switch event.Type {
		case "doc.read", "doc.hit":
			row := activityFor(event)
			if event.Type == "doc.read" {
				summary.Reads++
			} else {
				summary.Hits++
			}
			row.Reads++
			incrementActivityKind(row, eventKind(event))
			estimated := false
			if characters, ok := numericField(event.Fields, "characters"); ok {
				estimate := math.Ceil(characters / float64(charsPerToken))
				if estimate < math.Exp2(63) {
					tokens := int64(estimate)
					if tokens <= math.MaxInt64-summary.EstimatedTokens {
						summary.EstimatedReads++
						summary.EstimatedTokens += tokens
						estimated = true
					}
				}
			}
			if !estimated {
				summary.UnestimatedReads++
			}
		case "command.exec":
			row := activityFor(event)
			summary.Commands++
			incrementActivityKind(row, eventKind(event))
		}
	}
	for _, event := range writes {
		row := activityFor(event)
		summary.Writes++
		row.Writes++
		kind := eventKind(event)
		incrementActivityKind(row, kind)
		switch kind {
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
		summary.Activity = append(summary.Activity, *activity[day])
	}
	if summary.UnestimatedReads > 0 {
		summary.Note = fmt.Sprintf("Token estimate covers %d of %d retained read ranges; %d legacy or invalid ranges without usable recorded character counts are omitted.", summary.EstimatedReads, summary.EstimatedReads+summary.UnestimatedReads, summary.UnestimatedReads)
	}
	return summary, nil
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
