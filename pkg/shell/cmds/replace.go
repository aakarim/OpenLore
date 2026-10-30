package cmds

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

func init() {
	Register("replace", CmdReplace)
}

// CmdReplace replaces literal bytes, requiring an unambiguous match unless
// --all is requested. Arguments have no regex or escape interpretation.
func CmdReplace(ctx CmdContext, args []string, w, errW io.Writer, stdin io.Reader) int {
	dryRun, all := false, false
	for len(args) > 0 {
		a := args[0]
		if a == "--" {
			args = args[1:]
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			break
		}
		switch a {
		case "-n":
			dryRun = true
		case "--all":
			all = true
		default:
			fmt.Fprintf(errW, "replace: unknown option %q\n", a)
			return 1
		}
		args = args[1:]
	}
	if len(args) != 3 || args[1] == "" {
		fmt.Fprintln(errW, "usage: replace [-n] [--all] <file> <old> <new> (old must not be empty)")
		return 1
	}
	p, old, replacement := args[0], []byte(args[1]), []byte(args[2])
	base, err := ctx.FS().ReadFile(ctx.Resolve(p))
	if err != nil {
		fmt.Fprintf(errW, "replace: %s: %s\n", p, err)
		return 1
	}
	count := bytes.Count(base, old)
	if count == 0 || (!all && count != 1) {
		fmt.Fprintf(errW, "replace: %s: found %d matches; expected exactly one (use --all for multiple matches)\n", p, count)
		return 1
	}
	updated := bytes.ReplaceAll(base, old, replacement)
	if dryRun {
		if err := replaceDiff(w, p, base, updated); err != nil {
			fmt.Fprintf(errW, "replace: %s\n", err)
			return 1
		}
		return 0
	}
	return WriteFileCASMsg(ctx, errW, "replace", p, updated, base)
}

// replaceDiff emits a single unified hunk spanning the changed lines. Trimming
// equal prefixes/suffixes avoids the quadratic memory cost of an LCS diff.
func replaceDiff(w io.Writer, p string, before, after []byte) error {
	if bytes.Equal(before, after) {
		return nil
	}
	a, b := strings.SplitAfter(string(before), "\n"), strings.SplitAfter(string(after), "\n")
	if a[len(a)-1] == "" {
		a = a[:len(a)-1]
	}
	if b[len(b)-1] == "" {
		b = b[:len(b)-1]
	}
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	aEnd, bEnd := len(a), len(b)
	for aEnd > prefix && bEnd > prefix && a[aEnd-1] == b[bEnd-1] {
		aEnd--
		bEnd--
	}
	aStart, bStart := prefix+1, prefix+1
	if aEnd == prefix {
		aStart = prefix
	}
	if bEnd == prefix {
		bStart = prefix
	}
	var diff strings.Builder
	fmt.Fprintf(&diff, "--- %s\n+++ %s\n@@ -%d,%d +%d,%d @@\n", p, p, aStart, aEnd-prefix, bStart, bEnd-prefix)
	for _, side := range []struct {
		marker byte
		lines  []string
	}{{'-', a[prefix:aEnd]}, {'+', b[prefix:bEnd]}} {
		for _, line := range side.lines {
			diff.WriteByte(side.marker)
			diff.WriteString(line)
			if !strings.HasSuffix(line, "\n") {
				diff.WriteString("\n\\ No newline at end of file\n")
			}
		}
	}
	_, err := io.WriteString(w, diff.String())
	return err
}
