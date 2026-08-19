package cmds

import (
	"fmt"
	"io"
)

type HistoryBackend interface {
	Query(principal, actor string) ([]byte, error)
}

type historyContext interface {
	HistoryBackend() HistoryBackend
}

func CmdHistory(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	provider, ok := ctx.(historyContext)
	if !ok || provider.HistoryBackend() == nil {
		fmt.Fprintln(w, "history: not available in this shell")
		return 0
	}
	principal, actor := "", ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--principal":
			i++
			if i >= len(args) {
				fmt.Fprintln(errW, "history: --principal requires a value")
				return 1
			}
			principal = args[i]
		case "--actor":
			i++
			if i >= len(args) {
				fmt.Fprintln(errW, "history: --actor requires a value")
				return 1
			}
			actor = args[i]
		default:
			fmt.Fprintf(errW, "history: unknown argument %q\n", args[i])
			return 1
		}
	}
	b, err := provider.HistoryBackend().Query(principal, actor)
	if err != nil {
		fmt.Fprintf(errW, "history: %v\n", err)
		return 1
	}
	_, _ = w.Write(b)
	return 0
}

func CmdAlias(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	fmt.Fprintln(errW, "alias: not supported in this shell")
	return 1
}

func CmdUnalias(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	fmt.Fprintln(errW, "unalias: not supported in this shell")
	return 1
}
