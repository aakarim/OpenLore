package cmds

import (
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/aakarim/go-openlore/pkg/openlore/meta"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// cmdLoreMeta is the `lore meta` command adapter. It is shell plumbing only: it
// parses the optional path argument, calls meta.Scan over the session
// filesystem (so read-scoping is inherited) with the session's plugin-provided
// extenders, and emits each record as one JSON object per line (NDJSON) with
// `path` merged in. All the real work lives in pkg/meta.
func cmdLoreMeta(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	root := ctx.Cwd()
	filterName := ""
	pathSet := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--filter" {
			if i+1 >= len(args) {
				fmt.Fprintln(errW, "lore meta: --filter requires a name")
				return 1
			}
			i++
			filterName = args[i]
			continue
		}
		if strings.HasPrefix(a, "-") {
			fmt.Fprintf(errW, "lore meta: unknown flag %q\n", a)
			return 1
		}
		root = ctx.Resolve(a)
		pathSet = true
	}
	if filterName != "" {
		return runMetaFilter(ctx, filterName, root, pathSet, w, errW)
	}

	records, err := meta.Scan(ctx.FS(), root, ctx.MetaExtenders()...)
	if err != nil {
		fmt.Fprintf(errW, "lore meta: %s\n", err)
		return 1
	}

	enc := json.NewEncoder(w)
	for _, r := range records {
		obj := make(map[string]any, len(r.Fields)+1)
		for k, v := range r.Fields {
			obj[k] = v
		}
		obj["path"] = r.Path
		if err := enc.Encode(obj); err != nil {
			fmt.Fprintf(errW, "lore meta: %s\n", err)
			return 1
		}
	}
	return 0
}

func runMetaFilter(ctx CmdContext, name, narrow string, pathSet bool, w, errW io.Writer) int {
	var filter *meta.Filter
	for _, f := range ctx.MetaFilters() {
		if f.Name == name {
			x := f
			filter = &x
			break
		}
		for _, a := range f.Aliases {
			if a == name {
				x := f
				filter = &x
				break
			}
		}
		if filter != nil {
			break
		}
	}
	if filter == nil {
		fmt.Fprintf(errW, "lore meta: unknown filter %q\n", name)
		return 1
	}
	canonical := func(p string) string { return vfs.CleanPath(p) }
	if c, ok := ctx.FS().(vfs.PathCanonicalizer); ok {
		canonical = func(p string) string { return vfs.CleanPath(c.CanonicalPath(p)) }
	}
	if pathSet {
		narrow = canonical(narrow)
	}
	seenRoots, seenResults := map[string]bool{}, map[string]bool{}
	enc := json.NewEncoder(w)
	for _, bound := range filter.Roots {
		bound = canonical(bound)
		scan := ""
		if !pathSet {
			scan = bound
		} else if within(bound, narrow) {
			scan = vfs.CleanPath(narrow)
		} else if within(narrow, bound) {
			scan = bound
		} else {
			continue
		}
		if seenRoots[scan] {
			continue
		}
		seenRoots[scan] = true
		info, err := ctx.FS().Stat(scan)
		if err != nil {
			fmt.Fprintf(errW, "lore meta: %s\n", err)
			return 1
		}
		recs, err := meta.Scan(ctx.FS(), scan, ctx.MetaExtenders()...)
		if err != nil {
			fmt.Fprintf(errW, "lore meta: %s\n", err)
			return 1
		}
		for _, r := range recs {
			abs := canonical(path.Join(scan, r.Path))
			if !info.Dir {
				abs = canonical(scan)
			}
			if seenResults[abs] || !filter.Selector(abs, r) {
				continue
			}
			seenResults[abs] = true
			resultPath := r.Path
			if filter.AbsolutePaths {
				resultPath = abs
			}
			obj := make(map[string]any, len(r.Fields)+1)
			for k, v := range r.Fields {
				obj[k] = v
			}
			obj["path"] = resultPath
			if err := enc.Encode(obj); err != nil {
				fmt.Fprintf(errW, "lore meta: %s\n", err)
				return 1
			}
		}
	}
	return 0
}

func within(root, p string) bool {
	root, p = vfs.CleanPath(root), vfs.CleanPath(p)
	return root == "/" || p == root || strings.HasPrefix(p, root+"/")
}

func init() {
	RegisterLoreSub(LoreSub{
		Name:    "meta",
		Summary: "Emit each document's frontmatter as JSON (NDJSON), cwd-scoped",
		Run:     cmdLoreMeta,
	})
}
