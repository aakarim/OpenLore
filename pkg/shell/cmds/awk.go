package cmds

import (
	"fmt"
	"io"
	"math"
	"regexp"
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

		// Pattern { action } or just { action } or /regex/ { action }
		if prog[0] == '{' {
			action, remaining := extractBlock(prog)
			rule.action = action
			prog = remaining
			rules = append(rules, rule)
			continue
		}

		if prog[0] == '/' {
			end := strings.Index(prog[1:], "/")
			if end >= 0 {
				rule.pattern = prog[1 : end+1]
				prog = strings.TrimSpace(prog[end+2:])
				if len(prog) > 0 && prog[0] == '{' {
					action, remaining := extractBlock(prog)
					rule.action = action
					prog = remaining
				} else {
					rule.action = "print"
				}
				rules = append(rules, rule)
				continue
			}
		}

		// Expression pattern
		patEnd := strings.Index(prog, "{")
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
	depth := 0
	inSingle := false
	inDouble := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\\' && i+1 < len(s) {
			i++
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
		}
		if !inSingle && !inDouble {
			if ch == '{' {
				depth++
			} else if ch == '}' {
				depth--
				if depth == 0 {
					return s[1:i], strings.TrimSpace(s[i+1:])
				}
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
			if a.matchPattern(rule.pattern) {
				a.execAction(rule.action)
			}
		}
	}

	// Run END rules
	for _, rule := range a.rules {
		if rule.isEnd {
			a.execAction(rule.action)
		}
	}

	return a.exitCode
}

func (a *awkInterpreter) splitFields(line string) {
	if a.fs == " " {
		a.fields = strings.Fields(line)
	} else {
		a.fields = strings.Split(line, a.fs)
	}
	a.nf = len(a.fields)
	a.vars["NR"] = strconv.Itoa(a.nr)
	a.vars["NF"] = strconv.Itoa(a.nf)
	a.vars["0"] = line
	for i, f := range a.fields {
		a.vars[strconv.Itoa(i+1)] = f
	}
}

func (a *awkInterpreter) matchPattern(pattern string) bool {
	if pattern == "" {
		return true
	}

	// Regex pattern (was extracted from /.../ delimiters by the parser)
	// Check if this looks like a regex pattern vs an expression
	isRegex := !strings.ContainsAny(pattern, "=!<>~ $")
	if isRegex {
		if re, err := regexp.Compile(pattern); err == nil {
			return re.MatchString(a.line)
		}
	}

	// Try as expression (e.g., $1 == "foo", NR > 1)
	result := a.evalExpr(pattern)
	return awkTruthy(result)
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
	for _, stmt := range stmts {
		a.execStatement(stmt)
	}
}

func splitAwkStatements(action string) []string {
	var stmts []string
	var current strings.Builder
	depth := 0
	inStr := false
	inRegex := false

	for i := 0; i < len(action); i++ {
		ch := action[i]
		if ch == '\\' && i+1 < len(action) {
			current.WriteByte(ch)
			i++
			current.WriteByte(action[i])
			continue
		}
		if ch == '"' && !inRegex {
			inStr = !inStr
		}
		if ch == '/' && !inStr && (i == 0 || action[i-1] == '~' || action[i-1] == ' ') {
			inRegex = !inRegex
		}
		if !inStr && !inRegex {
			if ch == '{' {
				depth++
			} else if ch == '}' {
				depth--
			}
			if ch == ';' && depth == 0 {
				s := strings.TrimSpace(current.String())
				if s != "" {
					stmts = append(stmts, s)
				}
				current.Reset()
				continue
			}
			if ch == '\n' && depth == 0 {
				s := strings.TrimSpace(current.String())
				if s != "" {
					stmts = append(stmts, s)
				}
				current.Reset()
				continue
			}
		}
		current.WriteByte(ch)
	}
	s := strings.TrimSpace(current.String())
	if s != "" {
		stmts = append(stmts, s)
	}
	return stmts
}

func (a *awkInterpreter) execStatement(stmt string) {
	stmt = strings.TrimSpace(stmt)
	if stmt == "" {
		return
	}

	// Handle if/else
	if strings.HasPrefix(stmt, "if") && len(stmt) > 2 && (stmt[2] == ' ' || stmt[2] == '(') {
		a.execIf(stmt)
		return
	}

	// Handle while
	if strings.HasPrefix(stmt, "while") && len(stmt) > 5 && (stmt[5] == ' ' || stmt[5] == '(') {
		a.execWhile(stmt)
		return
	}

	// Handle for
	if strings.HasPrefix(stmt, "for") && len(stmt) > 3 && (stmt[3] == ' ' || stmt[3] == '(') {
		a.execFor(stmt)
		return
	}

	// print/printf
	if strings.HasPrefix(stmt, "printf ") || strings.HasPrefix(stmt, "printf(") {
		a.execPrintf(stmt[6:])
		return
	}
	if stmt == "print" {
		fmt.Fprintln(a.w, a.line)
		return
	}
	if strings.HasPrefix(stmt, "print ") {
		a.execPrint(stmt[6:])
		return
	}

	// += -= etc (must be checked before simple assignment)
	for _, op := range []string{"+=", "-=", "*=", "/="} {
		if idx := strings.Index(stmt, op); idx > 0 {
			varName := strings.TrimSpace(stmt[:idx])
			valExpr := strings.TrimSpace(stmt[idx+2:])
			cur, _ := strconv.ParseFloat(a.vars[varName], 64)
			delta, _ := strconv.ParseFloat(a.evalExpr(valExpr), 64)
			var result float64
			switch op {
			case "+=":
				result = cur + delta
			case "-=":
				result = cur - delta
			case "*=":
				result = cur * delta
			case "/=":
				if delta != 0 {
					result = cur / delta
				}
			}
			a.vars[varName] = awkFormatNum(result)
			return
		}
	}

	// Assignment: var = expr
	if idx := strings.Index(stmt, "="); idx > 0 {
		ch := stmt[idx-1]
		if ch != '!' && ch != '<' && ch != '>' && ch != '=' && ch != '~' && ch != '+' && ch != '-' && ch != '*' && ch != '/' {
			if idx+1 < len(stmt) && stmt[idx+1] != '=' {
				varName := strings.TrimSpace(stmt[:idx])
				valExpr := strings.TrimSpace(stmt[idx+1:])
				// Check for array assignment: arr[key] = val
				if bIdx := strings.Index(varName, "["); bIdx >= 0 {
					arrName := varName[:bIdx]
					key := varName[bIdx+1 : len(varName)-1]
					key = a.evalExpr(key)
					val := a.evalExpr(valExpr)
					if a.arrays[arrName] == nil {
						a.arrays[arrName] = make(map[string]string)
					}
					a.arrays[arrName][key] = val
					return
				}
				val := a.evalExpr(valExpr)
				a.vars[varName] = val
				return
			}
		}
	}

	// Increment/decrement
	if strings.HasSuffix(stmt, "++") {
		varName := stmt[:len(stmt)-2]
		v, _ := strconv.Atoi(a.vars[varName])
		a.vars[varName] = strconv.Itoa(v + 1)
		return
	}
	if strings.HasSuffix(stmt, "--") {
		varName := stmt[:len(stmt)-2]
		v, _ := strconv.Atoi(a.vars[varName])
		a.vars[varName] = strconv.Itoa(v - 1)
		return
	}

}

func (a *awkInterpreter) execPrint(expr string) {
	expr = strings.TrimSpace(expr)
	parts := splitAwkPrintArgs(expr)
	var vals []string
	for _, p := range parts {
		vals = append(vals, a.evalExpr(strings.TrimSpace(p)))
	}
	ofs := a.vars["OFS"]
	if ofs == "" {
		ofs = " "
	}
	fmt.Fprintln(a.w, strings.Join(vals, ofs))
}

func splitAwkPrintArgs(expr string) []string {
	var parts []string
	var current strings.Builder
	depth := 0
	inStr := false

	for i := 0; i < len(expr); i++ {
		ch := expr[i]
		if ch == '"' && (i == 0 || expr[i-1] != '\\') {
			inStr = !inStr
		}
		if !inStr {
			if ch == '(' {
				depth++
			} else if ch == ')' {
				depth--
			}
			if ch == ',' && depth == 0 {
				parts = append(parts, current.String())
				current.Reset()
				continue
			}
		}
		current.WriteByte(ch)
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

func (a *awkInterpreter) execPrintf(expr string) {
	expr = strings.TrimSpace(expr)
	if strings.HasPrefix(expr, "(") && strings.HasSuffix(expr, ")") {
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
	if expr == "" {
		return ""
	}

	// String literal
	if len(expr) >= 2 && expr[0] == '"' && expr[len(expr)-1] == '"' {
		return awkUnquote(expr[1 : len(expr)-1])
	}

	// String concatenation: detect two adjacent expressions
	if parts := splitAwkConcat(expr); len(parts) > 1 {
		var sb strings.Builder
		for _, p := range parts {
			sb.WriteString(a.evalExpr(p))
		}
		return sb.String()
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

	// Regex match: ~ and !~
	if parts := awkSplitOp(expr, "!~"); len(parts) == 2 {
		l := a.evalExpr(parts[0])
		r := strings.TrimSpace(parts[1])
		r = strings.Trim(r, "/")
		re, err := regexp.Compile(r)
		if err != nil {
			return "0"
		}
		if !re.MatchString(l) {
			return "1"
		}
		return "0"
	}
	if parts := awkSplitOp(expr, "~"); len(parts) == 2 {
		l := a.evalExpr(parts[0])
		r := strings.TrimSpace(parts[1])
		r = strings.Trim(r, "/")
		re, err := regexp.Compile(r)
		if err != nil {
			return "0"
		}
		if re.MatchString(l) {
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

	// Parenthesized expression
	if expr[0] == '(' {
		depth := 0
		for i := 0; i < len(expr); i++ {
			if expr[i] == '(' {
				depth++
			} else if expr[i] == ')' {
				depth--
				if depth == 0 && i == len(expr)-1 {
					return a.evalExpr(expr[1:i])
				}
			}
		}
	}

	// Field reference: $0, $1, $NF, $(expr)
	if expr[0] == '$' {
		rest := expr[1:]
		if rest == "NF" {
			return a.vars[strconv.Itoa(a.nf)]
		}
		if rest == "0" {
			return a.line
		}
		n, err := strconv.Atoi(rest)
		if err == nil {
			return a.getField(n)
		}
		// $(expr)
		val := a.evalExpr(rest)
		n, _ = strconv.Atoi(val)
		return a.getField(n)
	}

	// Built-in functions
	if idx := strings.Index(expr, "("); idx > 0 {
		funcName := expr[:idx]
		closeParen := findMatchingParen(expr, idx)
		if closeParen > idx {
			argStr := expr[idx+1 : closeParen]
			return a.callFunc(funcName, argStr)
		}
	}

	// Array access: arr[key]
	if bIdx := strings.Index(expr, "["); bIdx > 0 {
		arrName := expr[:bIdx]
		key := expr[bIdx+1 : len(expr)-1]
		key = a.evalExpr(key)
		if arr, ok := a.arrays[arrName]; ok {
			return arr[key]
		}
		return ""
	}

	// Variable lookup
	if val, ok := a.vars[expr]; ok {
		return val
	}

	// Numeric literal
	if _, err := strconv.ParseFloat(expr, 64); err == nil {
		return expr
	}

	return expr
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
		sep := a.fs
		if len(args) >= 3 {
			sep = a.evalExpr(args[2])
		}
		parts := strings.Split(str, sep)
		if a.arrays[arrName] == nil {
			a.arrays[arrName] = make(map[string]string)
		}
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
	case "gsub":
		if len(args) < 2 {
			return "0"
		}
		pattern := a.evalExpr(args[0])
		replacement := a.evalExpr(args[1])
		target := a.line
		targetVar := "0"
		if len(args) >= 3 {
			targetVar = strings.TrimSpace(args[2])
			target = a.vars[targetVar]
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return "0"
		}
		matches := re.FindAllStringIndex(target, -1)
		result := re.ReplaceAllString(target, replacement)
		a.vars[targetVar] = result
		if targetVar == "0" {
			a.line = result
			a.splitFields(result)
		}
		return strconv.Itoa(len(matches))
	case "sub":
		if len(args) < 2 {
			return "0"
		}
		pattern := a.evalExpr(args[0])
		replacement := a.evalExpr(args[1])
		target := a.line
		targetVar := "0"
		if len(args) >= 3 {
			targetVar = strings.TrimSpace(args[2])
			target = a.vars[targetVar]
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return "0"
		}
		loc := re.FindStringIndex(target)
		if loc == nil {
			return "0"
		}
		result := target[:loc[0]] + replacement + target[loc[1]:]
		a.vars[targetVar] = result
		if targetVar == "0" {
			a.line = result
			a.splitFields(result)
		}
		return "1"
	case "match":
		if len(args) < 2 {
			return "0"
		}
		str := a.evalExpr(args[0])
		pattern := a.evalExpr(args[1])
		re, err := regexp.Compile(pattern)
		if err != nil {
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

	return ""
}

func findMatchingParen(s string, openIdx int) int {
	depth := 0
	inStr := false
	for i := openIdx; i < len(s); i++ {
		ch := s[i]
		if ch == '"' && (i == 0 || s[i-1] != '\\') {
			inStr = !inStr
		}
		if !inStr {
			if ch == '(' {
				depth++
			} else if ch == ')' {
				depth--
				if depth == 0 {
					return i
				}
			}
		}
	}
	return -1
}

func awkSplitOp(expr, op string) []string {
	depth := 0
	inStr := false
	for i := 0; i < len(expr)-len(op)+1; i++ {
		ch := expr[i]
		if ch == '"' && (i == 0 || expr[i-1] != '\\') {
			inStr = !inStr
		}
		if !inStr {
			if ch == '(' {
				depth++
			} else if ch == ')' {
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
	}
	return nil
}

func awkSplitArith(expr string, op byte) []string {
	depth := 0
	inStr := false
	// Scan from right to left for +/- (left-associative)
	for i := len(expr) - 1; i >= 0; i-- {
		ch := expr[i]
		if ch == '"' && (i == 0 || expr[i-1] != '\\') {
			inStr = !inStr
		}
		if !inStr {
			if ch == ')' {
				depth++
			} else if ch == '(' {
				depth--
			}
			if depth == 0 && ch == op {
				// Don't split on unary minus/plus
				if op == '-' || op == '+' {
					if i == 0 {
						continue
					}
					prev := expr[i-1]
					if prev == '(' || prev == ',' || prev == '=' || prev == '<' || prev == '>' || prev == '!' {
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
	}
	return nil
}

func splitAwkConcat(expr string) []string {
	// Only split on space between two quoted strings or variable references
	// This is a simplified concatenation detector
	return nil // Disable for now — concatenation is complex
}

func awkFindTernary(expr string) (int, int) {
	depth := 0
	inStr := false
	qIdx := -1
	for i := 0; i < len(expr); i++ {
		ch := expr[i]
		if ch == '"' && (i == 0 || expr[i-1] != '\\') {
			inStr = !inStr
		}
		if !inStr {
			if ch == '(' {
				depth++
			} else if ch == ')' {
				depth--
			}
			if depth == 0 {
				if ch == '?' {
					qIdx = i
				} else if ch == ':' && qIdx >= 0 {
					return qIdx, i
				}
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

func (a *awkInterpreter) execIf(stmt string) {
	// if (cond) { action } else { action }
	stmt = strings.TrimSpace(stmt[2:]) // remove "if"
	if stmt[0] != '(' {
		return
	}
	closeP := findMatchingParen(stmt, 0)
	if closeP < 0 {
		return
	}
	cond := stmt[1:closeP]
	rest := strings.TrimSpace(stmt[closeP+1:])

	if awkTruthy(a.evalExpr(cond)) {
		if len(rest) > 0 && rest[0] == '{' {
			action, _ := extractBlock(rest)
			a.execAction(action)
		} else {
			a.execStatement(rest)
		}
	} else {
		// Find else
		if len(rest) > 0 && rest[0] == '{' {
			_, remaining := extractBlock(rest)
			remaining = strings.TrimSpace(remaining)
			if strings.HasPrefix(remaining, "else") {
				elseBody := strings.TrimSpace(remaining[4:])
				if len(elseBody) > 0 && elseBody[0] == '{' {
					action, _ := extractBlock(elseBody)
					a.execAction(action)
				} else {
					a.execStatement(elseBody)
				}
			}
		}
	}
}

func (a *awkInterpreter) execWhile(stmt string) {
	stmt = strings.TrimSpace(stmt[5:]) // remove "while"
	if stmt[0] != '(' {
		return
	}
	closeP := findMatchingParen(stmt, 0)
	if closeP < 0 {
		return
	}
	cond := stmt[1:closeP]
	rest := strings.TrimSpace(stmt[closeP+1:])

	for i := 0; i < 10000; i++ { // safety limit
		if !awkTruthy(a.evalExpr(cond)) {
			break
		}
		if len(rest) > 0 && rest[0] == '{' {
			action, _ := extractBlock(rest)
			a.execAction(action)
		} else {
			a.execStatement(rest)
		}
	}
}

func (a *awkInterpreter) execFor(stmt string) {
	stmt = strings.TrimSpace(stmt[3:]) // remove "for"
	if stmt[0] != '(' {
		return
	}
	closeP := findMatchingParen(stmt, 0)
	if closeP < 0 {
		return
	}
	inner := stmt[1:closeP]
	rest := strings.TrimSpace(stmt[closeP+1:])

	// for (var in array)
	if strings.Contains(inner, " in ") {
		parts := strings.SplitN(inner, " in ", 2)
		varName := strings.TrimSpace(parts[0])
		arrName := strings.TrimSpace(parts[1])
		if arr, ok := a.arrays[arrName]; ok {
			for key := range arr {
				a.vars[varName] = key
				if len(rest) > 0 && rest[0] == '{' {
					action, _ := extractBlock(rest)
					a.execAction(action)
				}
			}
		}
		return
	}

	// for (init; cond; incr) { body }
	parts := strings.SplitN(inner, ";", 3)
	if len(parts) == 3 {
		a.execStatement(strings.TrimSpace(parts[0]))
		for i := 0; i < 10000; i++ {
			if !awkTruthy(a.evalExpr(strings.TrimSpace(parts[1]))) {
				break
			}
			if len(rest) > 0 && rest[0] == '{' {
				action, _ := extractBlock(rest)
				a.execAction(action)
			}
			a.execStatement(strings.TrimSpace(parts[2]))
		}
	}
}
