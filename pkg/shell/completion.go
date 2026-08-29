package shell

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aakarim/go-openlore/pkg/shell/cmds"
	"github.com/mattn/go-runewidth"
)

type completionCandidate struct {
	value   string
	display string
}

type completionResult struct {
	line       string
	candidates []completionCandidate
	finished   bool
}

type completionToken struct {
	kind       byte // w=word, c=command separator, r=redirect
	start, end int
	value      string
	quote      byte
}

// complete returns the edited line and all matching candidates. Completion is
// intentionally limited to the end of the line: the interactive shell does not
// otherwise expose cursor movement.
func (s *Shell) complete(line string) completionResult {
	tokens := scanCompletionTokens(line)
	current := completionToken{kind: 'w', start: len(line), end: len(line)}
	previous := tokens
	if len(tokens) > 0 && tokens[len(tokens)-1].kind == 'w' && tokens[len(tokens)-1].end == len(line) {
		current = tokens[len(tokens)-1]
		previous = tokens[:len(tokens)-1]
	}

	segmentStart := 0
	for i, token := range previous {
		if token.kind == 'c' {
			segmentStart = i + 1
		}
	}
	segment := previous[segmentStart:]
	command, args, redirectTarget := completionPosition(segment)

	var candidates []completionCandidate
	switch {
	case redirectTarget:
		candidates = s.pathCandidates(current.value, current.quote)
	case command == "":
		candidates = s.commandCandidates(current.value)
	case command == "lore" && len(args) == 0:
		candidates = loreSubCandidates(current.value)
	default:
		candidates = s.pathCandidates(current.value, current.quote)
	}
	if len(candidates) == 0 {
		return completionResult{line: line}
	}

	prefix := candidates[0].value
	for _, candidate := range candidates[1:] {
		prefix = commonPrefix(prefix, candidate.value)
	}
	prefix = printablePrefix(prefix)
	unique := len(candidates) == 1 && prefix == candidates[0].value
	replacement := quoteCompletion(prefix, current.quote)
	if unique && !strings.HasSuffix(prefix, "/") {
		replacement += " "
	}
	return completionResult{
		line:       line[:current.start] + replacement,
		candidates: candidates,
		finished:   unique,
	}
}

func (s *Shell) commandCandidates(prefix string) []completionCandidate {
	names := append(cmds.Names(), "pwd", "exit", "quit")
	seen := make(map[string]bool, len(names))
	var candidates []completionCandidate
	for _, name := range names {
		if seen[name] || !strings.HasPrefix(name, prefix) || !s.ActionAllowed(cmds.ActionFor(name)) {
			continue
		}
		seen[name] = true
		candidates = append(candidates, completionCandidate{value: name, display: name})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].value < candidates[j].value })
	return candidates
}

func loreSubCandidates(prefix string) []completionCandidate {
	var candidates []completionCandidate
	for _, name := range cmds.LoreSubNames() {
		if strings.HasPrefix(name, prefix) {
			candidates = append(candidates, completionCandidate{value: name, display: name})
		}
	}
	return candidates
}

func (s *Shell) pathCandidates(prefix string, quote byte) []completionCandidate {
	typedDir, base := splitPathPrefix(prefix)
	lookupDir := typedDir
	if quote == 0 && (lookupDir == "~" || strings.HasPrefix(lookupDir, "~/")) {
		lookupDir = s.expandTilde(lookupDir)
	}
	if lookupDir == "" {
		lookupDir = s.cwd
	} else {
		lookupDir = s.Resolve(lookupDir)
	}
	entries, err := s.fs.ReadDir(lookupDir)
	if err != nil {
		return nil
	}
	var candidates []completionCandidate
	for _, entry := range entries {
		if strings.HasPrefix(entry.FileName, ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		if !strings.HasPrefix(entry.FileName, base) {
			continue
		}
		value := pathPrefixJoin(typedDir, entry.FileName)
		display := escapeDisplay(entry.FileName)
		if entry.Dir {
			value += "/"
			display += "/"
		}
		candidates = append(candidates, completionCandidate{value: value, display: display})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].value < candidates[j].value })
	return candidates
}

func splitPathPrefix(prefix string) (dir, base string) {
	i := strings.LastIndexByte(prefix, '/')
	if i < 0 {
		return "", prefix
	}
	if i == 0 {
		return "/", prefix[1:]
	}
	return prefix[:i], prefix[i+1:]
}

func pathPrefixJoin(dir, name string) string {
	switch dir {
	case "":
		return name
	case "/":
		return "/" + name
	default:
		return dir + "/" + name
	}
}

func completionPosition(tokens []completionToken) (command string, args []string, redirectTarget bool) {
	expectRedirect := false
	for _, token := range tokens {
		switch token.kind {
		case 'r':
			expectRedirect = true
		case 'w':
			if expectRedirect {
				expectRedirect = false
				continue
			}
			if command == "" && isAssignment(token.value) {
				continue
			}
			if command == "" {
				command = token.value
			} else {
				args = append(args, token.value)
			}
		}
	}
	return command, args, expectRedirect
}

func isAssignment(word string) bool {
	i := strings.IndexByte(word, '=')
	if i <= 0 {
		return false
	}
	for j, r := range word[:i] {
		if !(r == '_' || unicode.IsLetter(r) || (j > 0 && unicode.IsDigit(r))) {
			return false
		}
	}
	return true
}

func scanCompletionTokens(line string) []completionToken {
	var tokens []completionToken
	for i := 0; i < len(line); {
		if line[i] == ' ' || line[i] == '\t' {
			i++
			continue
		}
		start := i
		if line[i] == ';' || line[i] == '|' || line[i] == '&' {
			i++
			if i < len(line) && line[i] == line[start] {
				i++
			} else if line[start] == '|' && i < len(line) && line[i] == '&' {
				i++
			}
			tokens = append(tokens, completionToken{kind: 'c', start: start, end: i})
			continue
		}
		if line[i] == '>' || (line[i] == '2' && i+1 < len(line) && line[i+1] == '>') {
			if line[i] == '2' {
				i++
			}
			i++
			if i < len(line) && line[i] == '>' {
				i++
			}
			tokens = append(tokens, completionToken{kind: 'r', start: start, end: i})
			continue
		}
		token := completionToken{kind: 'w', start: start}
		var value strings.Builder
		var quote byte
		for i < len(line) {
			ch := line[i]
			if quote == 0 && (ch == ' ' || ch == '\t' || ch == ';' || ch == '|' || ch == '&' || ch == '>') {
				break
			}
			if ch == '\\' && quote != '\'' && i+1 < len(line) {
				value.WriteByte(line[i+1])
				i += 2
				continue
			}
			if ch == '\'' || ch == '"' {
				if quote == 0 {
					quote = ch
					if i == start {
						token.quote = ch
					}
					i++
					continue
				}
				if quote == ch {
					quote = 0
					i++
					continue
				}
			}
			value.WriteByte(ch)
			i++
		}
		token.end = i
		token.value = value.String()
		tokens = append(tokens, token)
	}
	return tokens
}

func commonPrefix(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	for i > 0 && i < len(a) && !utf8.RuneStart(a[i]) {
		i--
	}
	return a[:i]
}

func quoteCompletion(value string, preferred byte) string {
	switch preferred {
	case '\'':
		return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
	case '"':
		replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "$", "\\$", "`", "\\`")
		return "\"" + replacer.Replace(value) + "\""
	}
	var result strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("/_-.~", r) {
			result.WriteRune(r)
			continue
		}
		result.WriteByte('\\')
		result.WriteRune(r)
	}
	return result.String()
}

func escapeDisplay(value string) string {
	var result strings.Builder
	for _, r := range value {
		switch r {
		case '\n':
			result.WriteString(`\n`)
		case '\r':
			result.WriteString(`\r`)
		case '\t':
			result.WriteString(`\t`)
		default:
			if unicode.IsControl(r) {
				fmt.Fprintf(&result, `\x%02x`, r)
			} else {
				result.WriteRune(r)
			}
		}
	}
	return result.String()
}

// printablePrefix prevents a filename discovered from the server-side VFS
// from injecting control bytes into the user's terminal. Such entries remain
// visible in the escaped candidate listing but are not inserted automatically.
func printablePrefix(value string) string {
	for i, r := range value {
		if unicode.IsControl(r) {
			return value[:i]
		}
	}
	return value
}

func formatCandidateColumns(candidates []completionCandidate, width int) string {
	if width <= 0 {
		width = 80
	}
	maxWidth := 0
	for _, candidate := range candidates {
		if w := runewidth.StringWidth(candidate.display); w > maxWidth {
			maxWidth = w
		}
	}
	columnWidth := maxWidth + 2
	columns := width / columnWidth
	if columns < 1 {
		columns = 1
	}
	rows := (len(candidates) + columns - 1) / columns
	var result strings.Builder
	for row := 0; row < rows; row++ {
		for col := 0; col < columns; col++ {
			i := col*rows + row
			if i >= len(candidates) {
				continue
			}
			text := candidates[i].display
			result.WriteString(text)
			if col+1 < columns && i+rows < len(candidates) {
				result.WriteString(strings.Repeat(" ", columnWidth-runewidth.StringWidth(text)))
			}
		}
		result.WriteString("\r\n")
	}
	return result.String()
}
