package cmds

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

// CmdPatch applies a unified diff (read from stdin) to a target file and
// commits the result as a single atomic whole-object write. The patch's own
// context/removed lines are the precondition: if the file has drifted from the
// diff's base, the hunk fails to apply and patch reports a conflict (exit 1),
// committing nothing — exactly like git applying onto a stale base.
//
// Usage: patch <file> < changes.diff
func CmdPatch(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	var target string
	for _, a := range args {
		switch {
		case a == "-i":
			ReportUnsupportedFlag(ctx, "patch", a)
		case strings.HasPrefix(a, "-p"), strings.HasPrefix(a, "--"):
			ReportUnsupportedFlag(ctx, "patch", a)
		case strings.HasPrefix(a, "-"):
			ReportUnsupportedFlag(ctx, "patch", a)
		default:
			if target == "" {
				target = a
			}
		}
	}
	if target == "" {
		fmt.Fprintln(errW, "patch: usage: patch <file> < changes.diff")
		return 1
	}
	if stdin == nil {
		fmt.Fprintln(errW, "patch: no diff on stdin")
		return 1
	}

	diffData, _ := io.ReadAll(stdin)
	hunks, err := parseUnifiedDiff(string(diffData))
	if err != nil {
		fmt.Fprintf(errW, "patch: %s\n", err)
		return 1
	}
	if len(hunks) == 0 {
		fmt.Fprintln(errW, "patch: empty diff (no hunks)")
		return 1
	}

	resolved := ctx.Resolve(target)
	orig, readErr := ctx.FS().ReadFile(resolved)
	exists := readErr == nil

	newContent, applyErr := applyUnifiedDiff(orig, hunks)
	if applyErr != nil {
		fmt.Fprintf(errW, "patch: %s: %s\n", target, applyErr)
		fmt.Fprintln(errW, "patch: file has drifted from the diff base — re-read and regenerate the diff")
		return 1
	}

	wfs, ok := ctx.FS().(vfs.WritableFS)
	if !ok {
		fmt.Fprintf(errW, "patch: %s: read-only filesystem\n", target)
		return 1
	}

	var opts vfs.WriteOpts
	if exists {
		h := sha256.Sum256(orig)
		hexStr := hex.EncodeToString(h[:])
		opts.IfMatch = &hexStr
	} else {
		opts.IfNoneMatch = true
	}

	if _, err := wfs.WriteFileAtomic(resolved, newContent, opts); err != nil {
		var pchg *vfs.PendingChangeError
		if errors.As(err, &pchg) {
			// Not a failure: a middleware parked the patch as a pending change.
			fmt.Fprintln(errW, pendingChangeLine("patch", target, pchg))
			return 0
		}
		var pe *vfs.PreconditionError
		if errors.As(err, &pe) {
			fmt.Fprintf(errW, "patch: %s: file changed concurrently — re-read and retry\n", target)
			return 1
		}
		if errors.Is(err, vfs.ErrReadOnly) {
			fmt.Fprintf(errW, "patch: %s: read-only filesystem\n", target)
			return 1
		}
		fmt.Fprintf(errW, "patch: %s: %s\n", target, err)
		return 1
	}

	fmt.Fprintf(w, "patched %s\n", target)
	return 0
}

// diffHunk is one @@ ... @@ block of a unified diff.
type diffHunk struct {
	oldStart int      // 1-based line in the original
	lines    []string // body lines, each prefixed by ' ', '-', or '+'
}

// parseUnifiedDiff extracts hunks from a unified diff, ignoring file headers.
func parseUnifiedDiff(diff string) ([]diffHunk, error) {
	var hunks []diffHunk
	var cur *diffHunk
	sc := bufio.NewScanner(strings.NewReader(diff))
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
			// file headers — ignore
			continue
		case strings.HasPrefix(line, "@@"):
			if cur != nil {
				hunks = append(hunks, *cur)
			}
			oldStart, err := parseHunkHeader(line)
			if err != nil {
				return nil, err
			}
			cur = &diffHunk{oldStart: oldStart}
		default:
			if cur == nil {
				// preamble before the first hunk — skip
				continue
			}
			if line == "" {
				// a blank line in the diff = an empty context line
				cur.lines = append(cur.lines, " ")
				continue
			}
			switch line[0] {
			case ' ', '+', '-':
				cur.lines = append(cur.lines, line)
			case '\\':
				// "\ No newline at end of file" — ignore
			default:
				// unexpected; ignore stray lines
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if cur != nil {
		hunks = append(hunks, *cur)
	}
	return hunks, nil
}

// parseHunkHeader parses the old-side start line from "@@ -l,s +l,s @@".
func parseHunkHeader(line string) (int, error) {
	// e.g. "@@ -12,7 +12,6 @@ optional context"
	fields := strings.Fields(line)
	for _, f := range fields {
		if strings.HasPrefix(f, "-") {
			spec := strings.TrimPrefix(f, "-")
			if i := strings.IndexByte(spec, ','); i >= 0 {
				spec = spec[:i]
			}
			n, err := strconv.Atoi(spec)
			if err != nil {
				return 0, fmt.Errorf("bad hunk header: %q", line)
			}
			return n, nil
		}
	}
	return 0, fmt.Errorf("bad hunk header: %q", line)
}

// applyUnifiedDiff applies hunks to orig, verifying every context and removed
// line against the original (no fuzz). A mismatch returns an error (conflict).
func applyUnifiedDiff(orig []byte, hunks []diffHunk) ([]byte, error) {
	hadTrailingNewline := len(orig) > 0 && orig[len(orig)-1] == '\n'
	var origLines []string
	if len(orig) == 0 {
		origLines = nil
	} else {
		body := string(orig)
		if hadTrailingNewline {
			body = body[:len(body)-1]
		}
		origLines = strings.Split(body, "\n")
	}

	var result []string
	cursor := 0 // index into origLines

	for _, h := range hunks {
		start := h.oldStart - 1
		if start < 0 {
			start = 0
		}
		// Copy unchanged lines preceding the hunk.
		if start > len(origLines) {
			return nil, fmt.Errorf("hunk starts past end of file (line %d)", h.oldStart)
		}
		for cursor < start {
			result = append(result, origLines[cursor])
			cursor++
		}
		for _, bl := range h.lines {
			op, text := bl[0], bl[1:]
			switch op {
			case ' ':
				if cursor >= len(origLines) || origLines[cursor] != text {
					return nil, fmt.Errorf("context mismatch at line %d", cursor+1)
				}
				result = append(result, text)
				cursor++
			case '-':
				if cursor >= len(origLines) || origLines[cursor] != text {
					return nil, fmt.Errorf("removed line mismatch at line %d", cursor+1)
				}
				cursor++
			case '+':
				result = append(result, text)
			}
		}
	}
	// Copy the remainder of the file.
	for cursor < len(origLines) {
		result = append(result, origLines[cursor])
		cursor++
	}

	out := strings.Join(result, "\n")
	if hadTrailingNewline || (len(orig) == 0 && len(result) > 0) {
		out += "\n"
	}
	return []byte(out), nil
}
