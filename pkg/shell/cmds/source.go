package cmds

import (
	"fmt"
	"io"
	"strings"
)

func CmdSource(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	if len(args) == 0 {
		fmt.Fprintln(errW, "source: filename argument required")
		return 1
	}

	filePath := ctx.Resolve(args[0])
	content, err := ctx.FS().ReadFile(filePath)
	if err != nil {
		fmt.Fprintf(errW, "source: %s: %s\n", args[0], err)
		return 1
	}

	exitCode := 0
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		exitCode = ctx.ExecPipeline(line, w, errW, nil)
		// A sourced file runs in the caller's shell, so `exit` ends the file
		// and leaves the request set for the caller to stop too.
		if ec, ok := ctx.(exitContext); ok && ec.ExitRequested() {
			break
		}
	}
	return exitCode
}

type exitContext interface{ ExitRequested() bool }

func CmdEval(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	if len(args) == 0 {
		return 0
	}
	cmdLine := strings.Join(args, " ")
	return ctx.ExecPipeline(cmdLine, w, errW, nil)
}
