package cmds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

func CmdStat(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	factsProvider := analyticsFacts(ctx)
	jsonFlag := false
	if len(args) > 0 && args[0] == "--json" {
		jsonFlag = true
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(errW, "stat: missing file operand")
		return 1
	}
	for _, a := range args {
		p := ctx.Resolve(a)
		f, err := ctx.FS().Stat(p)
		if err != nil {
			fmt.Fprintf(errW, "stat: %s: %s\n", a, err)
			return 1
		}
		if jsonFlag && factsProvider != nil {
			facts, factErr := factsProvider.Stat(context.Background(), p)
			if factErr != nil {
				fmt.Fprintf(errW, "stat: %s: %s\n", a, factErr)
				return 1
			}
			_ = json.NewEncoder(w).Encode(facts)
			continue
		}
		ftype, file := "regular file", p
		switch {
		case f.LinkTarget != "":
			ftype, file = "symbolic link", p+" -> "+f.LinkTarget
		case f.Dir:
			ftype = "directory"
		}
		fmt.Fprintf(w, "  File: %s\n", file)
		fmt.Fprintf(w, "  Size: %d\tType: %s\n", f.FileSize, ftype)
		fmt.Fprintf(w, "Access: %s\n", longMode(ctx, p, f))
		if !f.FileModTime.IsZero() {
			fmt.Fprintf(w, "Modify: %s\n", f.FileModTime.Format("2006-01-02 15:04:05"))
		}
	}
	return 0
}
