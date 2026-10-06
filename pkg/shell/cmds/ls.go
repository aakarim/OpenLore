package cmds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
)

func CmdLs(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	factsProvider := analyticsFacts(ctx)
	longFormat := false
	allFlag := false
	statsFlag := false
	jsonFlag := false
	var targets []string
	for _, a := range args {
		switch {
		case a == "--stats":
			statsFlag = true
		case a == "--json":
			jsonFlag = true
		case len(a) > 1 && a[0] == '-' && a[1] != '-':
			// Combined short flags: -l, -a, -la, -l1, ...
			for _, r := range a[1:] {
				switch r {
				case 'l':
					longFormat = true
				case 'a':
					allFlag = true
				case '1':
					// Output is already one entry per line.
				default:
					fmt.Fprintf(errW, "ls: invalid option -- '%c'\nusage: ls [-l|-a|-1|--stats|--json] [path...]\n", r)
					return 1
				}
			}
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
				if statsFlag && factsProvider != nil {
					facts, factErr := factsProvider.Stat(context.Background(), p)
					if factErr != nil {
						fmt.Fprintf(errW, "ls: %s: %s\n", target, factErr)
						exitCode = 1
						continue
					}
					fmt.Fprintf(w, "%s %8d %8.0f %8.0f %s\n", f.Mode(), f.Size(), facts.Scalars["lines"], facts.Scalars["tokens"], f.Name())
				} else {
					PrintLong(w, f)
				}
			} else if jsonFlag && factsProvider != nil {
				facts, factErr := factsProvider.Stat(context.Background(), p)
				if factErr != nil {
					fmt.Fprintf(errW, "ls: %s: %s\n", target, factErr)
					exitCode = 1
					continue
				}
				_ = json.NewEncoder(w).Encode(facts)
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
			child := path.Join(p, e.FileName)
			if jsonFlag && factsProvider != nil {
				facts, factErr := factsProvider.Stat(context.Background(), child)
				if factErr != nil {
					fmt.Fprintf(errW, "ls: %s: %s\n", child, factErr)
					exitCode = 1
					continue
				}
				_ = json.NewEncoder(w).Encode(facts)
			} else if longFormat {
				if statsFlag && factsProvider != nil {
					facts, factErr := factsProvider.Stat(context.Background(), child)
					if factErr != nil {
						fmt.Fprintf(errW, "ls: %s: %s\n", child, factErr)
						exitCode = 1
						continue
					}
					fmt.Fprintf(w, "%s %8d %8.0f %8.0f %s\n", ei.Mode(), ei.FileSize, facts.Scalars["lines"], facts.Scalars["tokens"], ei.Name())
				} else {
					PrintLong(w, &ei)
				}
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
