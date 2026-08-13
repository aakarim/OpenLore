package cmds

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

// RipgrepOptions is the deliberately small behavior surface of Ripgrep.
// The implementation is intentionally naive: it collects and sorts every
// path, reads each complete file, splits it into lines, and searches files
// sequentially. The recursive-search experiment asks agents to make this
// faster without changing its observable behavior.
type RipgrepOptions struct {
	CaseInsensitive  bool
	LineNumbers      bool
	FilesWithMatches bool
}

// RipgrepMatch is one matching line. In FilesWithMatches mode, LineNumber and
// Line identify the first match but callers should render only Path.
type RipgrepMatch struct {
	Path       string
	LineNumber int
	Line       string
}

// Ripgrep recursively searches roots in fsys. Results are ordered by path and
// then line number, independent of filesystem traversal order.
func Ripgrep(fsys vfs.FileSystem, roots []string, pattern string, opts RipgrepOptions) ([]RipgrepMatch, error) {
	if opts.CaseInsensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}

	seenRoots := make(map[string]struct{}, len(roots))
	var files []string
	for _, root := range roots {
		root = vfs.CleanPath(root)
		if _, ok := seenRoots[root]; ok {
			continue
		}
		seenRoots[root] = struct{}{}

		info, err := fsys.Stat(root)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", root, err)
		}
		if !info.Dir {
			files = append(files, root)
			continue
		}
		if err := vfs.WalkDir(fsys, root, func(path string, info *vfs.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.Dir {
				files = append(files, path)
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("%s: %w", root, err)
		}
	}
	sort.Strings(files)

	var matches []RipgrepMatch
	for _, file := range files {
		data, err := fsys.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if len(data) == 0 {
			continue
		}
		lines := strings.Split(string(data), "\n")
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		for i, line := range lines {
			line = strings.TrimSuffix(line, "\r")
			if !re.MatchString(line) {
				continue
			}
			matches = append(matches, RipgrepMatch{Path: file, LineNumber: i + 1, Line: line})
			if opts.FilesWithMatches {
				break
			}
		}
	}
	return matches, nil
}

// CmdRg implements a focused recursive search command:
//
//	rg [-i] [-n] [-l] PATTERN [PATH ...]
func CmdRg(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	var opts RipgrepOptions
	var pattern string
	var targets []string
	options := true

	for _, arg := range args {
		if options && arg == "--" {
			options = false
			continue
		}
		if options && strings.HasPrefix(arg, "-") && arg != "-" {
			for _, flag := range arg[1:] {
				switch flag {
				case 'i':
					opts.CaseInsensitive = true
				case 'n':
					opts.LineNumbers = true
				case 'l':
					opts.FilesWithMatches = true
				default:
					fmt.Fprintf(errW, "rg: unknown option: -%c\n", flag)
					return 2
				}
			}
			continue
		}
		options = false
		if pattern == "" {
			pattern = arg
		} else {
			targets = append(targets, ctx.Resolve(arg))
		}
	}

	if pattern == "" {
		fmt.Fprintln(errW, "rg: missing pattern")
		return 2
	}
	if len(targets) == 0 {
		targets = []string{ctx.Cwd()}
	}

	matches, err := Ripgrep(ctx.FS(), targets, pattern, opts)
	if err != nil {
		fmt.Fprintf(errW, "rg: %v\n", err)
		return 2
	}
	for _, match := range matches {
		if opts.FilesWithMatches {
			fmt.Fprintln(w, match.Path)
		} else if opts.LineNumbers {
			fmt.Fprintf(w, "%s:%d:%s\n", match.Path, match.LineNumber, match.Line)
		} else {
			fmt.Fprintf(w, "%s:%s\n", match.Path, match.Line)
		}
	}
	if len(matches) == 0 {
		return 1
	}
	return 0
}
