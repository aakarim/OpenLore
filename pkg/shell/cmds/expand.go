package cmds

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

func CmdExpand(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	tabstop := 8

	var files []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-t" && i+1 < len(args) {
			tabstop, _ = strconv.Atoi(args[i+1])
			i++
		} else if strings.HasPrefix(args[i], "-") {
			ReportUnsupportedFlag(ctx, "expand", args[i])
		} else {
			files = append(files, args[i])
		}
	}

	lines, code := ReadInputLines(ctx, files, stdin, errW, "expand")
	if code != 0 {
		return code
	}

	spaces := strings.Repeat(" ", tabstop)
	for _, line := range lines {
		fmt.Fprintln(w, strings.ReplaceAll(line, "\t", spaces))
	}
	return 0
}

func CmdUnexpand(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	tabstop := 8
	allFlag := false

	var files []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-t":
			if i+1 < len(args) {
				tabstop, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "-a":
			allFlag = true
		default:
			if strings.HasPrefix(args[i], "-") {
				ReportUnsupportedFlag(ctx, "unexpand", args[i])
			} else {
				files = append(files, args[i])
			}
		}
	}

	lines, code := ReadInputLines(ctx, files, stdin, errW, "unexpand")
	if code != 0 {
		return code
	}

	spaces := strings.Repeat(" ", tabstop)
	for _, line := range lines {
		if allFlag {
			fmt.Fprintln(w, strings.ReplaceAll(line, spaces, "\t"))
		} else {
			// Only convert leading spaces
			trimmed := strings.TrimLeft(line, " ")
			leading := len(line) - len(trimmed)
			tabs := leading / tabstop
			remainder := leading % tabstop
			fmt.Fprintln(w, strings.Repeat("\t", tabs)+strings.Repeat(" ", remainder)+trimmed)
		}
	}
	return 0
}
