package cmds

import (
	"fmt"
	"io"
	"strings"
)

func CmdWc(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	showLines := false
	showWords := false
	showBytes := false
	showChars := false
	var files []string

	for _, a := range args {
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			for _, ch := range a[1:] {
				switch ch {
				case 'l':
					showLines = true
				case 'w':
					showWords = true
				case 'c':
					showBytes = true
				case 'm':
					showChars = true
				}
			}
		} else {
			files = append(files, a)
		}
	}

	// Default: show all three
	showAll := !showLines && !showWords && !showBytes && !showChars

	printCounts := func(data []byte, name string) {
		lines := strings.Count(string(data), "\n")
		words := len(strings.Fields(string(data)))
		b := len(data)
		chars := len([]rune(string(data)))
		var parts []string
		if showAll || showLines {
			parts = append(parts, fmt.Sprintf("%d", lines))
		}
		if showAll || showWords {
			parts = append(parts, fmt.Sprintf("%d", words))
		}
		if showAll || showBytes {
			parts = append(parts, fmt.Sprintf("%d", b))
		}
		if showChars {
			parts = append(parts, fmt.Sprintf("%d", chars))
		}
		if name != "" {
			parts = append(parts, name)
		}
		fmt.Fprintf(w, " %s\n", strings.Join(parts, " "))
	}

	if len(files) == 0 {
		if stdin != nil {
			data, _ := io.ReadAll(stdin)
			printCounts(data, "")
			return 0
		}
		fmt.Fprintln(errW, "wc: missing file operand")
		return 1
	}

	var totalLines, totalWords, totalBytes, totalChars int
	for _, a := range files {
		p := ctx.Resolve(a)
		content, err := ctx.FS().ReadFile(p)
		if err != nil {
			fmt.Fprintf(errW, "wc: %s: %s\n", a, err)
			return 1
		}
		totalLines += strings.Count(string(content), "\n")
		totalWords += len(strings.Fields(string(content)))
		totalBytes += len(content)
		totalChars += len([]rune(string(content)))
		printCounts(content, a)
	}
	if len(files) > 1 {
		var parts []string
		if showAll || showLines {
			parts = append(parts, fmt.Sprintf("%d", totalLines))
		}
		if showAll || showWords {
			parts = append(parts, fmt.Sprintf("%d", totalWords))
		}
		if showAll || showBytes {
			parts = append(parts, fmt.Sprintf("%d", totalBytes))
		}
		if showChars {
			parts = append(parts, fmt.Sprintf("%d", totalChars))
		}
		parts = append(parts, "total")
		fmt.Fprintf(w, " %s\n", strings.Join(parts, " "))
	}
	return 0
}
