package cmds

import (
	"fmt"
	"io"
	"strings"
)

func CmdUniq(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	count := false
	dupsOnly := false
	caseI := false
	uniqOnly := false

	var files []string
	for _, a := range args {
		switch a {
		case "-c":
			count = true
		case "-d":
			dupsOnly = true
		case "-i":
			caseI = true
		case "-u":
			uniqOnly = true
		default:
			if !strings.HasPrefix(a, "-") {
				files = append(files, a)
			}
		}
	}

	lines, code := ReadInputLines(ctx, files, stdin, errW, "uniq")
	if code != 0 {
		return code
	}

	type group struct {
		line  string
		count int
	}

	var groups []group
	for _, l := range lines {
		cmp := l
		if caseI {
			cmp = strings.ToLower(l)
		}
		if len(groups) > 0 {
			prev := groups[len(groups)-1].line
			prevCmp := prev
			if caseI {
				prevCmp = strings.ToLower(prev)
			}
			if cmp == prevCmp {
				groups[len(groups)-1].count++
				continue
			}
		}
		groups = append(groups, group{line: l, count: 1})
	}

	for _, g := range groups {
		if dupsOnly && g.count < 2 {
			continue
		}
		if uniqOnly && g.count > 1 {
			continue
		}
		if count {
			fmt.Fprintf(w, "%7d %s\n", g.count, g.line)
		} else {
			fmt.Fprintln(w, g.line)
		}
	}
	return 0
}
