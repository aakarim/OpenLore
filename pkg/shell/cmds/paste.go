package cmds

import (
	"fmt"
	"io"
	"strings"
)

func CmdPaste(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	delimiter := "\t"
	serial := false

	var files []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-d":
			if i+1 < len(args) {
				delimiter = args[i+1]
				i++
			}
		case "-s":
			serial = true
		default:
			if strings.HasPrefix(args[i], "-") {
				ReportUnsupportedFlag(ctx, "paste", args[i])
			} else {
				files = append(files, args[i])
			}
		}
	}

	var fileContents [][]string
	for _, f := range files {
		p := ctx.Resolve(f)
		content, err := ctx.FS().ReadFile(p)
		if err != nil {
			fmt.Fprintf(errW, "paste: %s: %s\n", f, err)
			return 1
		}
		fileContents = append(fileContents, strings.Split(string(content), "\n"))
	}

	if len(fileContents) == 0 && stdin != nil {
		data, _ := io.ReadAll(stdin)
		fileContents = append(fileContents, strings.Split(string(data), "\n"))
	}

	if serial {
		for _, fc := range fileContents {
			fmt.Fprintln(w, strings.Join(fc, delimiter))
		}
		return 0
	}

	maxLines := 0
	for _, fc := range fileContents {
		if len(fc) > maxLines {
			maxLines = len(fc)
		}
	}

	for i := 0; i < maxLines; i++ {
		var parts []string
		for _, fc := range fileContents {
			if i < len(fc) {
				parts = append(parts, fc[i])
			} else {
				parts = append(parts, "")
			}
		}
		fmt.Fprintln(w, strings.Join(parts, delimiter))
	}
	return 0
}
