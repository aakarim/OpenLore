package okfrule

import (
	"context"

	"github.com/aakarim/go-openlore/pkg/okf"
	"github.com/aakarim/go-openlore/pkg/rules"
)

type Member struct{ Bundle bool }

func init() {
	rules.Register(Member{})
	rules.Register(Member{Bundle: true})
}

func (m Member) Manifest() rules.Manifest {
	if m.Bundle {
		return rules.Manifest{Path: "okf/bundle", Kind: rules.KindRule, Scope: rules.ScopeBundle, Summary: "Validate OKF bundle structure and families"}
	}
	return rules.Manifest{Path: "okf", Kind: rules.KindRule, Scope: rules.ScopeFile, Summary: "Validate OKF concept conformance"}
}

func (m Member) Compile(_ map[string]any, _ rules.Env) (rules.Check, error) {
	return check{bundle: m.Bundle}, nil
}

type check struct{ bundle bool }

func (c check) Evaluate(_ context.Context, subject rules.Subject) ([]rules.Finding, error) {
	if !c.bundle {
		if err := okf.Validate(subject.Path, subject.Content); err != nil {
			if subject.Mode == rules.ModeValidate {
				if okf.IsReserved(subject.Path) {
					return nil, nil
				}
				return []rules.Finding{{Code: "okf/concept", Measured: err.Error()}}, nil
			}
			return nil, err
		}
		return nil, nil
	}
	if subject.Bundle == nil || len(subject.Bundle.Files) == 0 || subject.Path != subject.Bundle.Files[0].AbsolutePath {
		return nil, nil
	}
	files := make([]okf.File, 0, len(subject.Bundle.Files))
	for _, file := range subject.Bundle.Files {
		files = append(files, okf.File{Path: file.Path, Content: file.Content})
	}
	var findings []rules.Finding
	for _, d := range okf.ValidateBundle(files) {
		if d.Rule == "okf/concept" {
			continue
		}
		findings = append(findings, rules.Finding{Code: d.Rule, Path: d.Path, Line: d.Line, Column: d.Column, Warning: d.Severity == okf.SeverityWarning, Measured: d.Message})
	}
	return findings, nil
}
func (check) OnRemove(context.Context, string) error       { return nil }
func (check) OnMove(context.Context, string, string) error { return nil }
