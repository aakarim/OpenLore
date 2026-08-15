package cmds

import (
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

func CmdTree(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	root := ctx.Cwd()
	maxDepth := -1

	for i := 0; i < len(args); i++ {
		if args[i] == "-L" && i+1 < len(args) {
			fmt.Sscanf(args[i+1], "%d", &maxDepth)
			i++
		} else if !strings.HasPrefix(args[i], "-") {
			root = ctx.Resolve(args[i])
		}
	}

	fmt.Fprintln(w, root)
	printTree(ctx, w, root, "", 0, maxDepth)
	return 0
}

func printTree(ctx CmdContext, w io.Writer, dir string, prefix string, depth int, maxDepth int) {
	if maxDepth >= 0 && depth >= maxDepth {
		return
	}

	entries, err := ctx.FS().ReadDir(dir)
	if err != nil {
		return
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].FileName < entries[j].FileName })

	for i, e := range entries {
		isLast := i == len(entries)-1
		connector := "├── "
		if isLast {
			connector = "└── "
		}

		name := e.FileName
		if e.Dir {
			name += "/"
		}
		fmt.Fprintf(w, "%s%s%s\n", prefix, connector, name)

		if e.Dir {
			childPrefix := prefix + "│   "
			if isLast {
				childPrefix = prefix + "    "
			}
			childPath := path.Join(dir, e.FileName)
			printTree(ctx, w, childPath, childPrefix, depth+1, maxDepth)
		}
	}
}
