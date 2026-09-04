package size

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"strings"
	"time"

	"github.com/aakarim/go-openlore/pkg/packagestate"
	"github.com/aakarim/go-openlore/pkg/rules"
	"github.com/aakarim/go-openlore/pkg/rules/tokenizer"
)

type Baseline struct {
	Path      string    `json:"path,omitempty"`
	At        time.Time `json:"ts"`
	Op        string    `json:"op"`
	Reason    string    `json:"reason,omitempty"`
	Actor     string    `json:"actor,omitempty"`
	Note      string    `json:"note,omitempty"`
	Commit    string    `json:"commit,omitempty"`
	Kilobytes int       `json:"kilobytes"`
	Lines     int       `json:"lines"`
	Tokens    int       `json:"tokens"`
	Tokenizer string    `json:"tokenizer,omitempty"`
}

type BaselineStore interface {
	Get(context.Context, string) (Baseline, bool, error)
	History(context.Context, string) ([]Baseline, error)
	Record(context.Context, Baseline) error
	Clear(context.Context, string, string) error
	Move(context.Context, string, string) error
}

type baselineStore struct{ state packagestate.Store }

func NewBaselineStore(state packagestate.Store) BaselineStore { return &baselineStore{state: state} }
func stateKey(p string) string                                { return path.Base(p) + ".jsonl" }

func (s *baselineStore) History(ctx context.Context, p string) ([]Baseline, error) {
	d, err := s.state.Dir(ctx, path.Dir(p))
	if err != nil {
		return nil, err
	}
	var out []Baseline
	for raw, recordErr := range d.Records(stateKey(p)) {
		if recordErr != nil {
			return nil, recordErr
		}
		var record Baseline
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, nil
}
func (s *baselineStore) Get(ctx context.Context, p string) (Baseline, bool, error) {
	history, err := s.History(ctx, p)
	if err != nil {
		return Baseline{}, false, err
	}
	var current Baseline
	found := false
	for _, record := range history {
		if record.Op == "remove" {
			found = false
		} else if record.Op == "baseline" {
			current, found = record, true
		}
	}
	return current, found, nil
}
func (s *baselineStore) Record(ctx context.Context, b Baseline) error {
	if b.At.IsZero() {
		b.At = time.Now().UTC()
	}
	if b.Op == "" {
		b.Op = "baseline"
	}
	d, err := s.state.Dir(ctx, path.Dir(b.Path))
	if err != nil {
		return err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return d.Append(stateKey(b.Path), raw)
}
func (s *baselineStore) Clear(ctx context.Context, p, actor string) error {
	return s.Record(ctx, Baseline{Path: p, Op: "remove", Actor: actor})
}
func (s *baselineStore) Move(ctx context.Context, from, to string) error {
	src, err := s.state.Dir(ctx, path.Dir(from))
	if err != nil {
		return err
	}
	dst, err := s.state.Dir(ctx, path.Dir(to))
	if err != nil {
		return err
	}
	var records [][]byte
	for raw, recordErr := range src.Records(stateKey(from)) {
		if recordErr != nil {
			return recordErr
		}
		var record Baseline
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		record.Path = to
		rewritten, err := json.Marshal(record)
		if err != nil {
			return err
		}
		records = append(records, rewritten)
	}
	if len(records) == 0 {
		return nil
	}
	if err := dst.Replace(stateKey(to), records); err != nil {
		return err
	}
	return src.Remove(stateKey(from))
}

func Measure(content []byte, tok tokenizer.Tokenizer) (Baseline, error) {
	b := Baseline{Kilobytes: (len(content) + 1023) / 1024}
	if len(content) != 0 {
		b.Lines = strings.Count(string(content), "\n")
		if content[len(content)-1] != '\n' {
			b.Lines++
		}
	}
	if tok != nil {
		count, err := tok.Count(content)
		if err != nil {
			return Baseline{}, err
		}
		b.Tokens, b.Tokenizer = count, tok.Name()
	}
	return b, nil
}
func Reset(ctx context.Context, store BaselineStore, p string, content []byte, tok tokenizer.Tokenizer, actor, note string) (Baseline, Baseline, error) {
	previous, _, err := store.Get(ctx, p)
	if err != nil {
		return Baseline{}, Baseline{}, err
	}
	next, err := Measure(content, tok)
	if err != nil {
		return Baseline{}, Baseline{}, err
	}
	next.Path, next.Op, next.Reason, next.Actor, next.Note = p, "baseline", "reset", actor, note
	next.At = time.Now().UTC()
	if err := store.Record(ctx, next); err != nil {
		return Baseline{}, Baseline{}, err
	}
	return previous, next, nil
}

type Metric int

const (
	Kilobytes Metric = iota
	Lines
	Tokens
)

type Rule struct{ Metric Metric }

func Members() []rules.Member { return []rules.Member{Rule{Kilobytes}, Rule{Lines}, Rule{Tokens}} }
func Register(reg *rules.Registry) {
	for _, member := range Members() {
		reg.Register(member)
	}
}
func init() { Register(rules.DefaultRegistry()) }

func (r Rule) Manifest() rules.Manifest {
	member, summary, doc := "size/kilobytes", "Reject writes larger than max kibibytes", "Reject writes whose size exceeds max KiB."
	if r.Metric == Lines {
		member, summary, doc = "size/lines", "Reject writes with more than max lines", "Reject writes whose line count exceeds max."
	}
	if r.Metric == Tokens {
		member, summary, doc = "size/tokens", "Reject writes with more than max tokens", "Reject writes whose estimated token count exceeds max. Tokens use estimate/v1: ceil(bytes / 4)."
	}
	return rules.Manifest{Path: member, Kind: rules.KindRule, Scope: rules.ScopeFile, Summary: summary, Doc: doc,
		Params:  []rules.Param{{Name: "max", Type: rules.ParamIntegerOrInitial, Required: true, Doc: `Fixed cap, or baseline × growth.`}, {Name: "growth", Type: rules.ParamNumber, Doc: `Multiplier when max is "initial".`}},
		Example: "use: " + member + "\nwith: { max: initial, growth: 1.1 }"}
}
func (r Rule) Compile(with map[string]any, env rules.Env) (rules.Check, error) {
	if value, ok := with["max"].(string); ok && value == "initial" {
		growth := env.Defaults.Growth
		if growth == 0 {
			growth = 1.25
		}
		if raw, exists := with["growth"]; exists {
			var ok bool
			growth, ok = number(raw)
			if !ok {
				return nil, fmt.Errorf("growth must be a number >= 1")
			}
		}
		if growth < 1 {
			return nil, fmt.Errorf("growth must be a number >= 1")
		}
		if env.State == nil {
			return nil, fmt.Errorf("max initial requires package state")
		}
		return &check{metric: r.Metric, member: r.Manifest().Path, initial: true, growth: growth, store: NewBaselineStore(env.State), tok: env.Tokenizer}, nil
	}
	max, ok := integer(with["max"])
	if !ok || max < 1 {
		return nil, fmt.Errorf("max must be an integer >= 1")
	}
	if _, exists := with["growth"]; exists {
		return nil, fmt.Errorf("growth is only meaningful with max: initial")
	}
	return &check{metric: r.Metric, member: r.Manifest().Path, max: max, tok: env.Tokenizer}, nil
}

type check struct {
	metric  Metric
	member  string
	max     int
	initial bool
	growth  float64
	store   BaselineStore
	tok     tokenizer.Tokenizer
}

// Inspect exposes the stateful configuration to the size administration command.
func Inspect(value rules.Check) (BaselineStore, Metric, float64, tokenizer.Tokenizer, bool) {
	c, ok := value.(*check)
	if !ok || !c.initial {
		return nil, 0, 0, nil, false
	}
	return c.store, c.metric, c.growth, c.tokenizer(), true
}

func (c *check) Evaluate(ctx context.Context, subject rules.Subject) ([]rules.Finding, error) {
	measured, err := Measure(subject.Content, c.tokenizer())
	if err != nil {
		return nil, err
	}
	value, unit := c.value(measured)
	limit, provenance := c.max, fmt.Sprintf("max: %d", c.max)
	if c.initial {
		baseline, ok, err := c.baseline(ctx, subject.Path)
		if err != nil {
			return nil, err
		}
		tokenizerChanged := c.metric == Tokens && ok && baseline.Tokenizer != c.tokenizer().Name()
		if tokenizerChanged {
			ok = false
		}
		var existingContent []byte
		existed := false
		if subject.Mode == rules.ModeAdmit && subject.Existing != nil {
			existingContent, existed, err = subject.Existing()
			if err != nil {
				return nil, err
			}
			if !existed {
				ok = false
			}
		}
		if !ok {
			baseContent := subject.Content
			if subject.Mode == rules.ModeValidate {
				return nil, nil
			}
			if len(existingContent) != 0 || existed {
				baseContent = existingContent
			}
			baseline, err = Measure(baseContent, c.tokenizer())
			if err != nil {
				return nil, err
			}
			baseline.Path, baseline.Reason, baseline.Actor = subject.Path, "create", subject.Actor
			if existed {
				baseline.Reason = "rule-added"
			}
			if tokenizerChanged && existed {
				baseline.Reason = "tokenizer-changed"
			}
			baseline.At = time.Now().UTC()
			if err = c.store.Record(ctx, baseline); err != nil {
				return nil, err
			}
			if !existed {
				return nil, nil
			}
		}
		base, _ := c.value(baseline)
		limit = int(math.Floor(float64(base) * c.growth))
		provenance = fmt.Sprintf("baseline %d %s × growth %g, set %s on %s", base, unit, c.growth, baseline.At.Format("2006-01-02"), baseline.Reason)
	}
	if value <= limit {
		return nil, nil
	}
	base := path.Base(subject.Path)
	ext := path.Ext(base)
	sibling := strings.TrimSuffix(base, ext) + "-details" + ext
	override := ""
	if c.initial {
		override = fmt.Sprintf("a role in config.edit can run `lore size baseline reset %s`", subject.Path)
	}
	return []rules.Finding{{Code: c.member, Measured: fmt.Sprintf("%d %s exceeds the limit of %d (%s)", value, unit, limit, provenance), Limit: fmt.Sprintf("this file cannot grow past %d %s under this rule", limit, unit), Remedy: fmt.Sprintf("keep %s under %d %s; move the new material into a sibling file such as %s and add a link to it from %s so readers can drill in", base, limit, unit, sibling, base), Override: override}}, nil
}

func (c *check) baseline(ctx context.Context, p string) (Baseline, bool, error) {
	history, err := c.store.History(ctx, p)
	if err != nil {
		return Baseline{}, false, err
	}
	var current Baseline
	found := false
	for _, record := range history {
		if record.Op == "remove" {
			found = false
			continue
		}
		if record.Op != "baseline" {
			continue
		}
		if c.metric != Tokens && record.Reason == "tokenizer-changed" {
			continue
		}
		current, found = record, true
	}
	return current, found, nil
}
func (c *check) OnRemove(ctx context.Context, p, actor string) error {
	if !c.initial {
		return nil
	}
	return c.store.Clear(ctx, p, actor)
}
func (c *check) OnMove(ctx context.Context, from, to string) error {
	if !c.initial {
		return nil
	}
	return c.store.Move(ctx, from, to)
}
func (c *check) tokenizer() tokenizer.Tokenizer {
	if c.tok != nil {
		return c.tok
	}
	return tokenizer.Estimator{}
}
func (c *check) value(b Baseline) (int, string) {
	if c.metric == Kilobytes {
		return b.Kilobytes, "KiB"
	}
	if c.metric == Lines {
		return b.Lines, "lines"
	}
	return b.Tokens, "tokens"
}
func integer(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), n == float64(int(n))
	}
	return 0, false
}
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}
