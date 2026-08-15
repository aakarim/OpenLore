package cmds

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

func CmdJoin(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	field1 := 1
	field2 := 1
	sep := " "

	var files []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-1":
			if i+1 < len(args) {
				field1, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "-2":
			if i+1 < len(args) {
				field2, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "-t":
			if i+1 < len(args) {
				sep = args[i+1]
				i++
			}
		default:
			if !strings.HasPrefix(args[i], "-") {
				files = append(files, args[i])
			}
		}
	}

	if len(files) < 2 {
		fmt.Fprintln(errW, "join: missing operand")
		return 1
	}

	p1 := ctx.Resolve(files[0])
	p2 := ctx.Resolve(files[1])

	c1, err := ctx.FS().ReadFile(p1)
	if err != nil {
		fmt.Fprintf(errW, "join: %s: %s\n", files[0], err)
		return 1
	}
	c2, err := ctx.FS().ReadFile(p2)
	if err != nil {
		fmt.Fprintf(errW, "join: %s: %s\n", files[1], err)
		return 1
	}

	lines1 := strings.Split(strings.TrimRight(string(c1), "\n"), "\n")
	lines2 := strings.Split(strings.TrimRight(string(c2), "\n"), "\n")

	// Build map from file2
	file2Map := make(map[string][]string)
	for _, l := range lines2 {
		fields := strings.Split(l, sep)
		if field2 > 0 && field2 <= len(fields) {
			key := fields[field2-1]
			file2Map[key] = append(file2Map[key], l)
		}
	}

	for _, l := range lines1 {
		fields1 := strings.Split(l, sep)
		if field1 > 0 && field1 <= len(fields1) {
			key := fields1[field1-1]
			if matches, ok := file2Map[key]; ok {
				for _, m := range matches {
					fields2 := strings.Split(m, sep)
					var out []string
					out = append(out, key)
					for j, f := range fields1 {
						if j+1 != field1 {
							out = append(out, f)
						}
					}
					for j, f := range fields2 {
						if j+1 != field2 {
							out = append(out, f)
						}
					}
					fmt.Fprintln(w, strings.Join(out, sep))
				}
			}
		}
	}
	return 0
}
