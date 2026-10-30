package cmds

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	"github.com/aakarim/go-openlore/pkg/openlore/meta"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

const loreMetaHelp = `Usage:
  lore meta [path]
  lore meta [path] --missing [key1,key2,...]
  lore meta [path] --filter <name>
  lore meta set <path> key=value
  lore meta unset <path> key

Emit parseable YAML frontmatter as NDJSON, sorted by relative path.
Only .md files are scanned; path defaults to the current directory.
A scan summary goes to stderr, leaving stdout safe for pipelines.

  --missing        List files without parseable frontmatter as NDJSON.
                   Optional comma-separated top-level keys also include files
                   missing any listed key. Null/empty values count as present.
                   Output: path, reason, and missing_keys when applicable.
                   Put path before --missing (or use --missing=keys path).
  --filter <name>  Run a plugin-provided filter, preserving its path semantics.
                   Cannot be combined with --missing.
  --help, -h       Show this help.

Examples:
  lore meta
  lore meta /home/agent --missing
  lore meta --missing type,tags
  lore meta --missing=type,tags /home/agent
`

// cmdLoreMeta keeps metadata discovery on the session filesystem, so read
// scoping and plugin-provided extenders and filters are inherited.
func cmdLoreMeta(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	if len(args) > 0 && (args[0] == "set" || args[0] == "unset") {
		return cmdLoreMetaEdit(ctx, args, w, errW, stdin)
	}
	root := ctx.Cwd()
	filterName := ""
	pathSet := false
	missing := false
	var keys []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--help" || a == "-h" {
			fmt.Fprint(w, loreMetaHelp)
			return 0
		}
		if a == "--missing" || strings.HasPrefix(a, "--missing=") {
			if missing {
				fmt.Fprintln(errW, "lore meta: --missing may only be specified once")
				return 1
			}
			missing = true
			value, hasValue := strings.CutPrefix(a, "--missing=")
			if !hasValue && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				value, hasValue = args[i], true
			}
			if hasValue {
				for _, key := range strings.Split(value, ",") {
					key = strings.TrimSpace(key)
					if key == "" {
						fmt.Fprintln(errW, "lore meta: --missing requires non-empty comma-separated keys")
						return 1
					}
					keys = append(keys, key)
				}
			}
			continue
		}
		if a == "--filter" {
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
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
		if pathSet {
			fmt.Fprintln(errW, "lore meta: expected at most one path (see lore meta --help)")
			return 1
		}
		root = ctx.Resolve(a)
		pathSet = true
	}
	if filterName != "" {
		if missing {
			fmt.Fprintln(errW, "lore meta: --missing cannot be combined with --filter")
			return 1
		}
		return runMetaFilter(ctx, filterName, root, pathSet, w, errW)
	}

	report, err := meta.ScanWithReport(ctx.FS(), root, keys, ctx.MetaExtenders()...)
	if err != nil {
		fmt.Fprintf(errW, "lore meta: %s\n", err)
		return 1
	}

	enc := json.NewEncoder(w)
	if missing {
		for _, r := range report.Missing {
			if err := enc.Encode(r); err != nil {
				fmt.Fprintf(errW, "lore meta: %s\n", err)
				return 1
			}
		}
	} else {
		for _, r := range report.Records {
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
	}
	fmt.Fprintf(errW, "lore meta: %d with frontmatter, %d without parseable frontmatter; use lore meta [path] --missing to discover omitted files\n", report.WithFrontmatter, report.WithoutFrontmatter)
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
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
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
