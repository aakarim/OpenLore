package cmds

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

func CmdSort(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	reverse := false
	numeric := false
	unique := false
	caseFold := false
	fieldNum := 0
	sep := ""

	var files []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-k" {
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &fieldNum)
				i++
			}
		} else if a == "-t" {
			if i+1 < len(args) {
				sep = args[i+1]
				i++
			}
		} else if strings.HasPrefix(a, "-") && len(a) > 1 {
			longOption := strings.HasPrefix(a, "--")
			if longOption {
				ReportUnsupportedFlag(ctx, "sort", a)
			}
			for _, ch := range a[1:] {
				switch ch {
				case 'r':
					reverse = true
				case 'n':
					numeric = true
				case 'u':
					unique = true
				case 'f':
					caseFold = true
				default:
					if !longOption {
						ReportUnsupportedFlag(ctx, "sort", "-"+string(ch))
					}
				}
			}
		} else {
			files = append(files, a)
		}
	}

	lines, code := ReadInputLines(ctx, files, stdin, errW, "sort")
	if code != 0 {
		return code
	}

	sort.SliceStable(lines, func(i, j int) bool {
		a, b := lines[i], lines[j]
		if fieldNum > 0 {
			a = getField(a, fieldNum, sep)
			b = getField(b, fieldNum, sep)
		}
		if caseFold {
			a = strings.ToLower(a)
			b = strings.ToLower(b)
		}
		if numeric {
			an := parseLeadingFloat(a)
			bn := parseLeadingFloat(b)
			if an != bn {
				if reverse {
					return an > bn
				}
				return an < bn
			}
		}
		if reverse {
			return a > b
		}
		return a < b
	})

	if unique {
		lines = uniqueLines(lines, caseFold)
	}

	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	return 0
}

func getField(line string, fieldNum int, sep string) string {
	var fields []string
	if sep == "" {
		fields = strings.Fields(line)
	} else {
		fields = strings.Split(line, sep)
	}
	if fieldNum > 0 && fieldNum <= len(fields) {
		return fields[fieldNum-1]
	}
	return ""
}

func parseLeadingFloat(s string) float64 {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && (s[end] == '-' || s[end] == '+' || s[end] == '.' || (s[end] >= '0' && s[end] <= '9')) {
		end++
	}
	if end == 0 {
		return 0
	}
	f, _ := strconv.ParseFloat(s[:end], 64)
	return f
}

func uniqueLines(lines []string, caseFold bool) []string {
	if len(lines) == 0 {
		return lines
	}
	result := []string{lines[0]}
	for i := 1; i < len(lines); i++ {
		prev := result[len(result)-1]
		cur := lines[i]
		if caseFold {
			if strings.EqualFold(prev, cur) {
				continue
			}
		} else {
			if prev == cur {
				continue
			}
		}
		result = append(result, cur)
	}
	return result
}
