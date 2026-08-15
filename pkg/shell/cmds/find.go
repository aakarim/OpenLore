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

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-name":
			if i+1 < len(args) {
				namePattern = args[i+1]
				i++
			}
		case "-type":
			if i+1 < len(args) {
				typeFilter = args[i+1]
				i++
			}
		default:
			if !strings.HasPrefix(args[i], "-") {
				root = ctx.Resolve(args[i])
			}
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
		return nil
	})
	if err != nil {
		fmt.Fprintf(errW, "find: %s\n", err)
		return 1
	}
	return 0
}
