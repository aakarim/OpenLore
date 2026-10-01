package cmds

import (
	"fmt"
	"io"
	"strings"
	"time"
)

func CmdTrue(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	return 0
}

func CmdFalse(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	return 1
}

func CmdClear(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	fmt.Fprint(w, "\033[2J\033[H")
	return 0
}

func CmdSleep(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	fmt.Fprintln(errW, "sleep: not supported in this shell")
	return 0
}

func CmdTimeout(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	// Skip the timeout value, execute the rest as a command
	if len(args) < 2 {
		fmt.Fprintln(errW, "timeout: missing operand")
		return 1
	}
	if code, ok := execArgsPreserving(ctx, args[1:], w, errW, nil); ok {
		return code
	}
	cmdLine := strings.Join(args[1:], " ")
	return ctx.Exec(cmdLine, w, errW, nil)
}

func CmdTime(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	if len(args) == 0 {
		return 0
	}
	start := time.Now()
	exitCode, ok := execArgsPreserving(ctx, args, w, errW, nil)
	if !ok {
		exitCode = ctx.Exec(strings.Join(args, " "), w, errW, nil)
	}
	elapsed := time.Since(start)
	fmt.Fprintf(errW, "\nreal\t%s\n", elapsed.Round(time.Millisecond))
	return exitCode
}
