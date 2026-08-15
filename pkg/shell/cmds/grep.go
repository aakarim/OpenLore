package cmds

import (
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

func CmdGrep(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	caseInsensitive := false
	lineNumbers := false
	recursive := false
	onlyMatching := false
	noFilename := false
	countOnly := false
	invertMatch := false
	filesWithMatches := false
	var pattern string
	var targets []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--") {
			ReportUnsupportedFlag(ctx, "grep", a)
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 && !strings.HasPrefix(a, "--") {
			for _, ch := range a[1:] {
				switch ch {
				case 'i':
					caseInsensitive = true
				case 'n':
					lineNumbers = true
				case 'r', 'R':
					recursive = true
				case 'o':
					onlyMatching = true
				case 'h':
					noFilename = true
				case 'c':
					countOnly = true
				case 'v':
					invertMatch = true
				case 'l':
					filesWithMatches = true
				default:
					ReportUnsupportedFlag(ctx, "grep", "-"+string(ch))
				}
			}
		} else if pattern == "" {
			pattern = a
		} else {
			targets = append(targets, a)
		}
	}

	if pattern == "" {
		fmt.Fprintln(errW, "grep: missing pattern")
		return 1
	}

	rePattern := pattern
	if caseInsensitive {
		rePattern = "(?i)" + rePattern
	}
	re, reErr := regexp.Compile(rePattern)
	if reErr != nil {
		// Fall back to literal match
		re = regexp.MustCompile(regexp.QuoteMeta(pattern))
		if caseInsensitive {
			re = regexp.MustCompile("(?i)" + regexp.QuoteMeta(pattern))
		}
	}

	found := false

	grepLines := func(lines []string, filePath string, showFile bool) {
		matchCount := 0
		for i, line := range lines {
			matched := re.MatchString(line)
			if invertMatch {
				matched = !matched
			}
			if !matched {
				continue
			}
			found = true
			matchCount++

			if filesWithMatches {
				if filePath != "" {
					fmt.Fprintln(w, filePath)
				}
				return
			}
			if countOnly {
				continue
			}

			var prefix string
			if showFile && !noFilename && filePath != "" {
				prefix = filePath + ":"
			}
			if lineNumbers {
				prefix += fmt.Sprintf("%d:", i+1)
			}

			if onlyMatching && !invertMatch {
				matches := re.FindAllString(line, -1)
				for _, m := range matches {
					fmt.Fprintf(w, "%s%s\n", prefix, m)
				}
			} else {
				fmt.Fprintf(w, "%s%s\n", prefix, line)
			}
		}
		if countOnly {
			if showFile && !noFilename && filePath != "" {
				fmt.Fprintf(w, "%s:%d\n", filePath, matchCount)
			} else {
				fmt.Fprintf(w, "%d\n", matchCount)
			}
		}
	}

	// Read from stdin if no targets and stdin is available (pipe)
	if len(targets) == 0 && stdin != nil {
		data, _ := io.ReadAll(stdin)
		lines := strings.Split(string(data), "\n")
		grepLines(lines, "", false)
		if !found {
			return 1
		}
		return 0
	}

	if len(targets) == 0 {
		targets = []string{ctx.Cwd()}
		recursive = true
	}

	multiFile := len(targets) > 1 || recursive

	grepFile := func(filePath string) {
		content, err := ctx.FS().ReadFile(filePath)
		if err != nil {
			return
		}
		lines := strings.Split(string(content), "\n")
		grepLines(lines, filePath, multiFile)
	}

	for _, target := range targets {
		p := ctx.Resolve(target)
		f, err := ctx.FS().Stat(p)
		if err != nil {
			fmt.Fprintf(errW, "grep: %s: No such file or directory\n", target)
			continue
		}
		if f.Dir {
			if !recursive {
				fmt.Fprintf(errW, "grep: %s: Is a directory\n", target)
				continue
			}
			vfs.WalkDir(ctx.FS(), p, func(walkPath string, info *vfs.FileInfo, err error) error {
				if err != nil || info.Dir {
					return nil
				}
				grepFile(walkPath)
				return nil
			})
		} else {
			grepFile(p)
		}
	}

	if !found {
		return 1
	}
	return 0
}
