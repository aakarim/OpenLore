package cmds

import (
	"fmt"
	"io"
)

func CmdLs(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	longFormat := false
	allFlag := false
	var targets []string
	for _, a := range args {
		switch a {
		case "-l":
			longFormat = true
		case "-la", "-al":
			longFormat = true
			allFlag = true
		case "-a":
			allFlag = true
		default:
			targets = append(targets, a)
		}
	}
	_ = allFlag

	if len(targets) == 0 {
		targets = []string{ctx.Cwd()}
	}

	exitCode := 0
	for _, target := range targets {
		p := ctx.Resolve(target)
		entries, err := ctx.FS().ReadDir(p)
		if err != nil {
			f, ferr := ctx.FS().Stat(p)
			if ferr != nil {
				fmt.Fprintf(errW, "ls: %s: No such file or directory\n", target)
				exitCode = 1
				continue
			}
			if longFormat {
				PrintLong(w, f)
			} else {
				fmt.Fprintln(w, f.Name())
			}
			continue
		}

		if len(targets) > 1 {
			fmt.Fprintf(w, "%s:\n", target)
		}
		for _, e := range entries {
			ei := e
			if longFormat {
				PrintLong(w, &ei)
			} else {
				name := e.FileName
				if e.Dir {
					name += "/"
				}
				fmt.Fprintln(w, name)
			}
		}
	}
	return exitCode
}
