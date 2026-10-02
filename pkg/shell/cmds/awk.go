package cmds

import (
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func CmdAwk(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	fieldSep := ""
	var vars []string
	var program string
	var files []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-F":
			if i+1 < len(args) {
				fieldSep = args[i+1]
				i++
			}
		case "-v":
			if i+1 < len(args) {
				vars = append(vars, args[i+1])
				i++
			}
		default:
			if program == "" {
				program = args[i]
			} else {
				files = append(files, args[i])
			}
		}
	}

	if program == "" {
		fmt.Fprintln(errW, "awk: missing program")
		return 1
	}

	lines, code := ReadInputLines(ctx, files, stdin, errW, "awk")
	if code != 0 && code != -1 {
		return code
	}

	awk := newAwkInterpreter(program, fieldSep, vars, w, errW)
	return awk.run(lines)
}

// awkInterpreter implements a basic awk interpreter.
type awkInterpreter struct {
	rules    []awkRule
	fs       string
	ofs      string
	rs       string
	ors      string
	nr       int
	nf       int
	fields   []string
	line     string
	vars     map[string]string
	arrays   map[string]map[string]string
	w        io.Writer
	errW     io.Writer
	exitCode int
	// err holds the first unsupported construct or runtime error. Once set,
	// execution stops and awk exits 2 instead of printing wrong output.
	err     string
	regexps map[string]*regexp.Regexp
}

func (a *awkInterpreter) fail(format string, args ...any) {
	if a.err == "" {
		a.err = fmt.Sprintf(format, args...)
	}
}

type awkRule struct {
	pattern string // "BEGIN", "END", regex, expression, or empty (match all)
	action  string
	isBegin bool
	isEnd   bool
}

func newAwkInterpreter(program, fieldSep string, varDefs []string, w io.Writer, errW io.Writer) *awkInterpreter {
	awk := &awkInterpreter{
		fs:     " ",
		ofs:    " ",
		rs:     "\n",
		ors:    "\n",
		vars:   make(map[string]string),
		arrays: make(map[string]map[string]string),
		w:      w,
		errW:   errW,
	}

	if fieldSep != "" {
		awk.fs = fieldSep
	}

	for _, v := range varDefs {
		if idx := strings.Index(v, "="); idx >= 0 {
			awk.vars[v[:idx]] = v[idx+1:]
		}
	}

	awk.vars["FS"] = awk.fs
	awk.vars["OFS"] = awk.ofs
	awk.vars["RS"] = awk.rs
	awk.vars["ORS"] = awk.ors

	awk.rules = parseAwkProgram(program)
	return awk
}

func parseAwkProgram(prog string) []awkRule {
	var rules []awkRule
	prog = strings.TrimSpace(prog)

	for len(prog) > 0 {
		prog = strings.TrimSpace(prog)
		if len(prog) == 0 {
			break
		}

		var rule awkRule

		if strings.HasPrefix(prog, "BEGIN") {
			rest := strings.TrimSpace(prog[5:])
			if len(rest) > 0 && rest[0] == '{' {
				rule.isBegin = true
				rule.pattern = "BEGIN"
				action, remaining := extractBlock(rest)
				rule.action = action
				prog = remaining
				rules = append(rules, rule)
				continue
			}
		}

		if strings.HasPrefix(prog, "END") {
			rest := strings.TrimSpace(prog[3:])
			if len(rest) > 0 && rest[0] == '{' {
				rule.isEnd = true
				rule.pattern = "END"
				action, remaining := extractBlock(rest)
				rule.action = action
				prog = remaining
				rules = append(rules, rule)
				continue
			}
		}

		// Pattern { action }, { action }, or a pattern alone. The pattern
		// may be a regex literal, an expression, or both (/re/ && !done);
		// it is evaluated by evalExpr, so the action brace is located
		// outside string and regex literals.
		patEnd := awkIndexUnquoted(prog, '{')
		if patEnd >= 0 {
			rule.pattern = strings.TrimSpace(prog[:patEnd])
			action, remaining := extractBlock(prog[patEnd:])
			rule.action = action
			prog = remaining
			rules = append(rules, rule)
			continue
		}

		// Just a pattern with implicit print
		rule.pattern = prog
		rule.action = "print"
		rules = append(rules, rule)
		break
	}

	return rules
}

func extractBlock(s string) (string, string) {
	if len(s) == 0 || s[0] != '{' {
		return "", s
	}
	mask := awkLiteralMask(s)
	depth := 0
	for i := 0; i < len(s); i++ {
		if mask[i] {
			continue
		}
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[1:i], strings.TrimSpace(s[i+1:])
			}
		}
	}
	return s[1:], ""
}

func (a *awkInterpreter) run(lines []string) int {
	// Run BEGIN rules
	for _, rule := range a.rules {
		if rule.isBegin {
			a.execAction(rule.action)
			if a.err != "" {
				return a.reportError()
			}
		}
	}

	// Process lines
	for _, line := range lines {
		a.nr++
		a.line = line
		a.splitFields(line)

		for _, rule := range a.rules {
			if rule.isBegin || rule.isEnd {
				continue
			}
			matched := a.matchPattern(rule.pattern)
			if a.err == "" && matched {
				a.execAction(rule.action)
			}
			if a.err != "" {
				return a.reportError()
			}
		}
	}

	// Run END rules
	for _, rule := range a.rules {
		if rule.isEnd {
			a.execAction(rule.action)
			if a.err != "" {
				return a.reportError()
			}
		}
	}

	return a.exitCode
}

func (a *awkInterpreter) reportError() int {
	fmt.Fprintf(a.errW, "awk: %s\n", a.err)
	return 2
}

func (a *awkInterpreter) splitFields(line string) {
	fs := a.vars["FS"]
	if fs == " " {
		a.fields = strings.Fields(line)
	} else {
		a.fields = strings.Split(line, fs)
	}
	a.nf = len(a.fields)
	a.vars["NR"] = strconv.Itoa(a.nr)
	a.vars["NF"] = strconv.Itoa(a.nf)
}

func (a *awkInterpreter) matchPattern(pattern string) bool {
	if pattern == "" {
		return true
	}
	// A regex literal pattern (/re/) evaluates to a match against $0, so
	// regex, expression, and combined patterns all go through evalExpr.
	return awkTruthy(a.evalExpr(pattern))
}

func awkTruthy(val string) bool {
	if val == "" || val == "0" {
		return false
	}
	return true
}

func (a *awkInterpreter) execAction(action string) {
	action = strings.TrimSpace(action)
	if action == "" {
		return
	}

	stmts := splitAwkStatements(action)
	for i := 0; i < len(stmts); i++ {
		stmt := stmts[i]
		// Rejoin "if (c) stmt; else stmt" which the splitter separates.
		for awkStartsWithWord(stmt, "if") && i+1 < len(stmts) && awkStartsWithWord(stmts[i+1], "else") {
			stmt += "; " + stmts[i+1]
			i++
		}
		a.execStatement(stmt)
		if a.err != "" {
			return
		}
	}
}

// awkStartsWithWord reports whether s begins with the keyword word, not
// merely an identifier that has word as a prefix.
func awkStartsWithWord(s, word string) bool {
	if !strings.HasPrefix(s, word) {
		return false
	}
	if len(s) == len(word) {
		return true
	}
	c := s[len(word)]
	return !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9')
}

func splitAwkStatements(action string) []string {
	var stmts []string
	mask := awkLiteralMask(action)
	depth := 0
	start := 0
	flush := func(end int) {
		if s := strings.TrimSpace(action[start:end]); s != "" {
			stmts = append(stmts, s)
		}
		start = end + 1
	}

	for i := 0; i < len(action); i++ {
		if mask[i] {
			continue
		}
		switch action[i] {
		case '{', '(':
			depth++
		case '}', ')':
			depth--
		case ';', '\n':
			if depth == 0 {
				flush(i)
			}
		}
	}
	flush(len(action))
	return stmts
}

func (a *awkInterpreter) execStatement(stmt string) {
	stmt = strings.TrimSpace(stmt)
	if stmt == "" {
		return
	}

	// Handle if/else
	if awkStartsWithWord(stmt, "if") {
		a.execIf(stmt)
		return
	}

	// Handle while
	if awkStartsWithWord(stmt, "while") {
		a.execWhile(stmt)
		return
	}

	// Handle for
	if awkStartsWithWord(stmt, "for") {
		a.execFor(stmt)
		return
	}

	// Brace block used as a statement
	if stmt[0] == '{' {
		action, _ := extractBlock(stmt)
		a.execAction(action)
		return
	}

	// print/printf
	if awkStartsWithWord(stmt, "printf") {
		a.execPrintf(stmt[6:])
		return
	}
	if awkStartsWithWord(stmt, "print") {
		a.execPrint(stmt[5:])
		return
	}

	// Assignment: lvalue = expr, lvalue += expr, ...
	if lhs, op, rhs, ok := awkFindAssign(stmt); ok {
		a.assign(lhs, op, rhs)
		return
	}

	// Increment/decrement: x++, x--, ++x, --x
	for _, op := range []string{"++", "--"} {
		lv := ""
		if strings.HasSuffix(stmt, op) {
			lv = strings.TrimSpace(stmt[:len(stmt)-2])
		} else if strings.HasPrefix(stmt, op) {
			lv = strings.TrimSpace(stmt[2:])
		}
		if lv != "" && awkIsLValue(lv) {
			delta := 1.0
			if op == "--" {
				delta = -1
			}
			a.setLValue(lv, awkFormatNum(awkNum(a.evalExpr(lv))+delta))
			return
		}
	}

	if word := awkLeadingWord(stmt); awkKeywords[word] {
		a.fail("unsupported statement: %s", stmt)
		return
	}

	// Expression statement, such as sub(...) or gsub(...).
	a.evalExpr(stmt)
}

// awkKeywords are reserved words that this interpreter does not evaluate
// as expressions. Using one where an expression is expected is an error
// rather than a silent no-op.
var awkKeywords = map[string]bool{
	"BEGIN": true, "END": true, "break": true, "continue": true, "delete": true,
	"do": true, "else": true, "exit": true, "for": true, "function": true,
	"getline": true, "if": true, "in": true, "next": true, "nextfile": true,
	"print": true, "printf": true, "return": true, "while": true,
}

func awkLeadingWord(s string) string {
	end := 0
	for end < len(s) && (s[end] == '_' || s[end] >= 'a' && s[end] <= 'z' || s[end] >= 'A' && s[end] <= 'Z' || end > 0 && s[end] >= '0' && s[end] <= '9') {
		end++
	}
	return s[:end]
}

func awkIsIdentifier(s string) bool {
	return s != "" && awkLeadingWord(s) == s
}

// awkIsLValue reports whether s is a variable, an array element, or a
// field reference.
func awkIsLValue(s string) bool {
	if len(s) > 1 && s[0] == '$' {
		return true
	}
	if b := strings.IndexByte(s, '['); b > 0 && strings.HasSuffix(s, "]") {
		return awkIsIdentifier(s[:b]) && !awkKeywords[s[:b]]
	}
	return awkIsIdentifier(s) && !awkKeywords[s]
}

// awkFindAssign splits an assignment statement into its lvalue, operator
// (=, +=, -=, *=, /=, %=, ^=), and right-hand side.
func awkFindAssign(stmt string) (string, string, string, bool) {
	mask := awkLiteralMask(stmt)
	depth := 0
	for i := 0; i < len(stmt); i++ {
		if mask[i] {
			continue
		}
		switch stmt[i] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case '=':
			if depth != 0 {
				continue
			}
			if i+1 < len(stmt) && stmt[i+1] == '=' {
				return "", "", "", false
			}
			start := i
			if i > 0 && strings.IndexByte("+-*/%^", stmt[i-1]) >= 0 {
				start = i - 1
			} else if i > 0 && strings.IndexByte("!<>", stmt[i-1]) >= 0 {
				return "", "", "", false
			}
			lhs := strings.TrimSpace(stmt[:start])
			if !awkIsLValue(lhs) {
				return "", "", "", false
			}
			return lhs, stmt[start : i+1], strings.TrimSpace(stmt[i+1:]), true
		}
	}
	return "", "", "", false
}

func (a *awkInterpreter) assign(lhs, op, rhs string) {
	val := a.evalExpr(rhs)
	if op != "=" {
		cur := awkNum(a.evalExpr(lhs))
		delta := awkNum(val)
		var result float64
		switch op {
		case "+=":
			result = cur + delta
		case "-=":
			result = cur - delta
		case "*=":
			result = cur * delta
		case "/=", "%=":
			if delta == 0 {
				a.fail("division by zero in %s", op)
				return
			}
			if op == "/=" {
				result = cur / delta
			} else {
				result = math.Mod(cur, delta)
			}
		case "^=":
			result = math.Pow(cur, delta)
		}
		val = awkFormatNum(result)
	}
	a.setLValue(lhs, val)
}

func (a *awkInterpreter) setLValue(lv, val string) {
	switch {
	case lv[0] == '$':
		a.setField(int(awkNum(a.evalExpr(lv[1:]))), val)
	case strings.HasSuffix(lv, "]"):
		b := strings.IndexByte(lv, '[')
		name := lv[:b]
		key := a.evalExpr(lv[b+1 : len(lv)-1])
		if a.arrays[name] == nil {
			a.arrays[name] = make(map[string]string)
		}
		a.arrays[name][key] = val
	default:
		a.vars[lv] = val
	}
}

func (a *awkInterpreter) setField(n int, val string) {
	if n < 0 {
		a.fail("attempt to access field %d", n)
		return
	}
	if n == 0 {
		a.line = val
		a.splitFields(val)
		return
	}
	for len(a.fields) < n {
		a.fields = append(a.fields, "")
	}
	a.fields[n-1] = val
	a.nf = len(a.fields)
	a.vars["NF"] = strconv.Itoa(a.nf)
	a.line = strings.Join(a.fields, a.vars["OFS"])
}

// awkNum converts a string to a number the way awk does: the longest
// leading numeric prefix, or 0.
func awkNum(s string) float64 {
	f, _ := strconv.ParseFloat(awkNumPrefix.FindString(strings.TrimSpace(s)), 64)
	return f
}

var awkNumPrefix = regexp.MustCompile(`^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?`)

func awkIsNumberLiteral(s string) bool {
	if s == "" || !(s[0] >= '0' && s[0] <= '9' || s[0] == '.') {
		return false
	}
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

func (a *awkInterpreter) execPrint(expr string) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		fmt.Fprintln(a.w, a.line)
		return
	}
	if awkHasRedirect(expr) {
		a.fail("output redirection is not supported: print %s", expr)
		return
	}
	if expr[0] == '(' && findMatchingParen(expr, 0) == len(expr)-1 {
		expr = expr[1 : len(expr)-1]
	}
	parts := splitAwkPrintArgs(expr)
	var vals []string
	for _, p := range parts {
		vals = append(vals, a.evalExpr(strings.TrimSpace(p)))
	}
	if a.err != "" {
		return
	}
	fmt.Fprintln(a.w, strings.Join(vals, a.vars["OFS"]))
}

// awkHasRedirect reports whether a print argument list contains an
// unparenthesised > or | output redirection.
func awkHasRedirect(expr string) bool {
	mask := awkLiteralMask(expr)
	depth := 0
	for i := 0; i < len(expr); i++ {
		if mask[i] {
			continue
		}
		switch expr[i] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case '>':
			if depth == 0 {
				return true
			}
		case '|':
			if depth == 0 {
				if i+1 < len(expr) && expr[i+1] == '|' {
					i++
					continue
				}
				return true
			}
		}
	}
	return false
}

func splitAwkPrintArgs(expr string) []string {
	var parts []string
	mask := awkLiteralMask(expr)
	depth := 0
	start := 0

	for i := 0; i < len(expr); i++ {
		if mask[i] {
			continue
		}
		switch expr[i] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, expr[start:i])
				start = i + 1
			}
		}
	}
	if start < len(expr) {
		parts = append(parts, expr[start:])
	}
	return parts
}

func (a *awkInterpreter) execPrintf(expr string) {
	expr = strings.TrimSpace(expr)
	if awkHasRedirect(expr) {
		a.fail("output redirection is not supported: printf %s", expr)
		return
	}
	if strings.HasPrefix(expr, "(") && findMatchingParen(expr, 0) == len(expr)-1 {
		expr = expr[1 : len(expr)-1]
	}
	parts := splitAwkPrintArgs(expr)
	if len(parts) == 0 {
		return
	}
	format := a.evalExpr(strings.TrimSpace(parts[0]))
	var argVals []string
	for _, p := range parts[1:] {
		argVals = append(argVals, a.evalExpr(strings.TrimSpace(p)))
	}
	if a.err != "" {
		return
	}

	result := awkSprintf(format, argVals)
	fmt.Fprint(a.w, result)
}

func awkSprintf(format string, args []string) string {
	var sb strings.Builder
	argIdx := 0
	i := 0
	for i < len(format) {
		if format[i] == '\\' && i+1 < len(format) {
			switch format[i+1] {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case '\\':
				sb.WriteByte('\\')
			case '"':
				sb.WriteByte('"')
			default:
				sb.WriteByte('\\')
				sb.WriteByte(format[i+1])
			}
			i += 2
			continue
		}
		if format[i] == '%' && i+1 < len(format) {
			j := i + 1
			// Parse flags, width, precision
			for j < len(format) && (format[j] == '-' || format[j] == '+' || format[j] == '0' || format[j] == ' ' || format[j] == '#') {
				j++
			}
			for j < len(format) && format[j] >= '0' && format[j] <= '9' {
				j++
			}
			if j < len(format) && format[j] == '.' {
				j++
				for j < len(format) && format[j] >= '0' && format[j] <= '9' {
					j++
				}
			}
			if j < len(format) {
				spec := format[i : j+1]
				ch := format[j]
				var arg string
				if argIdx < len(args) {
					arg = args[argIdx]
					argIdx++
				}
				switch ch {
				case 'd', 'i':
					n, _ := strconv.ParseFloat(arg, 64)
					sb.WriteString(fmt.Sprintf(strings.Replace(spec, string(ch), "d", 1), int64(n)))
				case 'f', 'e', 'g':
					n, _ := strconv.ParseFloat(arg, 64)
					sb.WriteString(fmt.Sprintf(spec, n))
				case 's':
					sb.WriteString(fmt.Sprintf(spec, arg))
				case 'c':
					if len(arg) > 0 {
						sb.WriteByte(arg[0])
					}
				case 'x', 'o':
					n, _ := strconv.ParseFloat(arg, 64)
					sb.WriteString(fmt.Sprintf(spec, int64(n)))
				case '%':
					sb.WriteByte('%')
					argIdx-- // no arg consumed
				default:
					sb.WriteString(spec)
				}
				i = j + 1
				continue
			}
		}
		sb.WriteByte(format[i])
		i++
	}
	return sb.String()
}

func (a *awkInterpreter) evalExpr(expr string) string {
	expr = strings.TrimSpace(expr)
	if expr == "" || a.err != "" {
		return ""
	}

	// String literal
	if expr[0] == '"' && len(expr) >= 2 && awkLiteralEnd(expr, 0) == len(expr)-1 {
		return awkUnquote(expr[1 : len(expr)-1])
	}

	// A bare regex literal matches against $0.
	if body, ok := awkRegexLiteral(expr); ok {
		re := a.compileRegex(body)
		if re != nil && re.MatchString(a.line) {
			return "1"
		}
		return "0"
	}

	// Ternary: cond ? a : b
	if qIdx, cIdx := awkFindTernary(expr); qIdx >= 0 {
		cond := a.evalExpr(expr[:qIdx])
		trueVal := a.evalExpr(expr[qIdx+1 : cIdx])
		falseVal := a.evalExpr(expr[cIdx+1:])
		if awkTruthy(cond) {
			return trueVal
		}
		return falseVal
	}

	// Logical OR: ||
	if parts := awkSplitOp(expr, "||"); len(parts) == 2 {
		l := a.evalExpr(parts[0])
		r := a.evalExpr(parts[1])
		if awkTruthy(l) || awkTruthy(r) {
			return "1"
		}
		return "0"
	}

	// Logical AND: &&
	if parts := awkSplitOp(expr, "&&"); len(parts) == 2 {
		l := a.evalExpr(parts[0])
		r := a.evalExpr(parts[1])
		if awkTruthy(l) && awkTruthy(r) {
			return "1"
		}
		return "0"
	}

	// Array membership: key in array
	if parts := awkSplitOp(expr, " in "); len(parts) == 2 && awkIsIdentifier(parts[1]) {
		if _, ok := a.arrays[parts[1]][a.evalExpr(parts[0])]; ok {
			return "1"
		}
		return "0"
	}

	// Regex match: ~ and !~
	if parts := awkSplitOp(expr, "!~"); len(parts) == 2 {
		l := a.evalExpr(parts[0])
		re := a.compileRegex(a.regexArg(parts[1]))
		if re != nil && !re.MatchString(l) {
			return "1"
		}
		return "0"
	}
	if parts := awkSplitOp(expr, "~"); len(parts) == 2 {
		l := a.evalExpr(parts[0])
		re := a.compileRegex(a.regexArg(parts[1]))
		if re != nil && re.MatchString(l) {
			return "1"
		}
		return "0"
	}

	// Comparison operators
	for _, op := range []string{"==", "!=", "<=", ">=", "<", ">"} {
		if parts := awkSplitOp(expr, op); len(parts) == 2 {
			l := a.evalExpr(parts[0])
			r := a.evalExpr(parts[1])
			lf, le := strconv.ParseFloat(l, 64)
			rf, re := strconv.ParseFloat(r, 64)
			isNum := le == nil && re == nil
			var result bool
			switch op {
			case "==":
				if isNum {
					result = lf == rf
				} else {
					result = l == r
				}
			case "!=":
				if isNum {
					result = lf != rf
				} else {
					result = l != r
				}
			case "<":
				if isNum {
					result = lf < rf
				} else {
					result = l < r
				}
			case ">":
				if isNum {
					result = lf > rf
				} else {
					result = l > r
				}
			case "<=":
				if isNum {
					result = lf <= rf
				} else {
					result = l <= r
				}
			case ">=":
				if isNum {
					result = lf >= rf
				} else {
					result = l >= r
				}
			}
			if result {
				return "1"
			}
			return "0"
		}
	}

	// String concatenation: adjacent operands such as $1 " " $2
	if parts := splitAwkConcat(expr); len(parts) > 1 {
		var sb strings.Builder
		for _, p := range parts {
			sb.WriteString(a.evalExpr(p))
		}
		return sb.String()
	}

	// Arithmetic: + -
	if parts := awkSplitArith(expr, '+'); len(parts) == 2 {
		l, _ := strconv.ParseFloat(a.evalExpr(parts[0]), 64)
		r, _ := strconv.ParseFloat(a.evalExpr(parts[1]), 64)
		return awkFormatNum(l + r)
	}
	if parts := awkSplitArith(expr, '-'); len(parts) == 2 {
		// Make sure it's not a negative number or unary minus
		left := strings.TrimSpace(parts[0])
		if left != "" {
			l, _ := strconv.ParseFloat(a.evalExpr(parts[0]), 64)
			r, _ := strconv.ParseFloat(a.evalExpr(parts[1]), 64)
			return awkFormatNum(l - r)
		}
	}

	// Arithmetic: * / %
	if parts := awkSplitArith(expr, '*'); len(parts) == 2 {
		l, _ := strconv.ParseFloat(a.evalExpr(parts[0]), 64)
		r, _ := strconv.ParseFloat(a.evalExpr(parts[1]), 64)
		return awkFormatNum(l * r)
	}
	if parts := awkSplitArith(expr, '/'); len(parts) == 2 {
		l, _ := strconv.ParseFloat(a.evalExpr(parts[0]), 64)
		r, _ := strconv.ParseFloat(a.evalExpr(parts[1]), 64)
		if r == 0 {
			return "0"
		}
		return awkFormatNum(l / r)
	}
	if parts := awkSplitArith(expr, '%'); len(parts) == 2 {
		l, _ := strconv.ParseFloat(a.evalExpr(parts[0]), 64)
		r, _ := strconv.ParseFloat(a.evalExpr(parts[1]), 64)
		if r == 0 {
			return "0"
		}
		return awkFormatNum(math.Mod(l, r))
	}

	// Power: ^
	if parts := awkSplitArith(expr, '^'); len(parts) == 2 {
		l, _ := strconv.ParseFloat(a.evalExpr(parts[0]), 64)
		r, _ := strconv.ParseFloat(a.evalExpr(parts[1]), 64)
		return awkFormatNum(math.Pow(l, r))
	}

	// Unary not: !
	if strings.HasPrefix(expr, "!") {
		val := a.evalExpr(expr[1:])
		if awkTruthy(val) {
			return "0"
		}
		return "1"
	}

	// Unary minus and plus
	if expr[0] == '-' || expr[0] == '+' {
		val := awkNum(a.evalExpr(expr[1:]))
		if expr[0] == '-' {
			val = -val
		}
		return awkFormatNum(val)
	}

	// Parenthesized expression
	if expr[0] == '(' && findMatchingParen(expr, 0) == len(expr)-1 {
		return a.evalExpr(expr[1 : len(expr)-1])
	}

	// Field reference: $0, $1, $NF, $(expr)
	if expr[0] == '$' {
		n := int(awkNum(a.evalExpr(expr[1:])))
		if n < 0 {
			a.fail("attempt to access field %d", n)
			return ""
		}
		return a.getField(n)
	}

	// Built-in functions
	if idx := strings.Index(expr, "("); idx > 0 && awkIsIdentifier(expr[:idx]) && findMatchingParen(expr, idx) == len(expr)-1 {
		return a.callFunc(expr[:idx], expr[idx+1:len(expr)-1])
	}

	// Array access: arr[key]
	if bIdx := strings.Index(expr, "["); bIdx > 0 && strings.HasSuffix(expr, "]") && awkIsIdentifier(expr[:bIdx]) {
		arrName := expr[:bIdx]
		key := expr[bIdx+1 : len(expr)-1]
		key = a.evalExpr(key)
		if arr, ok := a.arrays[arrName]; ok {
			return arr[key]
		}
		return ""
	}

	// Numeric literal. Checked before variables so that 1 is never
	// looked up as a name.
	if awkIsNumberLiteral(expr) {
		return expr
	}

	// Variable lookup
	if val, ok := a.vars[expr]; ok {
		return val
	}

	if awkIsIdentifier(expr) {
		if expr == "length" {
			return strconv.Itoa(len(a.line))
		}
		if awkKeywords[expr] {
			a.fail("unsupported keyword in expression: %s", expr)
			return ""
		}
		// Uninitialised variables are the empty string (0 in numeric context).
		return ""
	}

	a.fail("unsupported expression: %s", expr)
	return ""
}

// awkRegexLiteral returns the body of expr if expr is exactly one regex
// literal such as /^## F$/.
func awkRegexLiteral(expr string) (string, bool) {
	if len(expr) >= 2 && expr[0] == '/' && awkLiteralEnd(expr, 0) == len(expr)-1 {
		return expr[1 : len(expr)-1], true
	}
	return "", false
}

// regexArg returns the regex source for an argument that may be a regex
// literal (/re/) or a dynamic regex expression ("re", var).
func (a *awkInterpreter) regexArg(arg string) string {
	arg = strings.TrimSpace(arg)
	if body, ok := awkRegexLiteral(arg); ok {
		return body
	}
	return a.evalExpr(arg)
}

func (a *awkInterpreter) compileRegex(src string) *regexp.Regexp {
	if re, ok := a.regexps[src]; ok {
		return re
	}
	re, err := regexp.Compile(src)
	if err != nil {
		a.fail("invalid regex %q: %v", src, err)
		return nil
	}
	if a.regexps == nil {
		a.regexps = make(map[string]*regexp.Regexp)
	}
	a.regexps[src] = re
	return re
}

// awkSubstitute implements sub and gsub replacement: & is the matched text
// and \& is a literal ampersand.
func awkSubstitute(re *regexp.Regexp, target, repl string, global bool) (string, int) {
	count := 0
	result := re.ReplaceAllStringFunc(target, func(m string) string {
		if !global && count > 0 {
			return m
		}
		count++
		var sb strings.Builder
		for i := 0; i < len(repl); i++ {
			switch {
			case repl[i] == '\\' && i+1 < len(repl) && (repl[i+1] == '&' || repl[i+1] == '\\'):
				sb.WriteByte(repl[i+1])
				i++
			case repl[i] == '&':
				sb.WriteString(m)
			default:
				sb.WriteByte(repl[i])
			}
		}
		return sb.String()
	})
	return result, count
}

func (a *awkInterpreter) getField(n int) string {
	if n == 0 {
		return a.line
	}
	if n > 0 && n <= len(a.fields) {
		return a.fields[n-1]
	}
	return ""
}

func (a *awkInterpreter) callFunc(name, argStr string) string {
	args := splitAwkPrintArgs(argStr)
	for i := range args {
		args[i] = strings.TrimSpace(args[i])
	}

	switch name {
	case "length":
		if len(args) == 0 || args[0] == "" {
			return strconv.Itoa(len(a.line))
		}
		val := a.evalExpr(args[0])
		return strconv.Itoa(len(val))
	case "substr":
		if len(args) < 2 {
			return ""
		}
		str := a.evalExpr(args[0])
		start, _ := strconv.Atoi(a.evalExpr(args[1]))
		if start < 1 {
			start = 1
		}
		if start > len(str) {
			return ""
		}
		if len(args) >= 3 {
			length, _ := strconv.Atoi(a.evalExpr(args[2]))
			end := start - 1 + length
			if end > len(str) {
				end = len(str)
			}
			return str[start-1 : end]
		}
		return str[start-1:]
	case "index":
		if len(args) < 2 {
			return "0"
		}
		str := a.evalExpr(args[0])
		target := a.evalExpr(args[1])
		idx := strings.Index(str, target)
		return strconv.Itoa(idx + 1)
	case "split":
		if len(args) < 2 {
			return "0"
		}
		str := a.evalExpr(args[0])
		arrName := strings.TrimSpace(args[1])
		if !awkIsIdentifier(arrName) {
			a.fail("split target is not an array name: %s", arrName)
			return "0"
		}
		sep := a.vars["FS"]
		if len(args) >= 3 {
			sep = a.regexArg(args[2])
		}
		var parts []string
		switch {
		case sep == " ":
			parts = strings.Fields(str)
		case len(sep) == 1:
			parts = strings.Split(str, sep)
		default:
			if re := a.compileRegex(sep); re != nil {
				parts = re.Split(str, -1)
			}
		}
		a.arrays[arrName] = make(map[string]string)
		for i, p := range parts {
			a.arrays[arrName][strconv.Itoa(i+1)] = p
		}
		return strconv.Itoa(len(parts))
	case "tolower":
		if len(args) > 0 {
			return strings.ToLower(a.evalExpr(args[0]))
		}
		return ""
	case "toupper":
		if len(args) > 0 {
			return strings.ToUpper(a.evalExpr(args[0]))
		}
		return ""
	case "gsub", "sub":
		if len(args) < 2 {
			a.fail("%s requires at least 2 arguments", name)
			return "0"
		}
		re := a.compileRegex(a.regexArg(args[0]))
		replacement := a.evalExpr(args[1])
		target := "$0"
		if len(args) >= 3 {
			target = args[2]
			if !awkIsLValue(target) {
				a.fail("%s target is not assignable: %s", name, target)
				return "0"
			}
		}
		if re == nil {
			return "0"
		}
		result, count := awkSubstitute(re, a.evalExpr(target), replacement, name == "gsub")
		if count > 0 {
			a.setLValue(target, result)
		}
		return strconv.Itoa(count)
	case "match":
		if len(args) < 2 {
			return "0"
		}
		str := a.evalExpr(args[0])
		re := a.compileRegex(a.regexArg(args[1]))
		if re == nil {
			return "0"
		}
		loc := re.FindStringIndex(str)
		if loc == nil {
			a.vars["RSTART"] = "0"
			a.vars["RLENGTH"] = "-1"
			return "0"
		}
		a.vars["RSTART"] = strconv.Itoa(loc[0] + 1)
		a.vars["RLENGTH"] = strconv.Itoa(loc[1] - loc[0])
		return strconv.Itoa(loc[0] + 1)
	case "sprintf":
		if len(args) == 0 {
			return ""
		}
		format := a.evalExpr(args[0])
		var fmtArgs []string
		for _, arg := range args[1:] {
			fmtArgs = append(fmtArgs, a.evalExpr(arg))
		}
		return awkSprintf(format, fmtArgs)
	case "int":
		if len(args) > 0 {
			f, _ := strconv.ParseFloat(a.evalExpr(args[0]), 64)
			return strconv.Itoa(int(f))
		}
		return "0"
	case "sqrt":
		if len(args) > 0 {
			f, _ := strconv.ParseFloat(a.evalExpr(args[0]), 64)
			return awkFormatNum(math.Sqrt(f))
		}
		return "0"
	case "sin":
		if len(args) > 0 {
			f, _ := strconv.ParseFloat(a.evalExpr(args[0]), 64)
			return awkFormatNum(math.Sin(f))
		}
		return "0"
	case "cos":
		if len(args) > 0 {
			f, _ := strconv.ParseFloat(a.evalExpr(args[0]), 64)
			return awkFormatNum(math.Cos(f))
		}
		return "0"
	case "exp":
		if len(args) > 0 {
			f, _ := strconv.ParseFloat(a.evalExpr(args[0]), 64)
			return awkFormatNum(math.Exp(f))
		}
		return "0"
	case "log":
		if len(args) > 0 {
			f, _ := strconv.ParseFloat(a.evalExpr(args[0]), 64)
			return awkFormatNum(math.Log(f))
		}
		return "0"
	}

	a.fail("unsupported function: %s", name)
	return ""
}

// awkLiteralMask marks the bytes of expr that belong to string literals
// ("...") or regex literals (/.../), including their delimiters, so that
// operator scans never split inside a literal.
func awkLiteralMask(expr string) []bool {
	mask := make([]bool, len(expr))
	for i := 0; i < len(expr); i++ {
		if expr[i] == '"' || (expr[i] == '/' && awkRegexCanStart(expr, i)) {
			end := awkLiteralEnd(expr, i)
			for j := i; j <= end; j++ {
				mask[j] = true
			}
			i = end
		}
	}
	return mask
}

// awkLiteralEnd returns the index of the delimiter that closes the string
// or regex literal opening at start, or the last index if it is unclosed.
func awkLiteralEnd(expr string, start int) int {
	delim := expr[start]
	for i := start + 1; i < len(expr); i++ {
		if expr[i] == '\\' {
			i++
			continue
		}
		if expr[i] == delim {
			return i
		}
	}
	return len(expr) - 1
}

// awkRegexCanStart reports whether a slash at i opens a regex literal
// rather than being a division operator: it must not follow an operand.
func awkRegexCanStart(expr string, i int) bool {
	j := i - 1
	for j >= 0 && (expr[j] == ' ' || expr[j] == '\t') {
		j--
	}
	if j < 0 {
		return true
	}
	if strings.IndexByte("(,!~&|=<>?:{};\n", expr[j]) >= 0 {
		return true
	}
	// After a keyword such as "in" or "print" a slash starts a regex.
	k := j
	for k >= 0 && (expr[k] == '_' || expr[k] >= 'a' && expr[k] <= 'z' || expr[k] >= 'A' && expr[k] <= 'Z' || expr[k] >= '0' && expr[k] <= '9') {
		k--
	}
	return awkKeywords[expr[k+1:j+1]]
}

// awkIndexUnquoted returns the index of the first ch outside string and
// regex literals, or -1.
func awkIndexUnquoted(s string, ch byte) int {
	mask := awkLiteralMask(s)
	for i := 0; i < len(s); i++ {
		if s[i] == ch && !mask[i] {
			return i
		}
	}
	return -1
}

func findMatchingParen(s string, openIdx int) int {
	mask := awkLiteralMask(s)
	depth := 0
	for i := openIdx; i < len(s); i++ {
		if mask[i] {
			continue
		}
		if s[i] == '(' {
			depth++
		} else if s[i] == ')' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func awkSplitOp(expr, op string) []string {
	mask := awkLiteralMask(expr)
	depth := 0
	for i := 0; i < len(expr)-len(op)+1; i++ {
		if mask[i] {
			continue
		}
		switch expr[i] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		}
		if depth == 0 && expr[i:i+len(op)] == op {
			left := strings.TrimSpace(expr[:i])
			right := strings.TrimSpace(expr[i+len(op):])
			if left != "" && right != "" {
				return []string{left, right}
			}
		}
	}
	return nil
}

func awkSplitArith(expr string, op byte) []string {
	mask := awkLiteralMask(expr)
	depth := 0
	// Scan from right to left (left-associative)
	for i := len(expr) - 1; i >= 0; i-- {
		if mask[i] {
			continue
		}
		ch := expr[i]
		switch ch {
		case ')', ']':
			depth++
		case '(', '[':
			depth--
		}
		if depth == 0 && ch == op {
			// Don't split on unary minus/plus
			if op == '-' || op == '+' {
				j := i - 1
				for j >= 0 && (expr[j] == ' ' || expr[j] == '\t') {
					j--
				}
				if j < 0 || strings.IndexByte("(,=<>!+-*/%^&|?:~", expr[j]) >= 0 {
					continue
				}
			}
			left := strings.TrimSpace(expr[:i])
			right := strings.TrimSpace(expr[i+1:])
			if left != "" && right != "" {
				return []string{left, right}
			}
		}
	}
	return nil
}

// splitAwkConcat splits expr into the operands of an implicit string
// concatenation, such as `$1 " " $2`. Operands must be separated by
// whitespace, and the gap must sit between the end of one operand and the
// start of the next, so binary operators are never treated as operands.
func splitAwkConcat(expr string) []string {
	mask := awkLiteralMask(expr)
	var parts []string
	depth := 0
	start := 0
	for i := 0; i < len(expr); i++ {
		if mask[i] {
			continue
		}
		switch expr[i] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case ' ', '\t':
			if depth != 0 || i == 0 {
				continue
			}
			j := i
			for j < len(expr) && (expr[j] == ' ' || expr[j] == '\t') {
				j++
			}
			if j < len(expr) && awkEndsOperand(expr, i-1, mask) && awkStartsOperand(expr, j) {
				parts = append(parts, strings.TrimSpace(expr[start:i]))
				start = j
			}
			i = j - 1
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return append(parts, strings.TrimSpace(expr[start:]))
}

func awkIsWordByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func awkEndsOperand(expr string, i int, mask []bool) bool {
	c := expr[i]
	if mask[i] {
		return c == '"' || c == '/'
	}
	if c == ')' || c == ']' {
		return true
	}
	if !awkIsWordByte(c) {
		return false
	}
	k := i
	for k >= 0 && awkIsWordByte(expr[k]) {
		k--
	}
	return !awkKeywords[expr[k+1:i+1]]
}

func awkStartsOperand(expr string, j int) bool {
	c := expr[j]
	if c == '"' || c == '$' || c == '(' {
		return true
	}
	if !awkIsWordByte(c) {
		return false
	}
	return !awkKeywords[awkLeadingWord(expr[j:])]
}

func awkFindTernary(expr string) (int, int) {
	mask := awkLiteralMask(expr)
	depth := 0
	qIdx := -1
	for i := 0; i < len(expr); i++ {
		if mask[i] {
			continue
		}
		switch expr[i] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case '?':
			if depth == 0 && qIdx < 0 {
				qIdx = i
			}
		case ':':
			if depth == 0 && qIdx >= 0 {
				return qIdx, i
			}
		}
	}
	return -1, -1
}

func awkFormatNum(f float64) string {
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', 6, 64)
}

func awkUnquote(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			default:
				b.WriteByte(s[i+1])
			}
			i++
		} else {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// awkLoopLimit bounds while and for loops. Exceeding it is an error rather
// than a silent truncation.
const awkLoopLimit = 100000

// awkSplitHeader splits "keyword (header) body" into header and body.
func (a *awkInterpreter) awkSplitHeader(keyword, stmt string) (string, string, bool) {
	stmt = strings.TrimSpace(stmt[len(keyword):])
	if stmt == "" || stmt[0] != '(' {
		a.fail("syntax error in %s: %s", keyword, stmt)
		return "", "", false
	}
	closeP := findMatchingParen(stmt, 0)
	if closeP < 0 {
		a.fail("syntax error in %s: unclosed (", keyword)
		return "", "", false
	}
	return stmt[1:closeP], strings.TrimSpace(stmt[closeP+1:]), true
}

// execBody runs a loop or if body: a { block } or a single statement.
func (a *awkInterpreter) execBody(body string) {
	if strings.HasPrefix(body, "{") {
		action, _ := extractBlock(body)
		a.execAction(action)
		return
	}
	a.execAction(body)
}

func (a *awkInterpreter) execIf(stmt string) {
	// if (cond) body [else body]
	cond, rest, ok := a.awkSplitHeader("if", stmt)
	if !ok {
		return
	}

	body, elseBody := rest, ""
	if strings.HasPrefix(rest, "{") {
		_, remaining := extractBlock(rest)
		body = rest[:len(rest)-len(remaining)]
		remaining = strings.TrimLeft(remaining, "; \t\n")
		if awkStartsWithWord(remaining, "else") {
			elseBody = strings.TrimSpace(remaining[4:])
		} else if remaining != "" {
			a.fail("syntax error after if block: %s", remaining)
			return
		}
	} else if parts := splitAwkStatements(rest); len(parts) > 1 {
		body = parts[0]
		tail := strings.Join(parts[1:], "; ")
		if !awkStartsWithWord(tail, "else") {
			a.fail("syntax error after if statement: %s", tail)
			return
		}
		elseBody = strings.TrimSpace(tail[4:])
	}

	if awkTruthy(a.evalExpr(cond)) {
		a.execBody(body)
	} else if elseBody != "" {
		a.execBody(elseBody)
	}
}

func (a *awkInterpreter) execWhile(stmt string) {
	cond, body, ok := a.awkSplitHeader("while", stmt)
	if !ok {
		return
	}
	for i := 0; ; i++ {
		if i == awkLoopLimit {
			a.fail("while loop exceeded %d iterations", awkLoopLimit)
			return
		}
		if !awkTruthy(a.evalExpr(cond)) || a.err != "" {
			return
		}
		a.execBody(body)
		if a.err != "" {
			return
		}
	}
}

func (a *awkInterpreter) execFor(stmt string) {
	inner, body, ok := a.awkSplitHeader("for", stmt)
	if !ok {
		return
	}

	// for (var in array)
	if parts := awkSplitOp(inner, " in "); len(parts) == 2 && awkIsIdentifier(parts[0]) && awkIsIdentifier(parts[1]) {
		keys := make([]string, 0, len(a.arrays[parts[1]]))
		for key := range a.arrays[parts[1]] {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			a.vars[parts[0]] = key
			a.execBody(body)
			if a.err != "" {
				return
			}
		}
		return
	}

	// for (init; cond; incr) body
	parts := strings.SplitN(inner, ";", 3)
	if len(parts) != 3 {
		a.fail("syntax error in for: (%s)", inner)
		return
	}
	a.execStatement(parts[0])
	for i := 0; ; i++ {
		if i == awkLoopLimit {
			a.fail("for loop exceeded %d iterations", awkLoopLimit)
			return
		}
		if cond := strings.TrimSpace(parts[1]); cond != "" && !awkTruthy(a.evalExpr(cond)) || a.err != "" {
			return
		}
		a.execBody(body)
		if a.err != "" {
			return
		}
		a.execStatement(parts[2])
	}
}
