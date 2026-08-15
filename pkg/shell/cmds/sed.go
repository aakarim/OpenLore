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

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-n":
			quiet = true
		case "-i", "--in-place":
			inPlace = true
		case "-e":
			if i+1 < len(args) {
				expressions = append(expressions, args[i+1])
				i++
			}
		default:
			if len(expressions) == 0 && !strings.HasPrefix(args[i], "-") && len(files) == 0 {
				expressions = append(expressions, args[i])
			} else {
				files = append(files, args[i])
			}
		}
	}

	if len(expressions) == 0 {
		fmt.Fprintln(errW, "sed: missing expression")
		return 1
	}

	cmds := parseSedCommands(expressions)

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
			orig, rerr := ctx.FS().ReadFile(ctx.Resolve(f))
			if rerr != nil {
				fmt.Fprintf(errW, "sed: %s: %s\n", f, rerr)
				code = 1
				continue
			}
			lines := splitLinesForSed(orig)
			var buf bytes.Buffer
			applySedCommands(cmds, lines, quiet, &buf)
			if c := WriteFileCASMsg(ctx, errW, "sed", f, buf.Bytes(), orig); c != 0 {
				code = c
			}
		}
		return code
	}

	lines, code := ReadInputLines(ctx, files, stdin, errW, "sed")
	if code != 0 {
		return code
	}

	applySedCommands(cmds, lines, quiet, w)
	return 0
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
func applySedCommands(cmds []sedCmd, lines []string, quiet bool, w io.Writer) {
	totalLines := len(lines)
	for lineNum, line := range lines {
		deleted := false
		printed := false
		for _, cmd := range cmds {
			if !sedAddressMatch(cmd, lineNum+1, totalLines, line) {
				continue
			}
			switch cmd.command {
			case 'd':
				deleted = true
			case 'p':
				fmt.Fprintln(w, line)
				printed = true
			case 's':
				var re *regexp.Regexp
				if cmd.sFlags.caseInsensitive {
					re, _ = regexp.Compile("(?i)" + cmd.pattern)
				} else {
					re, _ = regexp.Compile(cmd.pattern)
				}
				if re != nil {
					if cmd.sFlags.global {
						line = re.ReplaceAllString(line, cmd.replacement)
					} else {
						line = re.ReplaceAllStringFunc(line, func(match string) string {
							result := re.ReplaceAllString(match, cmd.replacement)
							return result
						})
						count := 0
						line2 := re.ReplaceAllStringFunc(lines[lineNum], func(match string) string {
							count++
							if count == 1 {
								return re.ReplaceAllString(match, cmd.replacement)
							}
							return match
						})
						line = line2
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

func parseSedCommands(expressions []string) []sedCmd {
	var cmds []sedCmd
	for _, expr := range expressions {
		for _, e := range strings.Split(expr, ";") {
			e = strings.TrimSpace(e)
			if e == "" {
				continue
			}
			cmd := parseSedExpr(e)
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}

func parseSedExpr(expr string) sedCmd {
	var cmd sedCmd
	i := 0

	// Parse address
	if i < len(expr) && expr[i] == '/' {
		end := strings.Index(expr[i+1:], "/")
		if end >= 0 {
			cmd.addrRegex = expr[i+1 : i+1+end]
			i = i + 1 + end + 1
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
			end := strings.Index(expr[i+1:], "/")
			if end >= 0 {
				cmd.addrEndRegex = expr[i+1 : i+1+end]
				i = i + 1 + end + 1
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

	if i >= len(expr) {
		return cmd
	}

	switch expr[i] {
	case 's':
		cmd.command = 's'
		if i+1 < len(expr) {
			delim := expr[i+1]
			parts := splitSedSubst(expr[i+2:], delim)
			if len(parts) >= 2 {
				cmd.pattern = parts[0]
				cmd.replacement = parts[1]
			}
			if len(parts) >= 3 {
				for _, f := range parts[2] {
					switch f {
					case 'g':
						cmd.sFlags.global = true
					case 'i', 'I':
						cmd.sFlags.caseInsensitive = true
					}
				}
			}
		}
	case 'd':
		cmd.command = 'd'
	case 'p':
		cmd.command = 'p'
	}

	return cmd
}

func splitSedSubst(s string, delim byte) []string {
	var parts []string
	var cur strings.Builder
	escaped := false
	for i := 0; i < len(s); i++ {
		if escaped {
			cur.WriteByte(s[i])
			escaped = false
			continue
		}
		if s[i] == '\\' {
			escaped = true
			cur.WriteByte(s[i])
			continue
		}
		if s[i] == delim {
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(s[i])
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}
