package cmds

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

func CmdTail(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
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
				fmt.Fprintln(errW, "tail: option requires an argument -- 'c'")
				return 1
			}
			count, err := strconv.Atoi(args[i+1])
			if err != nil || count < 0 {
				fmt.Fprintf(errW, "tail: invalid number of bytes: %s\n", args[i+1])
				return 1
			}
			byteCount = count
			i++
		} else if strings.HasPrefix(args[i], "-c") && len(args[i]) > 2 {
			count, err := strconv.Atoi(args[i][2:])
			if err != nil || count < 0 {
				fmt.Fprintf(errW, "tail: invalid number of bytes: %s\n", args[i][2:])
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
			start := len(data) - byteCount
			if start < 0 {
				start = 0
			}
			_, _ = w.Write(data[start:])
			return 0
		}
		lines := strings.Split(string(data), "\n")
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}
		fmt.Fprintln(w, strings.Join(lines, "\n"))
		return 0
	}
	if len(files) == 0 {
		fmt.Fprintln(errW, "tail: missing file operand")
		return 1
	}
	for _, f := range files {
		p := ctx.Resolve(f)
		content, err := ctx.FS().ReadFile(p)
		if err != nil {
			fmt.Fprintf(errW, "tail: %s: %s\n", f, err)
			return 1
		}
		if byteCount >= 0 {
			start := len(content) - byteCount
			if start < 0 {
				start = 0
			}
			_, _ = w.Write(content[start:])
			continue
		}
		lines := strings.Split(string(content), "\n")
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}
		fmt.Fprintln(w, strings.Join(lines, "\n"))
	}
	return 0
}
