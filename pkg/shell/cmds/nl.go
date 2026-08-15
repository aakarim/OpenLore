package cmds

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

func CmdNl(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	bodyNumbering := "t" // t=nonempty, a=all
	format := "rn"       // rn=right justified, ln=left justified, rz=right justified leading zeros
	width := 6
	separator := "\t"

	var files []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-b":
			if i+1 < len(args) {
				bodyNumbering = args[i+1]
				i++
			}
		case "-n":
			if i+1 < len(args) {
				format = args[i+1]
				i++
			}
		case "-w":
			if i+1 < len(args) {
				width, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "-s":
			if i+1 < len(args) {
				separator = args[i+1]
				i++
			}
		default:
			if strings.HasPrefix(args[i], "-") {
				ReportUnsupportedFlag(ctx, "nl", args[i])
			} else {
				files = append(files, args[i])
			}
		}
	}

	lines, code := ReadInputLines(ctx, files, stdin, errW, "nl")
	if code != 0 {
		return code
	}

	num := 0
	for _, line := range lines {
		shouldNumber := false
		switch bodyNumbering {
		case "a":
			shouldNumber = true
		case "t":
			shouldNumber = strings.TrimSpace(line) != ""
		case "n":
			shouldNumber = false
		}

		if shouldNumber {
			num++
			var numStr string
			switch format {
			case "ln":
				numStr = fmt.Sprintf("%-*d", width, num)
			case "rz":
				numStr = fmt.Sprintf("%0*d", width, num)
			default: // rn
				numStr = fmt.Sprintf("%*d", width, num)
			}
			fmt.Fprintf(w, "%s%s%s\n", numStr, separator, line)
		} else {
			fmt.Fprintf(w, "%*s%s%s\n", width, "", separator, line)
		}
	}
	return 0
}
