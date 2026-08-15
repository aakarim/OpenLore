package cmds

import (
	"errors"
	"fmt"
	"io"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

// CmdMkdir creates directories. With -p it also creates missing parents and does
// not treat an existing directory as an error. A folder can only be created
// strictly inside a docset — you cannot create a docset (or its root) this way.
//
// Usage: mkdir [-p] <dir>...
func CmdMkdir(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	parents := false
	var targets []string
	flagsDone := false
	for _, a := range args {
		if !flagsDone && len(a) > 1 && a[0] == '-' {
			switch a {
			case "--":
				flagsDone = true
			case "-p", "--parents":
				parents = true
			default:
				ReportUnsupportedFlag(ctx, "mkdir", a)
				fmt.Fprintf(errW, "mkdir: unknown option %q\n", a)
				return 1
			}
			continue
		}
		targets = append(targets, a)
	}
	if len(targets) == 0 {
		fmt.Fprintln(errW, "mkdir: missing operand")
		return 1
	}
	wfs, ok := ctx.FS().(vfs.WritableFS)
	if !ok {
		fmt.Fprintln(errW, "mkdir: read-only filesystem")
		return 1
	}

	exit := 0
	for _, t := range targets {
		resolved := ctx.Resolve(t)
		var err error
		if parents {
			err = wfs.MkdirAll(resolved)
		} else {
			err = wfs.Mkdir(resolved)
		}
		if err != nil {
			if errors.Is(err, vfs.ErrReadOnly) {
				fmt.Fprintf(errW, "mkdir: %s: read-only filesystem\n", t)
			} else {
				fmt.Fprintf(errW, "mkdir: %s: %s\n", t, err)
			}
			exit = 1
		}
	}
	return exit
}
