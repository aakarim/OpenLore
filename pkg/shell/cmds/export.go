package cmds

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

func CmdExport(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	if len(args) == 0 || (len(args) == 1 && args[0] == "-p") {
		env := ctx.AllEnv()
		if env == nil {
			return 0
		}
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "declare -x %s=\"%s\"\n", k, env[k])
		}
		return 0
	}

	for _, arg := range args {
		if arg == "-p" {
			continue
		}
		if idx := strings.Index(arg, "="); idx >= 0 {
			key := arg[:idx]
			value := arg[idx+1:]
			ctx.SetEnv(key, value)
		} else {
			// Ensure variable exists in env
			if ctx.GetEnv(arg) == "" {
				ctx.SetEnv(arg, "")
			}
		}
	}
	return 0
}

func CmdUnset(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	for _, name := range args {
		ctx.DeleteEnv(name)
	}
	return 0
}

func CmdSet(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	if len(args) == 0 {
		env := ctx.AllEnv()
		if env == nil {
			return 0
		}
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "%s='%s'\n", k, env[k])
		}
		return 0
	}

	setPositional := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--":
			// Remaining args become positional parameters
			positional := args[i+1:]
			// Clear old positional params
			for k := range ctx.AllEnv() {
				if _, err := strconv.Atoi(k); err == nil {
					ctx.DeleteEnv(k)
				}
			}
			for j, val := range positional {
				ctx.SetEnv(strconv.Itoa(j+1), val)
			}
			ctx.SetEnv("#", strconv.Itoa(len(positional)))
			setPositional = true
			i = len(args) // break
		case "-e", "-x", "-u", "+e", "+x", "+u":
			ReportUnsupportedFlag(ctx, "set", args[i])
		}
	}
	_ = setPositional
	return 0
}
