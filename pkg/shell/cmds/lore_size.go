package cmds

import (
	"fmt"
	"io"
)

func cmdLoreSize(ctx CmdContext, args []string, w, errW io.Writer, _ io.Reader) int {
	provider, ok := ctx.(sizeContext)
	if !ok || provider.SizeBackend() == nil {
		fmt.Fprintln(errW, "lore size: baseline state is unavailable")
		return 1
	}
	if len(args) >= 2 && args[0] == "baseline" && args[1] != "reset" {
		if len(args) != 2 {
			fmt.Fprintln(errW, "usage: lore size baseline <path>")
			return 1
		}
		out, err := provider.SizeBackend().Baseline(ctx.Resolve(args[1]))
		if err != nil {
			fmt.Fprintf(errW, "lore size baseline: %v\n", err)
			return 1
		}
		fmt.Fprint(w, out)
		return 0
	}
	if len(args) >= 3 && args[0] == "baseline" && args[1] == "reset" {
		path, note := ctx.Resolve(args[2]), ""
		if len(args) == 5 && args[3] == "--note" {
			note = args[4]
		} else if len(args) != 3 {
			fmt.Fprintln(errW, "usage: lore size baseline reset <path> [--note <text>]")
			return 1
		}
		out, err := provider.SizeBackend().Reset(path, note, commandAttribution(ctx))
		if err != nil {
			fmt.Fprintf(errW, "lore size baseline reset: %v\n", err)
			return 1
		}
		fmt.Fprint(w, out)
		return 0
	}
	fmt.Fprintln(errW, "usage: lore size baseline <path> | lore size baseline reset <path> [--note <text>]")
	return 1
}

func init() {
	RegisterLoreSub(LoreSub{Name: "size", Summary: "Inspect and reset size baselines", Run: cmdLoreSize})
}
