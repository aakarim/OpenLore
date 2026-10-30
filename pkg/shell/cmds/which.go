package cmds

import (
	"fmt"
	"io"
)

func CmdWhich(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	if len(args) == 0 {
		return 1
	}
	exitCode := 0
	for _, name := range args {
		found := false
		if IsKnown(name) && ctx.ActionAllowed(ActionFor(name)) {
			fmt.Fprintf(w, "%s: shell built-in command\n", name)
			found = true
		}
		if !found {
			fmt.Fprintf(errW, "which: no %s in shell\n", name)
			exitCode = 1
		}
	}
	return exitCode
}
