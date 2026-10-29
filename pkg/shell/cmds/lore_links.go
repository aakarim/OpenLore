package cmds

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/aakarim/go-openlore/pkg/okf"
	"github.com/aakarim/go-openlore/pkg/vfs"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

func cmdLoreLinks(ctx CmdContext, args []string, w, errW io.Writer, stdin io.Reader) int {
	if len(args) == 1 && args[0] == "--help" {
		fmt.Fprintln(w, "usage: lore links --to <path> (Markdown links and code-span paths in readable docsets)")
		return 0
	}
	if len(args) != 2 || args[0] != "--to" {
		fmt.Fprintln(errW, "usage: lore links --to <path>")
		return 1
	}
	paths, err := referencingPaths(ctx, ctx.Resolve(args[1]))
	if err != nil {
		fmt.Fprintf(errW, "lore links: %v\n", err)
		return 1
	}
	for _, p := range paths {
		if _, err := fmt.Fprintln(w, p); err != nil {
			fmt.Fprintf(errW, "lore links: %v\n", err)
			return 1
		}
	}
	return 0
}

// Scan only the caller's content view, not the durable journal or alias copies.
// Targets need not exist: this is also how agents find references after a move.
func referencingPaths(ctx CmdContext, target string) ([]string, error) {
	canonical := func(p string) string { return vfs.CleanPath(p) }
	if c, ok := ctx.FS().(vfs.PathCanonicalizer); ok {
		canonical = func(p string) string { return vfs.CleanPath(c.CanonicalPath(p)) }
	}
	target = canonical(target)
	roots := []string{}
	for _, docset := range ctx.Docsets() {
		if docset.AliasTarget == "" {
			roots = append(roots, docset.Paths...)
		}
	}
	if len(roots) == 0 {
		roots = []string{"/"}
	}
	seen := map[string]bool{}
	var result []string
	var walk func(string) error
	walk = func(p string) error {
		p = canonical(p)
		if within("/data/history", p) || seen[p] {
			return nil
		}
		seen[p] = true
		info, err := ctx.FS().Stat(p)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Dir {
			entries, err := ctx.FS().ReadDir(p)
			if errors.Is(err, fs.ErrPermission) {
				return nil
			}
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if err := walk(path.Join(p, entry.FileName)); err != nil {
					return err
				}
			}
			return nil
		}
		if !strings.EqualFold(path.Ext(p), ".md") {
			return nil
		}
		content, err := ctx.FS().ReadFile(p)
		if errors.Is(err, fs.ErrPermission) {
			return nil
		}
		if err != nil {
			return err
		}
		destinations := []string{}
		for _, link := range okf.Links(content) {
			destinations = append(destinations, link.Destination)
		}
		_, body, hasFM, fmErr := okf.ParseFrontmatter(content)
		if !hasFM || fmErr != nil {
			body = content
		}
		doc := goldmark.DefaultParser().Parse(text.NewReader(body))
		_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
			if span, ok := node.(*ast.CodeSpan); entering && ok {
				destinations = append(destinations, string(span.Text(body)))
			}
			return ast.WalkContinue, nil
		})
		for _, destination := range destinations {
			local, ok := okf.LocalLinkPath(destination)
			if !ok {
				continue
			}
			resolved := local
			if !strings.HasPrefix(local, "/") {
				resolved = path.Join(path.Dir(p), local)
			}
			if canonical(resolved) == target {
				result = append(result, p)
				break
			}
		}
		return nil
	}
	for _, root := range roots {
		if err := walk(root); err != nil {
			return nil, err
		}
	}
	sort.Strings(result)
	return result, nil
}

func warnMovedReferences(ctx CmdContext, source string, errW io.Writer) {
	paths, err := referencingPaths(ctx, source)
	if err != nil {
		fmt.Fprintf(errW, "mv: move completed; reference check failed: %v\n", err)
		return
	}
	if len(paths) != 0 {
		fmt.Fprintf(errW, "mv: move completed; update references to %s in:\n", source)
		for _, p := range paths {
			fmt.Fprintln(errW, p)
		}
	}
}

func init() {
	RegisterLoreSub(LoreSub{Name: "links", Summary: "List canonical files referencing a path", Run: cmdLoreLinks})
}
