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

func printLongSized(w io.Writer, mode string, f *vfs.FileInfo, size, name string) {
	t := f.FileModTime.Format("Jan  2 15:04")
	fmt.Fprintf(w, "%s  1 lore lore %s %s %s\n", mode, size, t, name)
}

// longMode is the ls -l / stat mode string for the resolved path p. Alias roots
// show as links; otherwise the owner write bit reflects whether this session
// may write p, so an rw grant reads as -rw-r--r-- and ro as -r--r--r--.
func longMode(ctx CmdContext, p string, f *vfs.FileInfo) string {
	if f.LinkTarget != "" {
		return "lrwxrwxrwx"
	}
	mode := f.Mode()
	if pathWritable(ctx.FS(), p) {
		mode |= 0200
	}
	return mode.String()
}

// pathWritable reports whether a write to p is in this session's scope. A
// filesystem without a declared write scope is writable exactly when it
// implements vfs.WritableFS.
func pathWritable(fsys vfs.FileSystem, p string) bool {
	if scope, ok := fsys.(vfs.WriteScopeFS); ok {
		return scope.CanWrite(p)
	}
	_, ok := fsys.(vfs.WritableFS)
	return ok
}
