package cmds

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/aakarim/go-openlore/pkg/rules"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// maxAppendRetries bounds the compare-and-swap loop for atomic appends.
const maxAppendRetries = 64

// overwritePreconditions resolves the WriteOpts for a blind whole-file
// overwrite (`>`, tee, publish) to resolved. Under PolicyLastWriteWins it is
// unconditional. Under PolicyHash it is compare-and-swap; the base it swaps
// against is, in order of preference:
//
//  1. the session's last-read (or last-written) hash for this path, if the
//     filesystem tracks reads (vfs.ReadTracker) — so an overwrite fails if the
//     file changed since the caller last saw it, without the caller naming a
//     hash;
//  2. otherwise the current content read at command time — a narrower guard
//     covering only the command's own read→write window (and create-only when
//     the target does not yet exist).
func overwritePreconditions(ctx CmdContext, resolved string) vfs.WriteOpts {
	if ctx.WriteConflictPolicy(resolved) == vfs.PolicyLastWriteWins {
		return vfs.WriteOpts{}
	}
	if rt, ok := ctx.FS().(vfs.ReadTracker); ok {
		if h, seen := rt.LastReadHash(resolved); seen {
			hh := h
			return vfs.WriteOpts{IfMatch: &hh}
		}
	}
	// No tracked read of this path: fall back to a command-time base.
	cur, readErr := ctx.FS().ReadFile(resolved)
	return overwriteOpts(ctx, resolved, cur, readErr == nil)
}

// overwriteOpts builds the precondition for a whole-file overwrite to resolved
// under the session's write-conflict policy. base is the content the caller
// treats as the write's base; baseExists is whether that base is a real prior
// version (false => create-only). Under PolicyLastWriteWins the write is
// unconditional; under PolicyHash it is compare-and-swap against base.
func overwriteOpts(ctx CmdContext, resolved string, base []byte, baseExists bool) vfs.WriteOpts {
	if ctx.WriteConflictPolicy(resolved) == vfs.PolicyLastWriteWins {
		return vfs.WriteOpts{}
	}
	if !baseExists {
		return vfs.WriteOpts{IfNoneMatch: true}
	}
	h := sha256.Sum256(base)
	hexStr := hex.EncodeToString(h[:])
	return vfs.WriteOpts{IfMatch: &hexStr}
}

// WriteFile commits data to path through the writable filesystem as a single
// atomic whole-object operation. It is the one write seam shared by every
// blind write verb (`>`, `>>`, tee, publish): there is no streaming.
//
// When appendMode is false it performs a whole-file overwrite governed by the
// session's write-conflict policy (see overwritePreconditions): under "hash"
// (the default) it is a compare-and-swap against the session's last-read hash
// for the path (or, if none was tracked, the content read at command time), so a
// concurrent change since the caller last saw the file surfaces a
// vfs.PreconditionError; under "last_write_wins" it is an unconditional atomic
// overwrite. When appendMode is true it runs a read-modify-write CAS loop so
// concurrent appends never clobber each other, regardless of policy.
//
// A read-modify-write verb that already holds the base it transformed (sed -i)
// should call WriteFileCAS instead, so the precondition is the true base rather
// than a re-read.
//
// It returns vfs.ErrReadOnly when the filesystem is read-only (the hard error
// surfaced to the agent), so callers can detect and message it.
func WriteFile(ctx CmdContext, p string, data []byte, appendMode bool) (string, error) {
	wfs, ok := ctx.FS().(vfs.WritableFS)
	if !ok {
		return "", vfs.ErrReadOnly
	}
	resolved := ctx.Resolve(p)

	if !appendMode {
		return wfs.WriteFileAtomic(resolved, data, overwritePreconditions(ctx, resolved))
	}

	// Atomic append: read current content, append, commit-if-unchanged, retry.
	for attempt := 0; attempt < maxAppendRetries; attempt++ {
		cur, readErr := ctx.FS().ReadFile(resolved)
		exists := readErr == nil

		var opts vfs.WriteOpts
		if exists {
			h := sha256.Sum256(cur)
			hexStr := hex.EncodeToString(h[:])
			opts.IfMatch = &hexStr
		} else {
			opts.IfNoneMatch = true
		}

		combined := make([]byte, 0, len(cur)+len(data))
		combined = append(combined, cur...)
		combined = append(combined, data...)

		newHash, werr := wfs.WriteFileAtomic(resolved, combined, opts)
		if werr == nil {
			return newHash, nil
		}
		var pe *vfs.PreconditionError
		if errors.As(werr, &pe) {
			continue // raced with another writer; re-read and retry
		}
		return "", werr
	}
	return "", fmt.Errorf("append: too much write contention on %s", p)
}

// WriteFileCAS commits a whole-file overwrite where the caller already holds
// the base content it transformed (a read-modify-write verb such as sed -i).
// Under "hash" policy the write is a true compare-and-swap against base, so a
// concurrent change since base was read surfaces a vfs.PreconditionError rather
// than silently clobbering it; under "last_write_wins" it is unconditional.
func WriteFileCAS(ctx CmdContext, p string, data []byte, base []byte) (string, error) {
	wfs, ok := ctx.FS().(vfs.WritableFS)
	if !ok {
		return "", vfs.ErrReadOnly
	}
	resolved := ctx.Resolve(p)
	return wfs.WriteFileAtomic(resolved, data, overwriteOpts(ctx, resolved, base, true))
}

// WriteFileMsg writes data and emits a uniform error line to errW on failure,
// returning a shell-style exit code (0 ok, 1 error). It centralizes the
// read-only, precondition, and generic error messaging for the write verbs.
func WriteFileMsg(ctx CmdContext, errW io.Writer, cmdName, p string, data []byte, appendMode bool) int {
	_, err := WriteFile(ctx, p, data, appendMode)
	return writeResultMsg(errW, cmdName, p, err)
}

// WriteFileCASMsg is WriteFileMsg for the compare-and-swap (known-base) seam.
func WriteFileCASMsg(ctx CmdContext, errW io.Writer, cmdName, p string, data []byte, base []byte) int {
	_, err := WriteFileCAS(ctx, p, data, base)
	return writeResultMsg(errW, cmdName, p, err)
}

// pendingChangeLine renders the informational (exit-0) line for a mutation a
// middleware parked as a pending change. Ref is the consumer-owned handle used
// to resolve it later; it may be empty.
func pendingChangeLine(cmdName, target string, pce *vfs.PendingChangeError) string {
	if pce.Ref != "" {
		return fmt.Sprintf("%s: %s change pending as %s", cmdName, target, pce.Ref)
	}
	return fmt.Sprintf("%s: %s change pending", cmdName, target)
}

// writeResultMsg renders the shared exit code + message for a write result.
func writeResultMsg(errW io.Writer, cmdName, p string, err error) int {
	if err == nil {
		return 0
	}
	if writeRuleRejection(errW, err) {
		return 1
	}
	var pce *vfs.PendingChangeError
	if errors.As(err, &pce) {
		// Not a failure: a middleware parked the write as a pending change.
		fmt.Fprintln(errW, pendingChangeLine(cmdName, p, pce))
		return 0
	}
	var pe *vfs.PreconditionError
	if errors.As(err, &pe) {
		fmt.Fprintf(errW, "%s: %s: file changed concurrently — re-read and retry\n", cmdName, p)
		return 1
	}
	if errors.Is(err, vfs.ErrReadOnly) {
		fmt.Fprintf(errW, "%s: %s: read-only filesystem\n", cmdName, p)
	} else {
		fmt.Fprintf(errW, "%s: %s: %s\n", cmdName, p, err)
	}
	return 1
}

func writeRuleRejection(errW io.Writer, err error) bool {
	var rejection *rules.Rejection
	if !errors.As(err, &rejection) {
		return false
	}
	fmt.Fprintln(errW, rejection.Error())
	return true
}
