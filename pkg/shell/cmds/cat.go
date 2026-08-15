package cmds

import (
	"fmt"
	"io"
)

func CmdCat(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	if len(args) == 0 && stdin != nil {
		io.Copy(w, stdin)
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(errW, "cat: missing file operand")
		return 1
	}
	exitCode := 0
	for _, a := range args {
		if len(a) > 1 && a[0] == '-' {
			ReportUnsupportedFlag(ctx, "cat", a)
		}
		p := ctx.Resolve(a)
		content, err := ctx.FS().ReadFile(p)
		if err != nil {
			fmt.Fprintf(errW, "cat: %s: %s\n", a, err)
			exitCode = 1
			continue
		}
		w.Write(content)
	}
	return exitCode
}
