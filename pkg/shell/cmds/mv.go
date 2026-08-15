package cmds

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

// CmdMv moves a file through the same atomic write and delete seams as the
// other mutating commands. Directory moves are deliberately unsupported: the
// VFS has no atomic tree-write operation, so emulating one would expose a
// partially copied tree when a write is denied or held for approval.
//
// Usage: mv <source> <destination>
func CmdMv(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	var operands []string
	flagsDone := false
	for _, arg := range args {
		if !flagsDone && arg == "--" {
			flagsDone = true
			continue
		}
		if !flagsDone && len(arg) > 1 && arg[0] == '-' {
			fmt.Fprintf(errW, "mv: unknown option %q\n", arg)
			return 1
		}
		operands = append(operands, arg)
	}
	if len(operands) < 2 {
		fmt.Fprintln(errW, "mv: missing destination file operand")
		return 1
	}
	if len(operands) > 2 {
		fmt.Fprintln(errW, "mv: multiple sources are not supported")
		return 1
	}

	source, destination := operands[0], operands[1]
	resolvedSource := ctx.Resolve(source)
	info, err := ctx.FS().Stat(resolvedSource)
	if err != nil {
		fmt.Fprintf(errW, "mv: cannot stat %s: No such file or directory\n", source)
		return 1
	}
	if info.Dir {
		fmt.Fprintf(errW, "mv: %s: directory moves are not supported\n", source)
		return 1
	}

	if destinationInfo, statErr := ctx.FS().Stat(ctx.Resolve(destination)); statErr == nil && destinationInfo.Dir {
		destination = path.Join(destination, path.Base(resolvedSource))
	}
	resolvedDestination := ctx.Resolve(destination)
	if canonicalizer, ok := ctx.FS().(vfs.PathCanonicalizer); ok {
		resolvedSource = canonicalizer.CanonicalPath(resolvedSource)
		resolvedDestination = canonicalizer.CanonicalPath(resolvedDestination)
	}
	if resolvedSource == resolvedDestination {
		return 0
	}

	data, err := ctx.FS().ReadFile(resolvedSource)
	if err != nil {
		fmt.Fprintf(errW, "mv: cannot read %s: %s\n", source, err)
		return 1
	}
	if _, err = WriteFile(ctx, destination, data, false); err != nil {
		return writeResultMsg(errW, "mv", destination, err)
	}

	wfs, ok := ctx.FS().(vfs.WritableFS)
	if !ok {
		fmt.Fprintf(errW, "mv: %s: read-only filesystem\n", source)
		return 1
	}
	hash := sha256.Sum256(data)
	snapshot := &vfs.TreeSnapshot{
		Root: resolvedSource,
		Ops: []vfs.TreeOp{{
			RelPath: ".",
			Kind:    "file",
			Hash:    hex.EncodeToString(hash[:]),
			Size:    int64(len(data)),
		}},
	}
	err = wfs.RemoveAll(resolvedSource, vfs.RemoveOpts{Expected: snapshot})
	if err == nil {
		return 0
	}
	var pending *vfs.PendingChangeError
	if errors.As(err, &pending) {
		fmt.Fprintln(w, pendingChangeLine("mv", source, pending))
		return 0
	}
	var stale *vfs.TreeStaleError
	if errors.As(err, &stale) {
		fmt.Fprintf(errW, "mv: %s: source changed concurrently; destination was written, source was not removed\n", source)
		return 1
	}
	if errors.Is(err, vfs.ErrReadOnly) {
		fmt.Fprintf(errW, "mv: %s: read-only filesystem\n", source)
	} else {
		fmt.Fprintf(errW, "mv: cannot remove %s: %s\n", source, err)
	}
	return 1
}
