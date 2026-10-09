package cmds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type lsOptions struct {
	long      bool
	recursive bool
	bySize    bool
	byTime    bool
	classify  bool
	human     bool
	dirSelf   bool
	stats     bool
	json      bool
}

func CmdLs(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	var o lsOptions
	var targets []string
	flagsDone := false
	for _, a := range args {
		if flagsDone || a == "-" || len(a) < 2 || a[0] != '-' {
			targets = append(targets, a)
			continue
		}
		switch a {
		case "--":
			flagsDone = true
			continue
		case "--stats":
			o.stats = true
			continue
		case "--json":
			o.json = true
			continue
		}
		if a[1] == '-' {
			return lsUnsupportedFlag(errW, a)
		}
		for _, c := range a[1:] {
			switch c {
			case 'l':
				o.long = true
			case 'a', '1':
				// All entries are always listed, one per line.
			case 'R':
				o.recursive = true
			case 'S':
				o.bySize = true
			case 't':
				o.byTime = true
			case 'F':
				o.classify = true
			case 'h':
				o.human = true
			case 'd':
				o.dirSelf = true
			default:
				return lsUnsupportedFlag(errW, "-"+string(c))
			}
		}
	}

	if len(targets) == 0 {
		targets = []string{ctx.Cwd()}
	}

	l := &lsRun{ctx: ctx, o: o, w: w, errW: errW, facts: analyticsFacts(ctx)}
	type operand struct {
		target, path string
		f            *vfs.FileInfo
	}
	var operands []operand
	for _, target := range targets {
		p := ctx.Resolve(target)
		f, err := ctx.FS().Stat(p)
		if err != nil {
			fmt.Fprintf(errW, "ls: %s: No such file or directory\n", target)
			l.exitCode = 1
			continue
		}
		operands = append(operands, operand{target, p, f})
	}
	if o.bySize || o.byTime {
		sort.SliceStable(operands, func(i, j int) bool {
			return o.less(operands[i].f, operands[j].f, operands[i].target, operands[j].target)
		})
	}
	for _, op := range operands {
		if !op.f.Dir || o.dirSelf {
			name := op.f.Name()
			if op.f.Dir {
				name = op.target
			}
			l.printEntry(op.f, op.path, name)
			l.printed = true
			continue
		}
		l.listDir(op.path, op.target, len(targets) > 1 || o.recursive)
	}
	return l.exitCode
}

// less orders entries for -S (largest first) or -t (newest first), breaking
// ties by name.
func (o lsOptions) less(a, b *vfs.FileInfo, aName, bName string) bool {
	switch {
	case o.bySize && a.FileSize != b.FileSize:
		return a.FileSize > b.FileSize
	case !o.bySize && o.byTime && !a.FileModTime.Equal(b.FileModTime):
		return a.FileModTime.After(b.FileModTime)
	}
	return aName < bName
}

func lsUnsupportedFlag(errW io.Writer, flag string) int {
	fmt.Fprintf(errW, "ls: unsupported flag %s (supported: -l -a -R -S -t -F -1 -h -d). Use `find <path>` or `tree <path>` for other listings.\n", flag)
	return 2
}

type lsRun struct {
	ctx      CmdContext
	o        lsOptions
	w, errW  io.Writer
	facts    analytics.ContentFacts
	printed  bool
	exitCode int
}

func (l *lsRun) listDir(p, display string, header bool) {
	entries, err := l.ctx.FS().ReadDir(p)
	if err != nil {
		fmt.Fprintf(l.errW, "ls: %s: %s\n", display, err)
		l.exitCode = 1
		return
	}
	if l.o.bySize || l.o.byTime {
		sort.SliceStable(entries, func(i, j int) bool {
			return l.o.less(&entries[i], &entries[j], entries[i].FileName, entries[j].FileName)
		})
	}

	// JSON output is a stream of records, so it has no headers or separators.
	if header && !(l.o.json && l.facts != nil) {
		if l.printed {
			fmt.Fprintln(l.w)
		}
		fmt.Fprintf(l.w, "%s:\n", display)
	}
	l.printed = true
	for i := range entries {
		e := &entries[i]
		l.printEntry(e, path.Join(p, e.FileName), e.FileName)
	}
	if l.o.recursive {
		for _, e := range entries {
			if e.Dir {
				l.listDir(path.Join(p, e.FileName), path.Join(display, e.FileName), true)
			}
		}
	}
}

// printEntry prints one file or directory. full is its resolved path and name
// is how it is shown.
func (l *lsRun) printEntry(f *vfs.FileInfo, full, name string) {
	if f.Dir && (l.o.classify || !l.o.long) && !strings.HasSuffix(name, "/") {
		name += "/"
	}
	needFacts := l.facts != nil && (l.o.json || (l.o.long && l.o.stats))
	var facts *analytics.DocScalars
	if needFacts {
		fv, err := l.facts.Stat(context.Background(), full)
		if err != nil {
			fmt.Fprintf(l.errW, "ls: %s: %s\n", full, err)
			l.exitCode = 1
			return
		}
		facts = &fv
	}
	size := fmt.Sprintf("%8d", f.FileSize)
	if l.o.human {
		size = fmt.Sprintf("%8s", humanSize(f.FileSize))
	}
	switch {
	case l.o.json && facts != nil:
		_ = json.NewEncoder(l.w).Encode(*facts)
	case l.o.long && facts != nil:
		fmt.Fprintf(l.w, "%s %s %8.0f %8.0f %s\n", f.Mode(), size, facts.Scalars["lines"], facts.Scalars["tokens"], name)
	case l.o.long:
		printLongSized(l.w, f, size, name)
	default:
		fmt.Fprintln(l.w, name)
	}
}
