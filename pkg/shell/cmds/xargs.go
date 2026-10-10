package cmds

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

type argsExecutor interface {
	ExecArgs(args []string, w io.Writer, errW io.Writer, stdin io.Reader) int
}

func execArgsPreserving(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) (int, bool) {
	executor, ok := ctx.(argsExecutor)
	if !ok {
		return 1, false
	}
	return executor.ExecArgs(args, w, errW, stdin), true
}

func CmdXargs(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	replaceStr := ""
	delimiter := ""
	maxArgs := 0
	nullDelim := false
	var cmdParts []string

	for i := 0; i < len(args); i++ {
		// GNU xargs accepts the replacement string attached to the flag
		// (`-I{}`) as well as separated (`-I {}`).
		if strings.HasPrefix(args[i], "-I") && len(args[i]) > 2 {
			replaceStr = args[i][2:]
			continue
		}
		switch args[i] {
		case "-I":
			if i+1 < len(args) {
				replaceStr = args[i+1]
				i++
			}
		case "-d":
			if i+1 < len(args) {
				delimiter = args[i+1]
				i++
			}
		case "-n":
			if i+1 < len(args) {
				maxArgs, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "-0":
			nullDelim = true
		default:
			cmdParts = append(cmdParts, args[i])
		}
	}

	if len(cmdParts) == 0 {
		cmdParts = []string{"echo"}
	}

	if stdin == nil {
		return 0
	}

	data, _ := io.ReadAll(stdin)
	input := string(data)

	var items []string
	if nullDelim {
		items = strings.Split(input, "\x00")
	} else if delimiter != "" {
		items = strings.Split(input, delimiter)
	} else if replaceStr != "" {
		items = strings.Split(input, "\n")
	} else {
		items = strings.Fields(input)
	}

	// Remove empty items
	var filtered []string
	for _, item := range items {
		if item != "" {
			filtered = append(filtered, item)
		}
	}
	items = filtered
	executor, ok := ctx.(argsExecutor)
	if !ok {
		fmt.Fprintln(errW, "xargs: argument-preserving execution is unavailable")
		return 1
	}

	lastExit := 0

	if replaceStr != "" {
		for _, item := range items {
			cmdArgs := make([]string, 0, len(cmdParts))
			for _, p := range cmdParts {
				cmdArgs = append(cmdArgs, strings.ReplaceAll(p, replaceStr, item))
			}
			lastExit = executor.ExecArgs(cmdArgs, w, errW, nil)
		}
		return lastExit
	}

	if maxArgs > 0 {
		for i := 0; i < len(items); i += maxArgs {
			end := i + maxArgs
			if end > len(items) {
				end = len(items)
			}
			cmdArgs := append(append([]string(nil), cmdParts...), items[i:end]...)
			lastExit = executor.ExecArgs(cmdArgs, w, errW, nil)
		}
		return lastExit
	}

	cmdArgs := append(append([]string(nil), cmdParts...), items...)
	return executor.ExecArgs(cmdArgs, w, errW, nil)
}
