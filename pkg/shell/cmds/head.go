package cmds

import (
	"fmt"
	"io"
	"strings"
)

func CmdHead(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	n := 10
	var files []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-n" && i+1 < len(args) {
			fmt.Sscanf(args[i+1], "%d", &n)
			i++
		} else if strings.HasPrefix(args[i], "-") && len(args[i]) > 1 {
			fmt.Sscanf(args[i][1:], "%d", &n)
		} else {
			files = append(files, args[i])
		}
	}
	if len(files) == 0 && stdin != nil {
		data, _ := io.ReadAll(stdin)
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
		lines := strings.SplitN(string(content), "\n", n+1)
		if len(lines) > n {
			lines = lines[:n]
		}
		fmt.Fprintln(w, strings.Join(lines, "\n"))
	}
	return 0
}
