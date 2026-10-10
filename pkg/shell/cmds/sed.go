package cmds

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

func CmdSed(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	quiet := false
	inPlace := false
	var expressions []string
	var files []string
	flagsDone := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !flagsDone {
			switch arg {
			case "--":
				flagsDone = true
				continue
			case "-n":
				quiet = true
				continue
			case "-i", "--in-place":
				inPlace = true
				continue
			case "-e":
				if i+1 < len(args) {
					expressions = append(expressions, args[i+1])
					i++
				}
				continue
			}
			if len(arg) > 1 && strings.HasPrefix(arg, "-") {
				fmt.Fprintf(errW, "sed: unknown option %q\n", arg)
				return 1
			}
		}
		if len(expressions) == 0 && len(files) == 0 {
			expressions = append(expressions, arg)
		} else {
			files = append(files, arg)
		}
	}

	if len(expressions) == 0 {
		fmt.Fprintln(errW, "sed: missing expression")
		return 1
	}

	cmds, err := parseSedCommands(expressions)
	if err != nil {
		fmt.Fprintf(errW, "sed: %s\n", err)
		return 1
	}

	// In-place: process each file separately, buffering the full transformed
	// output and committing it as one atomic write (no in-place streaming).
	if inPlace {
		if len(files) == 0 {
			fmt.Fprintln(errW, "sed: -i requires a file argument")
			return 1
		}
		code := 0
		for _, f := range files {
			// Read the raw base bytes so the commit can compare-and-swap
			// against exactly what we transformed (true CAS under hash policy).
			resolved := ctx.Resolve(f)
			orig, rerr := ctx.FS().ReadFile(resolved)
			if rerr != nil {
				fmt.Fprintf(errW, "sed: %s: %s\n", f, rerr)
				code = 1
				continue
			}
			lines := splitLinesForSed(orig)
			var buf bytes.Buffer
			applySedCommands(cmds, lines, quiet, &buf, nil)
			if c := WriteFileCASMsg(ctx, errW, "sed", f, buf.Bytes(), orig); c != 0 {
				code = c
			}
		}
		return code
	}

	lines, metricFiles, code := readSedInput(ctx, files, stdin, errW)
	if code != 0 {
		return code
	}

	var printedLines []int
	var recordPrint func(int)
	if quiet && metricsEnabled(ctx) {
		recordPrint = func(line int) { printedLines = append(printedLines, line) }
	}
	applySedCommands(cmds, lines, quiet, w, recordPrint)
	emitSedReads(ctx, metricFiles, quiet, printedLines)
	return 0
}

type sedMetricFile struct {
	path       string
	content    []byte
	lineOffset int
	lineCount  int
}

func readSedInput(ctx CmdContext, files []string, stdin io.Reader, errW io.Writer) ([]string, []sedMetricFile, int) {
	if len(files) == 0 || !metricsEnabled(ctx) {
		lines, code := ReadInputLines(ctx, files, stdin, errW, "sed")
		return lines, nil, code
	}
	var lines []string
	var tracked []sedMetricFile
	for _, file := range files {
		resolved := ctx.Resolve(file)
		content, err := ctx.FS().ReadFile(resolved)
		if err != nil {
			fmt.Fprintf(errW, "sed: %s: %s\n", file, err)
			return nil, nil, 1
		}
		fileLines := splitLinesForInput(content)
		tracked = append(tracked, sedMetricFile{path: resolved, content: content, lineOffset: len(lines), lineCount: len(fileLines)})
		lines = append(lines, fileLines...)
	}
	return lines, tracked, 0
}

func splitLinesForInput(content []byte) []string {
	text := string(content)
	if strings.HasSuffix(text, "\n") {
		text = text[:len(text)-1]
	}
	return strings.Split(text, "\n")
}

func emitSedReads(ctx CmdContext, files []sedMetricFile, quiet bool, printedLines []int) {
	for _, file := range files {
		if !quiet {
			emitDocMetric(ctx, "doc.read", file.path, file.content, fullLineRange(file.content))
			continue
		}
		var selected []int
		seen := make([]bool, file.lineCount)
		for _, globalLine := range printedLines {
			local := globalLine - file.lineOffset - 1
			if local >= 0 && local < file.lineCount && !seen[local] {
				selected = append(selected, local+1)
				seen[local] = true
			}
		}
		emitDocLineMetrics(ctx, "doc.read", file.path, file.content, selected)
	}
}

// splitLinesForSed splits raw file bytes into lines using the same convention
// as ReadInputLines: a single trailing newline is dropped, and empty content
// yields no lines.
func splitLinesForSed(content []byte) []string {
	text := string(content)
	if strings.HasSuffix(text, "\n") {
		text = text[:len(text)-1]
	}
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// applySedCommands runs the parsed sed commands over lines, writing the result
// to w. It is shared by streaming and in-place (`-i`) modes.
func applySedCommands(cmds []sedCmd, lines []string, quiet bool, w io.Writer, onPrint func(int)) {
	totalLines := len(lines)
	for lineNum, line := range lines {
		deleted := false
		printed := false
		var appendText []string
		for _, cmd := range cmds {
			if !sedAddressMatch(cmd, lineNum+1, totalLines, line) {
				continue
			}
			switch cmd.command {
			case 'a':
				appendText = append(appendText, cmd.text)
			case 'd':
				deleted = true
			case 'p':
				fmt.Fprintln(w, line)
				printed = true
				if onPrint != nil {
					onPrint(lineNum + 1)
				}
			case 's':
				var re *regexp.Regexp
				pattern := basicRegexpToRE2(cmd.pattern)
				replacement := sedReplacementToRE2(cmd.replacement, cmd.delimiter)
				if cmd.sFlags.caseInsensitive {
					re, _ = regexp.Compile("(?i)" + pattern)
				} else {
					re, _ = regexp.Compile(pattern)
				}
				if re != nil {
					if cmd.sFlags.global {
						line = re.ReplaceAllString(line, replacement)
					} else {
						loc := re.FindStringSubmatchIndex(line)
						if loc != nil {
							expanded := re.ExpandString(nil, replacement, line, loc)
							line = line[:loc[0]] + string(expanded) + line[loc[1]:]
						}
					}
				}
			}
			if deleted {
				break
			}
		}
		if !deleted {
			if !quiet || printed {
				if !printed {
					fmt.Fprintln(w, line)
				}
			}
		}
		for _, text := range appendText {
			fmt.Fprintln(w, text)
		}
		_ = printed
	}
}

type sedCmd struct {
	addrStart    int
	addrEnd      int
	addrRegex    string
	addrEndRegex string
	command      byte
	pattern      string
	replacement  string
	delimiter    byte
	text         string
	sFlags       struct {
		global          bool
		caseInsensitive bool
	}
}

func sedAddressMatch(cmd sedCmd, lineNum, totalLines int, line string) bool {
	if cmd.addrStart == 0 && cmd.addrEnd == 0 && cmd.addrRegex == "" && cmd.addrEndRegex == "" {
		return true
	}
	start := cmd.addrStart
	if start == -1 {
		start = totalLines
	}
	end := cmd.addrEnd
	if end == -1 {
		end = totalLines
	}

	if cmd.addrRegex != "" {
		re, err := regexp.Compile(cmd.addrRegex)
		if err != nil {
			return false
		}
		if !re.MatchString(line) {
			return false
		}
		return true
	}

	if end > 0 {
		return lineNum >= start && lineNum <= end
	}
	if start > 0 {
		return lineNum == start
	}
	return true
}

func parseSedCommands(expressions []string) ([]sedCmd, error) {
	var cmds []sedCmd
	for _, expr := range expressions {
		for {
			expr = strings.TrimSpace(expr)
			if expr == "" {
				break
			}

			separator := indexSedCommandSeparator(expr)
			if separator < 0 {
				cmd, err := parseSedExpr(expr)
				if err != nil {
					return nil, err
				}
				cmds = append(cmds, cmd)
				break
			}

			prefix := strings.TrimSpace(expr[:separator])
			if prefix == "" {
				expr = expr[separator+1:]
				continue
			}
			cmd, err := parseSedExpr(prefix)
			if err != nil {
				return nil, err
			}
			if cmd.command == 'a' {
				// Append text consumes the rest of this expression. Semicolons
				// in Markdown or code snippets are payload, not separators.
				cmd, err = parseSedExpr(expr)
				if err != nil {
					return nil, err
				}
				cmds = append(cmds, cmd)
				break
			}
			cmds = append(cmds, cmd)
			expr = expr[separator+1:]
		}
	}
	return cmds, nil
}

// indexSedCommandSeparator returns the first semicolon separating sed
// commands. Semicolons within an address, substitution pattern, or
// substitution replacement belong to that delimited value.
func indexSedCommandSeparator(expr string) int {
	i := 0
	if i < len(expr) && expr[i] == '/' {
		end := indexUnescapedSedDelimiter(expr, i+1, '/')
		if end < 0 {
			return -1
		}
		i = end + 1
	} else if i < len(expr) && expr[i] == '$' {
		i++
	} else {
		for i < len(expr) && expr[i] >= '0' && expr[i] <= '9' {
			i++
		}
	}
	if i < len(expr) && expr[i] == ',' {
		i++
		if i < len(expr) && expr[i] == '/' {
			end := indexUnescapedSedDelimiter(expr, i+1, '/')
			if end < 0 {
				return -1
			}
			i = end + 1
		} else if i < len(expr) && expr[i] == '$' {
			i++
		} else {
			for i < len(expr) && expr[i] >= '0' && expr[i] <= '9' {
				i++
			}
		}
	}

	for i < len(expr) && (expr[i] == ' ' || expr[i] == '\t') {
		i++
	}
	if i < len(expr) && expr[i] == 's' && i+1 < len(expr) {
		delim := expr[i+1]
		end := indexUnescapedSedDelimiter(expr, i+2, delim)
		if end < 0 {
			return -1
		}
		end = indexUnescapedSedDelimiter(expr, end+1, delim)
		if end < 0 {
			return -1
		}
		i = end + 1
	}
	if separator := strings.IndexByte(expr[i:], ';'); separator >= 0 {
		return i + separator
	}
	return -1
}

func indexUnescapedSedDelimiter(expr string, start int, delim byte) int {
	escaped := false
	for i := start; i < len(expr); i++ {
		if escaped {
			escaped = false
			continue
		}
		if expr[i] == '\\' {
			escaped = true
			continue
		}
		if expr[i] == delim {
			return i
		}
	}
	return -1
}

// parseSedExpr parses one sed command. Commands and substitution flags that
// are not implemented are reported as errors rather than silently ignored, so
// callers never see exit 0 for an edit that was not made.
func parseSedExpr(expr string) (sedCmd, error) {
	var cmd sedCmd
	i := 0

	// Parse address
	if i < len(expr) && expr[i] == '/' {
		end := indexUnescapedSedDelimiter(expr, i+1, '/')
		if end >= 0 {
			cmd.addrRegex = strings.ReplaceAll(expr[i+1:end], `\/`, "/")
			i = end + 1
		}
	} else if i < len(expr) && expr[i] == '$' {
		cmd.addrStart = -1
		i++
	} else if i < len(expr) && expr[i] >= '0' && expr[i] <= '9' {
		j := i
		for j < len(expr) && expr[j] >= '0' && expr[j] <= '9' {
			j++
		}
		cmd.addrStart, _ = strconv.Atoi(expr[i:j])
		i = j
	}

	// Range
	if i < len(expr) && expr[i] == ',' {
		i++
		if i < len(expr) && expr[i] == '$' {
			cmd.addrEnd = -1
			i++
		} else if i < len(expr) && expr[i] == '/' {
			end := indexUnescapedSedDelimiter(expr, i+1, '/')
			if end >= 0 {
				cmd.addrEndRegex = strings.ReplaceAll(expr[i+1:end], `\/`, "/")
				i = end + 1
			}
		} else {
			j := i
			for j < len(expr) && expr[j] >= '0' && expr[j] <= '9' {
				j++
			}
			cmd.addrEnd, _ = strconv.Atoi(expr[i:j])
			i = j
		}
	}

	for i < len(expr) && (expr[i] == ' ' || expr[i] == '\t') {
		i++
	}
	if i >= len(expr) {
		return cmd, fmt.Errorf("missing command in expression '%s'", expr)
	}

	switch expr[i] {
	case 'a':
		cmd.command = 'a'
		cmd.text = expr[i+1:]
		if strings.HasPrefix(cmd.text, "\\") {
			cmd.text = strings.TrimPrefix(cmd.text, "\\")
			cmd.text = strings.TrimPrefix(cmd.text, "\r\n")
			cmd.text = strings.TrimPrefix(cmd.text, "\n")
		} else {
			cmd.text = strings.TrimLeft(cmd.text, " \t")
		}
	case 's':
		cmd.command = 's'
		patternEnd := -1
		if i+1 < len(expr) {
			cmd.delimiter = expr[i+1]
			patternEnd = indexUnescapedSedDelimiter(expr, i+2, cmd.delimiter)
		}
		if patternEnd < 0 {
			return cmd, fmt.Errorf("unterminated 's' command in expression '%s'", expr)
		}
		cmd.pattern = expr[i+2 : patternEnd]
		// A missing closing delimiter is tolerated: the replacement runs to
		// the end of the expression and there are no flags.
		replacementEnd := indexUnescapedSedDelimiter(expr, patternEnd+1, cmd.delimiter)
		if replacementEnd < 0 {
			cmd.replacement = expr[patternEnd+1:]
			break
		}
		cmd.replacement = expr[patternEnd+1 : replacementEnd]
		// Every byte after the closing delimiter is a flag, including any
		// further delimiters, so nothing is silently dropped.
		for _, f := range expr[replacementEnd+1:] {
			switch f {
			case 'g':
				cmd.sFlags.global = true
			case 'i', 'I':
				cmd.sFlags.caseInsensitive = true
			default:
				return cmd, fmt.Errorf("unknown option to 's': %q in expression '%s' (supported: g, i, I)", f, expr)
			}
		}
	case 'd', 'p':
		cmd.command = expr[i]
		if strings.TrimSpace(expr[i+1:]) != "" {
			return cmd, fmt.Errorf("extra characters after command '%c' in expression '%s'", expr[i], expr)
		}
	default:
		return cmd, fmt.Errorf("unsupported command %q in expression '%s' (supported: s, a, d, p)", expr[i], expr)
	}

	return cmd, nil
}

func sedReplacementToRE2(replacement string, delimiter byte) string {
	var translated strings.Builder
	for i := 0; i < len(replacement); i++ {
		switch replacement[i] {
		case '\\':
			if i+1 >= len(replacement) {
				translated.WriteByte('\\')
				continue
			}
			next := replacement[i+1]
			switch {
			case next >= '1' && next <= '9':
				fmt.Fprintf(&translated, "${%c}", next)
				i++
			case next == 'n' || next == '\n':
				translated.WriteByte('\n')
				i++
			case next == 't':
				translated.WriteByte('\t')
				i++
			case next == '&' || next == '\\' || next == '/' || next == delimiter:
				translated.WriteByte(next)
				i++
			default:
				translated.WriteByte('\\')
			}
		case '&':
			translated.WriteString("${0}")
		case '$':
			translated.WriteString("$$")
		default:
			translated.WriteByte(replacement[i])
		}
	}
	return translated.String()
}
