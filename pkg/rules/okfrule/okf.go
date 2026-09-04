package okfrule

import (
	"context"
	"path"

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
		return rules.Manifest{Path: "okf/bundle", Kind: rules.KindRule, Scope: rules.ScopeBundle, Summary: "Root index.md/log.md structure, okf_version, family checks", Doc: "Validate root index.md and log.md structure, okf_version, and OKF family fields."}
	}
	return rules.Manifest{Path: "okf", Kind: rules.KindRule, Scope: rules.ScopeFile, Summary: "OKF concept conformance of one file", Doc: "Validate one file for OKF concept conformance."}
}

func (m Member) Compile(_ map[string]any, _ rules.Env) (rules.Check, error) {
	return check{bundle: m.Bundle}, nil
}

type check struct{ bundle bool }

func (c check) Evaluate(_ context.Context, subject rules.Subject) ([]rules.Finding, error) {
	if !c.bundle {
		if subject.Mode == rules.ModeValidate && path.Ext(subject.Path) != ".md" {
			return nil, nil
		}
		if err := okf.Validate(subject.Path, subject.Content); err != nil {
			if subject.Mode == rules.ModeValidate && okf.IsReserved(subject.Path) {
				return nil, nil
			}
			return []rules.Finding{{Code: "okf/concept", Measured: err.Error()}}, nil
		}
		return nil, nil
	}
	if subject.Bundle == nil {
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
func (check) OnRemove(context.Context, string, string) error { return nil }
func (check) OnMove(context.Context, string, string) error   { return nil }
