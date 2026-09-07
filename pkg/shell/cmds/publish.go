package cmds

import (
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

// defaultMaxPublishSize is used when a target has MaxFileSize <= 0.
const defaultMaxPublishSize int64 = 2_621_440 // 2.5 MiB

// PublishBaseURL is the base URL prefixed to a published path in the command's
// success output. Populated by the server at startup.
var PublishBaseURL string

// CmdPublish writes stdin to a file inside a publish inbox. The commit goes
// through the session filesystem (`WriteFile`), so it inherits the same
// per-identity write scoping, atomic CAS, and approval gating as every other
// write verb — there is no longer a separate direct-to-disk path. The session's
// publish inboxes come from the host via CmdContext.PublishTargets and are used
// here only for the usage listing and per-docset size cap; the filesystem is
// the authority on whether the write is actually permitted.
func CmdPublish(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	writable := ctx.PublishTargets()

	if len(writable) == 0 {
		fmt.Fprintln(errW, "No writable paths available to you.")
		return 1
	}

	if len(args) == 0 {
		fmt.Fprintln(w, "Usage: publish <path> < content")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Writable paths:")
		for _, t := range writable {
			fmt.Fprintf(w, "  /%s/\n", t.Name)
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Example: echo '# Title' | publish /knowledge/topic.md")
		return 0
	}

	// Parse /<docset>/<file...>.
	p := path.Clean("/" + strings.TrimPrefix(args[0], "/"))
	segs := strings.SplitN(strings.TrimPrefix(p, "/"), "/", 2)
	docset := segs[0]

	// The docset must be one of the session's writable targets.
	var target PublishTarget
	found := false
	for _, t := range writable {
		if t.Name == docset {
			target = t
			found = true
			break
		}
	}
	if !found {
		fmt.Fprintf(errW, "publish: no access to /%s/\n", docset)
		return 1
	}

	if len(segs) < 2 || segs[1] == "" {
		fmt.Fprintln(errW, "publish: path must include a file name after the docset")
		fmt.Fprintf(errW, "  e.g. publish /%s/my-file.md\n", docset)
		return 1
	}

	// Route the publish into the docset's inbox: `publish /<docset>/<rest>`
	// writes to <inbox>/<rest>, never to the docset root. The inbox is the only
	// place the publish grant may create files, so the destination must be
	// resolved here rather than writing the raw /<docset>/<rest> path.
	dest := path.Join(target.InboxPath, segs[1])

	maxSize := target.MaxFileSize
	if maxSize <= 0 {
		maxSize = defaultMaxPublishSize
	}

	// Buffer the whole object (bounded), then commit once through the VFS.
	data, err := io.ReadAll(io.LimitReader(stdin, maxSize+1))
	if err != nil {
		fmt.Fprintf(errW, "publish: %s\n", err)
		return 1
	}
	if int64(len(data)) > maxSize {
		fmt.Fprintf(errW, "publish: content exceeds maximum size of %d bytes\n", maxSize)
		return 1
	}

	if _, err := WriteFile(ctx, dest, data, false); err != nil {
		if writeRuleRejection(errW, err) {
			return 1
		}
		var pchg *vfs.PendingChangeError
		if errors.As(err, &pchg) {
			// Not a failure: a middleware parked the publish as a pending change.
			fmt.Fprintln(w, pendingChangeLine("publish", dest, pchg))
			return 0
		}
		var pce *vfs.PreconditionError
		if errors.As(err, &pce) {
			fmt.Fprintf(errW, "publish: %s changed concurrently — re-read and retry\n", dest)
			return 1
		}
		if errors.Is(err, vfs.ErrReadOnly) {
			fmt.Fprintf(errW, "publish: no access to %s\n", dest)
		} else {
			fmt.Fprintf(errW, "publish: %s\n", err)
		}
		return 1
	}

	fmt.Fprintf(w, "Published %s (%d bytes)\n", dest, len(data))
	if PublishBaseURL != "" {
		fmt.Fprintf(w, "%s%s\n", PublishBaseURL, dest)
	}
	return 0
}
