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
	extendedRegexp := false
	fixedStrings := false
	var pattern string
	var targets []string

	for i := 0; i < len(args); i++ {
		a := args[i]
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
				case 'E':
					extendedRegexp = true
					fixedStrings = false
				case 'F':
					fixedStrings = true
					extendedRegexp = false
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
	if fixedStrings {
		rePattern = regexp.QuoteMeta(pattern)
	} else if !extendedRegexp {
		rePattern = basicRegexpToRE2(pattern)
	}
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
	matchedFiles := 0
	matchedLines := 0

	grepLines := func(lines []string, filePath string, showFile bool) []int {
		matchCount := 0
		listedFile := false
		var lineHits []int
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
			lineHits = append(lineHits, i+1)

			if filesWithMatches {
				if filePath != "" && !listedFile {
					fmt.Fprintln(w, filePath)
					listedFile = true
				}
				continue
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
		return lineHits
	}

	// Read from stdin if no targets and stdin is available (pipe)
	if len(targets) == 0 && stdin != nil {
		data, _ := io.ReadAll(stdin)
		lines := strings.Split(string(data), "\n")
		lineHits := grepLines(lines, "", false)
		emitSearchMetric(ctx, pattern, []string{ctx.Cwd()}, 0, len(lineHits), len(lineHits) > 0)
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
		lineHits := grepLines(lines, filePath, multiFile)
		if len(lineHits) == 0 {
			return
		}
		matchedFiles++
		matchedLines += len(lineHits)
		emitDocLineMetrics(ctx, "doc.hit", filePath, content, lineHits)
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
	emitSearchMetric(ctx, pattern, scopePaths(ctx, targets), matchedFiles, matchedLines, found)

	if !found {
		return 1
	}
	return 0
}

func basicRegexpToRE2(pattern string) string {
	var translated strings.Builder
	inBracket := false
	bracketCanNegate := false
	bracketHasChar := false

	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		if ch == '\\' && i+1 < len(pattern) {
			if !inBracket && strings.ContainsRune("(){}|+?", rune(pattern[i+1])) {
				i++
				translated.WriteByte(pattern[i])
				continue
			}
			translated.WriteByte(ch)
			i++
			translated.WriteByte(pattern[i])
			if inBracket {
				bracketCanNegate = false
				bracketHasChar = true
			}
			continue
		}
		if ch == '[' && !inBracket {
			inBracket = true
			bracketCanNegate = true
			bracketHasChar = false
			translated.WriteByte(ch)
			continue
		}
		if inBracket {
			translated.WriteByte(ch)
			if bracketCanNegate && ch == '^' {
				bracketCanNegate = false
				continue
			}
			bracketCanNegate = false
			if ch == ']' && bracketHasChar {
				inBracket = false
			} else {
				bracketHasChar = true
			}
			continue
		}
		caretIsAnchor := ch == '^' && (i == 0 || i >= 2 && pattern[i-2] == '\\' && strings.ContainsRune("(|", rune(pattern[i-1])))
		dollarIsAnchor := ch == '$' && (i == len(pattern)-1 || i+2 < len(pattern) && pattern[i+1] == '\\' && strings.ContainsRune(")|", rune(pattern[i+2])))
		if ch == '^' && !caretIsAnchor || ch == '$' && !dollarIsAnchor {
			translated.WriteByte('\\')
		}
		if strings.ContainsRune("(){}|+?", rune(ch)) {
			translated.WriteByte('\\')
		}
		translated.WriteByte(ch)
	}

	return translated.String()
}
