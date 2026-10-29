package cmds

import (
	"fmt"
	"io"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

type HistoryBackend interface {
	Query(principal, actor string) ([]byte, error)
}

type PathHistoryBackend interface {
	QueryPath(path string) ([]byte, error)
}

func cmdLoreHistory(ctx CmdContext, args []string, w, errW io.Writer, stdin io.Reader) int {
	if len(args) != 1 || args[0] == "--help" {
		fmt.Fprintln(errW, "usage: lore history <path> (newest first, follows moves within readable docsets)")
		if len(args) == 1 && args[0] == "--help" {
			return 0
		}
		return 1
	}
	provider, ok := ctx.(historyContext)
	if !ok || provider.HistoryBackend() == nil {
		fmt.Fprintln(errW, "lore history: not available in this shell")
		return 1
	}
	backend, ok := provider.HistoryBackend().(PathHistoryBackend)
	if !ok {
		fmt.Fprintln(errW, "lore history: path history is not available")
		return 1
	}
	target := ctx.Resolve(args[0])
	if c, ok := ctx.FS().(vfs.PathCanonicalizer); ok {
		target = c.CanonicalPath(target)
	}
	data, err := backend.QueryPath(target)
	if err != nil {
		fmt.Fprintf(errW, "lore history: %v\n", err)
		return 1
	}
	_, err = w.Write(data)
	if err != nil {
		fmt.Fprintf(errW, "lore history: %v\n", err)
		return 1
	}
	return 0
}

func init() {
	RegisterLoreSub(LoreSub{Name: "history", Summary: "Show a path's provenance, following moves", Run: cmdLoreHistory})
}

// HistoryGCBackend is optional so existing query-only history providers remain
// source compatible. Implementations should return a human-readable summary.
type HistoryGCBackend interface {
	GC() ([]byte, error)
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
	if len(args) == 1 && args[0] == "gc" {
		gc, ok := provider.HistoryBackend().(HistoryGCBackend)
		if !ok {
			fmt.Fprintln(errW, "history: gc not available")
			return 1
		}
		b, err := gc.GC()
		if err != nil {
			fmt.Fprintf(errW, "history: gc: %v\n", err)
			return 1
		}
		_, _ = w.Write(b)
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
