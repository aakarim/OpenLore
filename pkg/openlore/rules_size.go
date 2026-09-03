package openlore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/aakarim/go-openlore/pkg/packagestate"
	"github.com/aakarim/go-openlore/pkg/rules"
	"github.com/aakarim/go-openlore/pkg/rules/size"
	"github.com/aakarim/go-openlore/pkg/rules/tokenizer"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type sizeRuleState struct {
	store  size.BaselineStore
	metric size.Metric
	growth float64
	rule   rules.CompiledRule
}

func (p *rulesPlugin) sizeRules(ctx context.Context, target string) ([]sizeRuleState, error) {
	compiled, err := p.engine.Effective(ctx, target)
	if err != nil {
		return nil, err
	}
	var out []sizeRuleState
	for _, rule := range compiled {
		store, metric, growth, ok := size.Inspect(rule.Check)
		if ok && rules.Applies(rule, target) {
			out = append(out, sizeRuleState{store, metric, growth, rule})
		}
	}
	return out, nil
}

func (p *rulesPlugin) baselineText(ctx context.Context, target string) (string, error) {
	states, err := p.sizeRules(ctx, target)
	if err != nil {
		return "", err
	}
	var store size.BaselineStore
	if len(states) == 0 {
		mapper, _ := p.fs.(packagestate.HostMapper)
		store = size.NewBaselineStore(packagestate.Open(mapper, "size"))
	} else {
		store = states[0].store
	}
	history, err := store.History(ctx, target)
	if err != nil {
		return "", err
	}
	if len(history) == 0 {
		return "no baseline recorded\n", nil
	}
	var b strings.Builder
	fmt.Fprintln(&b, target)
	for _, record := range history {
		fmt.Fprintf(&b, "  %s  %-10s  %-20s  %d lines  %d KiB  %d tokens", record.At.Format("2006-01-02T15:04:05Z"), first(record.Reason, record.Op), record.Actor, record.Lines, record.Kilobytes, record.Tokens)
		if record.Tokenizer != "" {
			fmt.Fprintf(&b, " (%s)", record.Tokenizer)
		}
		if record.Note != "" {
			fmt.Fprintf(&b, "  %q", record.Note)
		}
		fmt.Fprintln(&b)
	}
	current, ok, err := store.Get(ctx, target)
	if err != nil {
		return "", err
	}
	if ok && len(states) != 0 {
		fmt.Fprint(&b, "current cap:")
		for _, state := range states {
			value, unit := baselineMetric(current, state.metric)
			fmt.Fprintf(&b, " %d %s", int(math.Floor(float64(value)*state.growth)), unit)
		}
		fmt.Fprintln(&b)
	}
	return b.String(), nil
}

func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
func baselineMetric(b size.Baseline, m size.Metric) (int, string) {
	if m == size.Kilobytes {
		return b.Kilobytes, "KiB"
	}
	if m == size.Lines {
		return b.Lines, "lines"
	}
	return b.Tokens, "tokens"
}

type sessionSizeBackend struct {
	server   *Server
	identity Identity
}

func (b sessionSizeBackend) Baseline(target string) (string, error) {
	if b.server.authEnforced {
		allowed := false
		for _, root := range b.server.readableRoots(b.server.resolveSessionIdentity(b.identity)) {
			if pathWithinRoot(root, target) {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", errors.New("permission denied")
		}
	}
	return b.server.rules.baselineText(context.Background(), target)
}
func (b sessionSizeBackend) Reset(target, note string, a cmds.JobAttribution) (string, error) {
	id := b.server.resolveSessionIdentity(b.identity)
	if !scopeGrantsWrite(id.Scopes) || !b.server.identityHasDirConfigRole(id, target) || !b.server.identityCanWrite(id, vfs.ChangeActionWrite, target) {
		return "", errors.New("permission denied")
	}
	content, err := b.server.merge.ReadFile(target)
	if err != nil {
		return "", err
	}
	states, err := b.server.rules.sizeRules(context.Background(), target)
	if err != nil {
		return "", err
	}
	if len(states) == 0 {
		return "", fmt.Errorf("no max: initial size rule applies to %s", target)
	}
	actor := Attribution{Principal: a.Principal, Actor: a.Actor}.String()
	previous, next, err := size.Reset(context.Background(), states[0].store, target, content, tokenizer.Estimator{}, actor, note)
	if err != nil {
		return "", err
	}
	if err = b.server.audit.Record(context.Background(), AuditEvent{Type: "rules.baseline.reset", Attribution: Attribution{Principal: a.Principal, Actor: a.Actor}, Details: map[string]any{"path": target, "previous": previous, "next": next, "note": note}}); err != nil {
		return "", err
	}
	value, unit := baselineMetric(next, states[0].metric)
	cap := int(math.Floor(float64(value) * states[0].growth))
	return fmt.Sprintf("baseline reset: previous %d %s, new %d %s, new cap %d %s\n", baselineValue(previous, states[0].metric), unit, value, unit, cap, unit), nil
}
func baselineValue(b size.Baseline, m size.Metric) int { v, _ := baselineMetric(b, m); return v }
