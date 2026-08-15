package cmds

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

func CmdFold(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	width := 80
	breakSpaces := false

	var files []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-w":
			if i+1 < len(args) {
				width, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "-s":
			breakSpaces = true
		default:
			if strings.HasPrefix(args[i], "-") {
				ReportUnsupportedFlag(ctx, "fold", args[i])
			} else {
				files = append(files, args[i])
			}
		}
	}

	lines, code := ReadInputLines(ctx, files, stdin, errW, "fold")
	if code != 0 {
		return code
	}

	for _, line := range lines {
		if len(line) <= width {
			fmt.Fprintln(w, line)
			continue
		}
		for len(line) > width {
			breakAt := width
			if breakSpaces {
				last := strings.LastIndex(line[:width], " ")
				if last > 0 {
					breakAt = last + 1
				}
			}
			fmt.Fprintln(w, line[:breakAt])
			line = line[breakAt:]
		}
		if len(line) > 0 {
			fmt.Fprintln(w, line)
		}
	}
	return 0
}
