package cmds

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

func CmdCut(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	delimiter := "\t"
	charPositions := ""
	fieldPositions := ""
	suppress := false

	var files []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-d":
			if i+1 < len(args) {
				delimiter = args[i+1]
				i++
			}
		case "-c":
			if i+1 < len(args) {
				charPositions = args[i+1]
				i++
			}
		case "-f":
			if i+1 < len(args) {
				fieldPositions = args[i+1]
				i++
			}
		case "-s":
			suppress = true
		default:
			if strings.HasPrefix(args[i], "-") {
				ReportUnsupportedFlag(ctx, "cut", args[i])
			} else {
				files = append(files, args[i])
			}
		}
	}

	lines, code := ReadInputLines(ctx, files, stdin, errW, "cut")
	if code != 0 {
		return code
	}

	positions := parsePositions(charPositions + fieldPositions)

	for _, line := range lines {
		if fieldPositions != "" {
			fields := strings.Split(line, delimiter)
			if suppress && !strings.Contains(line, delimiter) {
				continue
			}
			var selected []string
			for _, p := range positions {
				if p > 0 && p <= len(fields) {
					selected = append(selected, fields[p-1])
				}
			}
			fmt.Fprintln(w, strings.Join(selected, delimiter))
		} else if charPositions != "" {
			runes := []rune(line)
			var sb strings.Builder
			for _, p := range positions {
				if p > 0 && p <= len(runes) {
					sb.WriteRune(runes[p-1])
				}
			}
			fmt.Fprintln(w, sb.String())
		}
	}
	return 0
}

func parsePositions(spec string) []int {
	var result []int
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			start, _ := strconv.Atoi(bounds[0])
			end, _ := strconv.Atoi(bounds[1])
			if start == 0 {
				start = 1
			}
			if end == 0 {
				end = start
			}
			for i := start; i <= end; i++ {
				result = append(result, i)
			}
		} else {
			n, err := strconv.Atoi(part)
			if err == nil {
				result = append(result, n)
			}
		}
	}
	return result
}
