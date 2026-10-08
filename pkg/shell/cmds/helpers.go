package cmds

import (
	"fmt"
	"io"
	"strings"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

// ReadInputLines reads lines from files or stdin, used by many text commands.
func ReadInputLines(ctx CmdContext, files []string, stdin io.Reader, errW io.Writer, cmdName string) ([]string, int) {
	if len(files) == 0 {
		if stdin == nil {
			return nil, 0
		}
		data, _ := io.ReadAll(stdin)
		text := string(data)
		if strings.HasSuffix(text, "\n") {
			text = text[:len(text)-1]
		}
		if text == "" {
			return nil, 0
		}
		return strings.Split(text, "\n"), 0
	}

	var allLines []string
	for _, f := range files {
		p := ctx.Resolve(f)
		content, err := ctx.FS().ReadFile(p)
		if err != nil {
			fmt.Fprintf(errW, "%s: %s: %s\n", cmdName, f, err)
			return nil, 1
		}
		text := string(content)
		if strings.HasSuffix(text, "\n") {
			text = text[:len(text)-1]
		}
		allLines = append(allLines, strings.Split(text, "\n")...)
	}
	return allLines, 0
}

// PrintLong prints a file in long format (used by ls -l).
func PrintLong(w io.Writer, f *vfs.FileInfo) {
	printLongSized(w, f, fmt.Sprintf("%8d", f.FileSize), f.FileName)
}

func printLongSized(w io.Writer, f *vfs.FileInfo, size, name string) {
	mode := "-r--r--r--"
	if f.Dir {
		mode = "dr-xr-xr-x"
	}
	t := f.FileModTime.Format("Jan  2 15:04")
	fmt.Fprintf(w, "%s  1 lore lore %s %s %s\n", mode, size, t, name)
}
