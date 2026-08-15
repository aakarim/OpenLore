package cmds

import (
	"fmt"
	"io"
	"strings"
)

func CmdColumn(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	tableMode := false
	sep := ""

	var files []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-t":
			tableMode = true
		case "-s":
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

	lines, code := ReadInputLines(ctx, files, stdin, errW, "column")
	if code != 0 {
		return code
	}

	if !tableMode {
		for _, l := range lines {
			fmt.Fprintln(w, l)
		}
		return 0
	}

	// Parse into fields
	var rows [][]string
	maxCols := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var fields []string
		if sep != "" {
			fields = strings.Split(l, sep)
		} else {
			fields = strings.Fields(l)
		}
		rows = append(rows, fields)
		if len(fields) > maxCols {
			maxCols = len(fields)
		}
	}

	// Compute column widths
	widths := make([]int, maxCols)
	for _, row := range rows {
		for j, f := range row {
			if len(f) > widths[j] {
				widths[j] = len(f)
			}
		}
	}

	for _, row := range rows {
		var parts []string
		for j, f := range row {
			if j < len(row)-1 {
				parts = append(parts, fmt.Sprintf("%-*s", widths[j], f))
			} else {
				parts = append(parts, f)
			}
		}
		fmt.Fprintln(w, strings.Join(parts, "  "))
	}
	return 0
}
