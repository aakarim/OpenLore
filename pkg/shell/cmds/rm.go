package cmds

import (
	"errors"
	"fmt"
	"io"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

// CmdRm removes files and, with -r, directory trees. A directory requires -r.
// Deletion of a gated path (or a tree containing one) does not happen
// immediately: it becomes a pending delete changeset that an approver must
// resolve, reported here as pending (not a failure). -f only suppresses
// not-found / missing-operand errors; it never bypasses approval.
//
// Multiple operands are independent: one failing does not stop the others, but
// the command exits non-zero if any failed.
//
// Usage: rm [-r] [-f] <path>...
func CmdRm(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	recursive := false
	force := false
	var targets []string
	flagsDone := false
	for _, a := range args {
		if !flagsDone && len(a) > 1 && a[0] == '-' {
			switch a {
			case "--":
				flagsDone = true
			case "-r", "-R", "--recursive":
				recursive = true
			case "-f", "--force":
				force = true
			case "-rf", "-fr", "-Rf", "-fR":
				recursive = true
				force = true
			default:
				ReportUnsupportedFlag(ctx, "rm", a)
				fmt.Fprintf(errW, "rm: unknown option %q\n", a)
				return 1
			}
			continue
		}
		targets = append(targets, a)
	}
	if len(targets) == 0 {
		if force {
			return 0
		}
		fmt.Fprintln(errW, "rm: missing operand")
		return 1
	}
	wfs, ok := ctx.FS().(vfs.WritableFS)
	if !ok {
		fmt.Fprintln(errW, "rm: read-only filesystem")
		return 1
	}

	exit := 0
	for _, t := range targets {
		if rmOne(ctx, wfs, t, recursive, force, w, errW) != 0 {
			exit = 1
		}
	}
	return exit
}

func rmOne(ctx CmdContext, wfs vfs.WritableFS, t string, recursive, force bool, w, errW io.Writer) int {
	resolved := ctx.Resolve(t)
	info, statErr := ctx.FS().Stat(resolved)
	if statErr != nil {
		if force {
			return 0
		}
		fmt.Fprintf(errW, "rm: %s: No such file or directory\n", t)
		return 1
	}
	if info.Dir && !recursive {
		fmt.Fprintf(errW, "rm: %s: is a directory\n", t)
		return 1
	}

	var err error
	if recursive {
		err = wfs.RemoveAll(resolved, vfs.RemoveOpts{})
	} else {
		err = wfs.Remove(resolved)
	}
	return rmResult(w, errW, t, err)
}

// rmResult renders the shared exit code + message for a delete result.
func rmResult(w, errW io.Writer, t string, err error) int {
	if err == nil {
		return 0
	}
	var pce *vfs.PendingChangeError
	if errors.As(err, &pce) {
		// Not a failure: a middleware parked the delete as a pending change.
		fmt.Fprintln(w, pendingChangeLine("rm", t, pce))
		return 0
	}
	var te *vfs.TreeStaleError
	if errors.As(err, &te) {
		fmt.Fprintf(errW, "rm: %s: tree changed concurrently — re-run\n", t)
		return 1
	}
	if errors.Is(err, vfs.ErrReadOnly) {
		fmt.Fprintf(errW, "rm: %s: read-only filesystem\n", t)
		return 1
	}
	fmt.Fprintf(errW, "rm: %s: %s\n", t, err)
	return 1
}
