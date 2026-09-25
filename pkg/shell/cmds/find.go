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
	matchedResults := 0
	matchedFiles := 0

	for i := 0; i < len(args); i++ {
		switch args[i] {
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
			if typeFilter != "f" && typeFilter != "d" {
				fmt.Fprintf(errW, "find: unsupported type '%s'. This shell supports -type f|d.\n", typeFilter)
				return 1
			}
			i++
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(errW, "find: unsupported flag '%s'. This shell supports [path] [-name pat] [-type f|d]. To limit depth, use 'tree -L <n>'.\n", args[i])
				return 1
			}
			root = ctx.Resolve(args[i])
		}
	}

	err := vfs.WalkDir(ctx.FS(), root, func(p string, info *vfs.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if typeFilter == "f" && info.Dir {
			return nil
		}
		if typeFilter == "d" && !info.Dir {
			return nil
		}
		if namePattern != "" {
			matched, _ := path.Match(namePattern, info.FileName)
			if !matched {
				return nil
			}
		}
		fmt.Fprintln(w, p)
		matchedResults++
		if !info.Dir {
			matchedFiles++
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(errW, "find: %s\n", err)
		return 1
	}
	if namePattern != "" {
		emitSearchMetric(ctx, namePattern, []string{root}, matchedFiles, 0, matchedResults > 0)
	}
	return 0
}
