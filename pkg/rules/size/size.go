package size

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/aakarim/go-openlore/pkg/rules"
	"github.com/aakarim/go-openlore/pkg/rules/tokenizer"
)

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
	path, summary := "size/kilobytes", "Reject writes larger than max kibibytes"
	if r.Metric == Lines {
		path, summary = "size/lines", "Reject writes with more than max lines"
	}
	if r.Metric == Tokens {
		path, summary = "size/tokens", "Reject writes with more than max estimated tokens"
	}
	return rules.Manifest{Path: path, Kind: rules.KindRule, Scope: rules.ScopeFile, Summary: summary,
		Params:  []rules.Param{{Name: "max", Type: rules.ParamIntegerOrInitial, Required: true}, {Name: "growth", Type: rules.ParamNumber}},
		Example: "use: " + path + "\nwith: { max: 60 }"}
}

func (r Rule) Compile(with map[string]any, _ rules.Env) (rules.Check, error) {
	if initial, ok := with["max"].(string); ok && initial == "initial" {
		return nil, fmt.Errorf("max: initial is not supported until folder rules phase 4")
	}
	if growth, ok := asNumber(with["growth"]); ok && growth < 1 {
		return nil, fmt.Errorf("growth must be at least 1")
	}
	max, ok := asInteger(with["max"])
	if !ok || max < 1 {
		return nil, fmt.Errorf("max must be an integer >= 1")
	}
	return check{metric: r.Metric, max: max, member: r.Manifest().Path}, nil
}

type check struct {
	metric Metric
	max    int64
	member string
}

func (c check) Evaluate(_ context.Context, subject rules.Subject) ([]rules.Finding, error) {
	measured, unit := c.measure(subject.Content)
	if measured <= c.max {
		return nil, nil
	}
	base := path.Base(subject.Path)
	ext := path.Ext(base)
	sibling := strings.TrimSuffix(base, ext) + "-details" + ext
	extra := ""
	if c.metric == Tokens {
		extra = "; estimate/v1, ≈ bytes/4"
	}
	return []rules.Finding{{Code: c.member,
		Measured: fmt.Sprintf("%d %s exceeds the limit of %d (max: %d%s)", measured, unit, c.max, c.max, extra),
		Limit:    fmt.Sprintf("this file cannot grow past %d %s under this rule", c.max, unit),
		Remedy:   fmt.Sprintf("keep %s under %d %s; move the new material into a sibling file such as %s and add a link to it from %s so readers can drill in", base, c.max, unit, sibling, base),
	}}, nil
}
func (check) OnRemove(context.Context, string) error       { return nil }
func (check) OnMove(context.Context, string, string) error { return nil }

func (c check) measure(content []byte) (int64, string) {
	switch c.metric {
	case Kilobytes:
		return int64((len(content) + 1023) / 1024), "KiB"
	case Lines:
		if len(content) == 0 {
			return 0, "lines"
		}
		n := int64(strings.Count(string(content), "\n"))
		if content[len(content)-1] != '\n' {
			n++
		}
		return n, "lines"
	default:
		return int64(tokenizer.Estimate(content)), "tokens"
	}
}

func asInteger(value any) (int64, bool) {
	switch n := value.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), n == float64(int64(n))
	default:
		return 0, false
	}
}

func asNumber(value any) (float64, bool) {
	switch n := value.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	default:
		return 0, false
	}
}
