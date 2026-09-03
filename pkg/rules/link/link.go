package link

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/aakarim/go-openlore/pkg/okf"
	"github.com/aakarim/go-openlore/pkg/rules"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type Member struct {
	Alias bool
}

func init() {
	rules.Register(Member{})
	rules.Register(Member{Alias: true})
}

func (m Member) Manifest() rules.Manifest {
	if m.Alias {
		return rules.Manifest{Path: "link/alias", Kind: rules.KindRule, Scope: rules.ScopeBundle, Summary: "Warn on links involving aliased docset paths"}
	}
	return rules.Manifest{Path: "link/resolves", Kind: rules.KindRule, Scope: rules.ScopeBundle, Summary: "Require local links to resolve inside the bundle"}
}
func (m Member) Compile(_ map[string]any, env rules.Env) (rules.Check, error) {
	roots := append([]string(nil), env.AliasRoots...)
	sort.Slice(roots, func(i, j int) bool { return len(roots[i]) > len(roots[j]) })
	return check{alias: m.Alias, aliasRoots: roots}, nil
}

type check struct {
	alias      bool
	aliasRoots []string
}

func (c check) Evaluate(_ context.Context, subject rules.Subject) ([]rules.Finding, error) {
	if subject.Bundle == nil {
		return nil, nil
	}
	var findings []rules.Finding
	for _, file := range subject.Bundle.Files {
		if path.Ext(file.Path) != ".md" {
			continue
		}
		for _, markdownLink := range okf.Links(file.Content) {
			local, ok := okf.LocalLinkPath(markdownLink.Destination)
			if !ok {
				continue
			}
			target := path.Join(path.Dir(file.AbsolutePath), local)
			if strings.HasPrefix(local, "/") {
				target = path.Join(subject.Bundle.Root, strings.TrimPrefix(local, "/"))
			}
			target = vfs.CleanPath(target)
			if c.alias {
				if aliasRoot(file.AbsolutePath, c.aliasRoots) != "" {
					findings = append(findings, finding(file.Path, markdownLink, "openlore/alias-referrer", "link originates from an aliased docset path; use a stable checkout path"))
				}
				if alias := aliasRoot(target, c.aliasRoots); alias != "" {
					findings = append(findings, finding(file.Path, markdownLink, "openlore/alias-target", fmt.Sprintf("link targets aliased docset path %s; it may resolve differently on another machine", alias)))
				}
				continue
			}
			if !within(subject.Bundle.Root, target) {
				findings = append(findings, finding(file.Path, markdownLink, "openlore/link-outside-bundle", fmt.Sprintf("local link %q resolves outside the bundle", markdownLink.Destination)))
			} else if _, err := subject.Bundle.FS.Stat(target); err != nil {
				findings = append(findings, finding(file.Path, markdownLink, "openlore/broken-link", fmt.Sprintf("local link %q does not resolve", markdownLink.Destination)))
			}
		}
	}
	return findings, nil
}
func (check) OnRemove(context.Context, string) error       { return nil }
func (check) OnMove(context.Context, string, string) error { return nil }

func finding(file string, l okf.Link, code, message string) rules.Finding {
	return rules.Finding{Code: code, Path: file, Line: l.Line, Column: l.Column, Measured: message}
}
func within(root, target string) bool {
	root, target = vfs.CleanPath(root), vfs.CleanPath(target)
	return root == "/" || target == root || strings.HasPrefix(target, root+"/")
}
func aliasRoot(target string, roots []string) string {
	for _, root := range roots {
		if within(root, target) {
			return root
		}
	}
	return ""
}
