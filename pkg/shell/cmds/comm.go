package cmds

import (
	"fmt"
	"io"
	"strings"
)

func CmdComm(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	suppress1 := false
	suppress2 := false
	suppress3 := false

	var files []string
	for _, a := range args {
		switch a {
		case "-1":
			suppress1 = true
		case "-2":
			suppress2 = true
		case "-3":
			suppress3 = true
		case "-12":
			suppress1 = true
			suppress2 = true
		case "-23":
			suppress2 = true
			suppress3 = true
		case "-13":
			suppress1 = true
			suppress3 = true
		default:
			if !strings.HasPrefix(a, "-") {
				files = append(files, a)
			}
		}
	}

	if len(files) < 2 {
		fmt.Fprintln(errW, "comm: missing operand")
		return 1
	}

	p1 := ctx.Resolve(files[0])
	p2 := ctx.Resolve(files[1])

	c1, err := ctx.FS().ReadFile(p1)
	if err != nil {
		fmt.Fprintf(errW, "comm: %s: %s\n", files[0], err)
		return 1
	}
	c2, err := ctx.FS().ReadFile(p2)
	if err != nil {
		fmt.Fprintf(errW, "comm: %s: %s\n", files[1], err)
		return 1
	}

	lines1 := strings.Split(strings.TrimRight(string(c1), "\n"), "\n")
	lines2 := strings.Split(strings.TrimRight(string(c2), "\n"), "\n")

	i, j := 0, 0
	for i < len(lines1) || j < len(lines2) {
		if i >= len(lines1) {
			if !suppress2 {
				col1 := ""
				if !suppress1 {
					col1 = "\t"
				}
				fmt.Fprintf(w, "%s%s\n", col1, lines2[j])
			}
			j++
		} else if j >= len(lines2) {
			if !suppress1 {
				fmt.Fprintln(w, lines1[i])
			}
			i++
		} else if lines1[i] < lines2[j] {
			if !suppress1 {
				fmt.Fprintln(w, lines1[i])
			}
			i++
		} else if lines1[i] > lines2[j] {
			if !suppress2 {
				col1 := ""
				if !suppress1 {
					col1 = "\t"
				}
				fmt.Fprintf(w, "%s%s\n", col1, lines2[j])
			}
			j++
		} else {
			// Equal
			if !suppress3 {
				prefix := ""
				if !suppress1 {
					prefix += "\t"
				}
				if !suppress2 {
					prefix += "\t"
				}
				fmt.Fprintf(w, "%s%s\n", prefix, lines1[i])
			}
			i++
			j++
		}
	}
	return 0
}
