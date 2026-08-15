package cmds

import (
	"io"
	"strings"
)

// CmdTee reads stdin fully, writes it to stdout, and commits it to each file
// argument as a single atomic whole-object write (no streaming). `-a` appends.
// On a read-only filesystem the file writes fail with a clear error, but the
// pass-through to stdout still happens (so `tee` in a read-only pipe is inert
// for files but transparent for data).
func CmdTee(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	appendMode := false
	var files []string
	for _, a := range args {
		switch {
		case a == "-a" || a == "--append":
			appendMode = true
		case a == "-i" || a == "--ignore-interrupts":
			// accepted, no-op
		case strings.HasPrefix(a, "-") && a != "-":
			// ignore other flags
		default:
			files = append(files, a)
		}
	}

	var data []byte
	if stdin != nil {
		data, _ = io.ReadAll(stdin)
	}

	// Pass-through to stdout first (transparent data flow).
	w.Write(data)

	code := 0
	for _, f := range files {
		if c := WriteFileMsg(ctx, errW, "tee", f, data, appendMode); c != 0 {
			code = c
		}
	}
	return code
}
