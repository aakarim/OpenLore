package cmds

import (
	"fmt"
	"io"
	"strings"
)

func CmdType(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	if len(args) == 0 {
		return 0
	}
	exitCode := 0
	for _, name := range args {
		found := false
		if IsKnown(name) {
			fmt.Fprintf(w, "%s is a shell builtin\n", name)
			found = true
		}
		if !found {
			fmt.Fprintf(errW, "-bash: type: %s: not found\n", name)
			exitCode = 1
		}
	}
	return exitCode
}

func CmdCommand(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	vFlag := false
	var cmdArgs []string
	for _, a := range args {
		if a == "-v" || a == "-V" {
			vFlag = true
		} else {
			cmdArgs = append(cmdArgs, a)
		}
	}

	if vFlag {
		exitCode := 0
		for _, name := range cmdArgs {
			found := false
			if IsKnown(name) {
				fmt.Fprintln(w, name)
				found = true
			}
			if !found {
				exitCode = 1
			}
		}
		return exitCode
	}

	// Without -v, just execute the command
	if len(cmdArgs) > 0 {
		if code, ok := execArgsPreserving(ctx, cmdArgs, w, errW, nil); ok {
			return code
		}
		cmdLine := strings.Join(cmdArgs, " ")
		return ctx.Exec(cmdLine, w, errW, nil)
	}
	return 0
}
