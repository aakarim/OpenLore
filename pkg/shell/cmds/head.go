package cmds

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

func CmdHead(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	n := 10
	byteCount := -1
	var files []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-n" && i+1 < len(args) {
			fmt.Sscanf(args[i+1], "%d", &n)
			byteCount = -1
			i++
		} else if args[i] == "-c" {
			if i+1 >= len(args) {
				fmt.Fprintln(errW, "head: option requires an argument -- 'c'")
				return 1
			}
			count, err := strconv.Atoi(args[i+1])
			if err != nil || count < 0 {
				fmt.Fprintf(errW, "head: invalid number of bytes: %s\n", args[i+1])
				return 1
			}
			byteCount = count
			i++
		} else if strings.HasPrefix(args[i], "-c") && len(args[i]) > 2 {
			count, err := strconv.Atoi(args[i][2:])
			if err != nil || count < 0 {
				fmt.Fprintf(errW, "head: invalid number of bytes: %s\n", args[i][2:])
				return 1
			}
			byteCount = count
		} else if strings.HasPrefix(args[i], "-") && len(args[i]) > 1 {
			fmt.Sscanf(args[i][1:], "%d", &n)
			byteCount = -1
		} else {
			files = append(files, args[i])
		}
	}
	if len(files) == 0 && stdin != nil {
		data, _ := io.ReadAll(stdin)
		if byteCount >= 0 {
			if byteCount > len(data) {
				byteCount = len(data)
			}
			_, _ = w.Write(data[:byteCount])
			return 0
		}
		lines := strings.SplitN(string(data), "\n", n+1)
		if len(lines) > n {
			lines = lines[:n]
		}
		fmt.Fprintln(w, strings.Join(lines, "\n"))
		return 0
	}
	if len(files) == 0 {
		fmt.Fprintln(errW, "head: missing file operand")
		return 1
	}
	for _, f := range files {
		p := ctx.Resolve(f)
		content, err := ctx.FS().ReadFile(p)
		if err != nil {
			fmt.Fprintf(errW, "head: %s: %s\n", f, err)
			return 1
		}
		if byteCount >= 0 {
			end := byteCount
			if end > len(content) {
				end = len(content)
			}
			_, _ = w.Write(content[:end])
			continue
		}
		lines := strings.SplitN(string(content), "\n", n+1)
		if len(lines) > n {
			lines = lines[:n]
		}
		fmt.Fprintln(w, strings.Join(lines, "\n"))
	}
	return 0
}
