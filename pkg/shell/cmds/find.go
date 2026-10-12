package cmds

import (
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

func CmdFind(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	root := ctx.Cwd()
	var namePattern string
	var typeFilter string
	followAliases := false
	matchedResults := 0
	matchedFiles := 0

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-L":
			followAliases = true
		case "-name":
			if i+1 >= len(args) {
				fmt.Fprintln(errW, "find: option requires an argument -- 'name'")
				return 1
			}
			namePattern = args[i+1]
			i++
		case "-type":
			if i+1 >= len(args) {
				fmt.Fprintln(errW, "find: option requires an argument -- 'type'")
				return 1
			}
			typeFilter = args[i+1]
			if typeFilter != "f" && typeFilter != "d" && typeFilter != "l" {
				fmt.Fprintf(errW, "find: unsupported type '%s'. This shell supports -type f|d|l.\n", typeFilter)
				return 1
			}
			i++
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(errW, "find: unsupported flag '%s'. This shell supports [-L] [path] [-name pat] [-type f|d|l]. To limit depth, use 'tree -L <n>'.\n", args[i])
				return 1
			}
			root = ctx.Resolve(args[i])
		}
	}

	root = vfs.CleanPath(root)
	var walk func(p string, info *vfs.FileInfo)
	walk = func(p string, info *vfs.FileInfo) {
		// Like find -P, an alias root below the starting point is reported as
		// a link and not descended into, so each file is listed once. -L
		// follows aliases; a starting point is always followed, like find -H.
		link := info.LinkTarget != "" && !followAliases && p != root
		kind := "f"
		if link {
			kind = "l"
		} else if info.Dir {
			kind = "d"
		}
		matched := typeFilter == "" || typeFilter == kind
		if matched && namePattern != "" {
			matched, _ = path.Match(namePattern, info.FileName)
		}
		if matched {
			fmt.Fprintln(w, p)
			matchedResults++
			if kind == "f" {
				matchedFiles++
			}
		}
		if kind != "d" {
			return
		}
		entries, err := ctx.FS().ReadDir(p)
		if err != nil {
			return
		}
		for _, entry := range entries {
			childPath := path.Join(p, entry.FileName)
			if child, err := ctx.FS().Stat(childPath); err == nil {
				walk(childPath, child)
			}
		}
	}
	if info, err := ctx.FS().Stat(root); err == nil {
		walk(root, info)
	}
	if namePattern != "" {
		emitSearchMetric(ctx, namePattern, []string{root}, matchedFiles, 0, matchedResults > 0)
	}
	return 0
}
