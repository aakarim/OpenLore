package parser

import (
	"fmt"
	"strings"
)

// Parse parses a shell input string into an AST.
func Parse(src string) (*File, error) {
	t := newTokenizer(src)
	stmts, err := parseStmtList(t, tokEOF)
	if err != nil {
		return nil, err
	}
	return &File{Stmts: stmts}, nil
}

// parseStmtList parses statements separated by ; or newline until a stop token.
func parseStmtList(t *tokenizer, stop tokenKind) ([]*Stmt, error) {
	var stmts []*Stmt
	for {
		t.skipNewlines()
		tok := t.peek()
		if tok.kind == stop || tok.kind == tokEOF {
			break
		}
		// Stop words for compound commands
		if tok.kind == tokWord && isStopWord(tok.val) {
			break
		}
		stmt, err := parseAndOr(t)
		if err != nil {
			return nil, err
		}
		if stmt != nil {
			stmts = append(stmts, stmt)
		}
		// Consume separator
		tok = t.peek()
		if tok.kind == tokSemi || tok.kind == tokNewline {
			t.next()
			continue
		}
	}
	return stmts, nil
}

func isStopWord(s string) bool {
	switch s {
	case "then", "fi", "else", "elif", "do", "done", "esac":
		return true
	}
	return false
}

// parseAndOr parses && and || chains.
func parseAndOr(t *tokenizer) (*Stmt, error) {
	left, err := parsePipeline(t)
	if err != nil || left == nil {
		return left, err
	}

	for {
		tok := t.peek()
		var op BinaryOp
		switch tok.kind {
		case tokAndIf:
			op = AndStmt
		case tokOrIf:
			op = OrStmt
		default:
			return left, nil
		}
		t.next()
		t.skipNewlines()

		right, err := parsePipeline(t)
		if err != nil {
			return nil, err
		}
		left = &Stmt{
			Cmd: &BinaryCmd{Op: op, X: left, Y: right},
		}
	}
}

// parsePipeline parses pipe chains with optional ! prefix and time prefix.
func parsePipeline(t *tokenizer) (*Stmt, error) {
	negated := false
	isTime := false

	// Check for ! prefix
	if t.peek().kind == tokBang {
		t.next()
		negated = true
	}

	// Check for time prefix
	if t.peek().kind == tokWord && t.peek().val == "time" {
		t.next()
		isTime = true
	}

	left, err := parseCommand(t)
	if err != nil || left == nil {
		return nil, err
	}

	stmt := &Stmt{Cmd: left, Negated: negated}

	// Parse pipe chain
	for {
		tok := t.peek()
		var op BinaryOp
		switch tok.kind {
		case tokPipe:
			op = Pipe
		case tokPipeAll:
			op = PipeAll
		default:
			if isTime {
				return &Stmt{Cmd: &TimeClause{Stmt: stmt}}, nil
			}
			return stmt, nil
		}
		t.next()
		t.skipNewlines()

		right, err := parseCommand(t)
		if err != nil {
			return nil, err
		}
		rightStmt := &Stmt{Cmd: right}
		stmt = &Stmt{
			Cmd:     &BinaryCmd{Op: op, X: stmt, Y: rightStmt},
			Negated: false,
		}
	}
}

var declKeywords = map[string]bool{
	"export": true, "local": true, "declare": true, "readonly": true,
}

// parseCommand parses a single command form.
func parseCommand(t *tokenizer) (Command, error) {
	tok := t.peek()

	switch tok.kind {
	case tokLParen:
		return parseSubshell(t)
	case tokLBrace:
		return parseBlock(t)
	case tokDblLBracket:
		return parseTestClause(t)
	case tokWord:
		switch tok.val {
		case "if":
			return parseIf(t)
		case "while":
			return parseWhile(t, false)
		case "until":
			return parseWhile(t, true)
		case "for":
			return parseFor(t)
		case "let":
			return parseLet(t)
		case "((...))":
			t.next()
			return &ArithmCmd{}, nil
		}
		if declKeywords[tok.val] {
			return parseDecl(t)
		}
		return parseSimpleCommand(t)
	case tokEOF, tokSemi, tokNewline:
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected token: %v", tok.val)
	}
}

func parseSubshell(t *tokenizer) (Command, error) {
	t.expect(tokLParen)
	stmts, err := parseStmtList(t, tokRParen)
	if err != nil {
		return nil, err
	}
	t.expect(tokRParen)
	return &Subshell{Stmts: stmts}, nil
}

func parseBlock(t *tokenizer) (Command, error) {
	t.expect(tokLBrace)
	stmts, err := parseStmtList(t, tokRBrace)
	if err != nil {
		return nil, err
	}
	t.expect(tokRBrace)
	return &Block{Stmts: stmts}, nil
}

func parseTestClause(t *tokenizer) (Command, error) {
	t.expect(tokDblLBracket)
	var words []*Word
	for {
		tok := t.peek()
		if tok.kind == tokDblRBracket || tok.kind == tokEOF {
			break
		}
		if tok.kind == tokWord {
			t.next()
			words = append(words, parseWordString(tok.val))
		} else {
			// consume unexpected
			t.next()
		}
	}
	t.expect(tokDblRBracket)
	return &TestClause{Words: words}, nil
}

func parseIf(t *tokenizer) (Command, error) {
	t.next() // consume "if"
	t.skipNewlines()

	cond, err := parseStmtList(t, tokEOF) // stops at "then"
	if err != nil {
		return nil, err
	}
	// expect "then"
	t.skipNewlines()
	if t.peek().kind == tokWord && t.peek().val == "then" {
		t.next()
	}

	then, err := parseStmtList(t, tokEOF) // stops at "else"/"elif"/"fi"
	if err != nil {
		return nil, err
	}

	ic := &IfClause{Cond: cond, Then: then}

	t.skipNewlines()
	tok := t.peek()
	if tok.kind == tokWord {
		switch tok.val {
		case "elif":
			t.next() // consume "elif"
			// Re-parse as another if
			t.skipNewlines()
			elifCond, err := parseStmtList(t, tokEOF)
			if err != nil {
				return nil, err
			}
			t.skipNewlines()
			if t.peek().kind == tokWord && t.peek().val == "then" {
				t.next()
			}
			elifThen, err := parseStmtList(t, tokEOF)
			if err != nil {
				return nil, err
			}
			elifClause := &IfClause{Cond: elifCond, Then: elifThen}
			// Check for more elif/else/fi
			t.skipNewlines()
			etok := t.peek()
			if etok.kind == tokWord && etok.val == "else" {
				t.next()
				elseBody, err := parseStmtList(t, tokEOF)
				if err != nil {
					return nil, err
				}
				elifClause.Else = &IfClause{Then: elseBody}
			} else if etok.kind == tokWord && etok.val == "elif" {
				// Recursive elif handling — push back and re-parse
				innerIf, err := parseIf(t)
				if err != nil {
					return nil, err
				}
				elifClause.Else = innerIf.(*IfClause)
				ic.Else = elifClause
				return ic, nil // fi already consumed by inner
			}
			ic.Else = elifClause
		case "else":
			t.next()
			elseBody, err := parseStmtList(t, tokEOF)
			if err != nil {
				return nil, err
			}
			ic.Else = &IfClause{Then: elseBody}
		}
	}

	// expect "fi"
	t.skipNewlines()
	if t.peek().kind == tokWord && t.peek().val == "fi" {
		t.next()
	}

	return ic, nil
}

func parseWhile(t *tokenizer, until bool) (Command, error) {
	t.next() // consume "while" or "until"
	t.skipNewlines()

	cond, err := parseStmtList(t, tokEOF) // stops at "do"
	if err != nil {
		return nil, err
	}

	t.skipNewlines()
	if t.peek().kind == tokWord && t.peek().val == "do" {
		t.next()
	}

	body, err := parseStmtList(t, tokEOF) // stops at "done"
	if err != nil {
		return nil, err
	}

	t.skipNewlines()
	if t.peek().kind == tokWord && t.peek().val == "done" {
		t.next()
	}

	return &WhileClause{Until: until, Cond: cond, Do: body}, nil
}

func parseFor(t *tokenizer) (Command, error) {
	t.next() // consume "for"
	t.skipNewlines()

	// Variable name
	nameTok := t.next()
	name := nameTok.val

	t.skipNewlines()

	// Optional "in items..."
	var items []*Word
	if t.peek().kind == tokWord && t.peek().val == "in" {
		t.next() // consume "in"
		for {
			tok := t.peek()
			if tok.kind == tokSemi || tok.kind == tokNewline || tok.kind == tokEOF {
				break
			}
			if tok.kind == tokWord && tok.val == "do" {
				break
			}
			t.next()
			items = append(items, parseWordString(tok.val))
		}
	}

	// consume separator
	if t.peek().kind == tokSemi || t.peek().kind == tokNewline {
		t.next()
	}
	t.skipNewlines()

	if t.peek().kind == tokWord && t.peek().val == "do" {
		t.next()
	}

	body, err := parseStmtList(t, tokEOF) // stops at "done"
	if err != nil {
		return nil, err
	}

	t.skipNewlines()
	if t.peek().kind == tokWord && t.peek().val == "done" {
		t.next()
	}

	return &ForClause{
		Loop: &WordIter{Name: &Lit{Value: name}, Items: items},
		Do:   body,
	}, nil
}

func parseDecl(t *tokenizer) (Command, error) {
	variantTok := t.next() // "export", "local", etc.
	dc := &DeclClause{Variant: &Lit{Value: variantTok.val}}

	for {
		tok := t.peek()
		if tok.kind != tokWord {
			break
		}
		if isStopWord(tok.val) {
			break
		}
		t.next()

		// Check for NAME=VALUE
		if idx := strings.IndexByte(tok.val, '='); idx > 0 {
			name := tok.val[:idx]
			val := tok.val[idx+1:]
			dc.Args = append(dc.Args, &Assign{
				Name:  &Lit{Value: name},
				Value: parseWordString(val),
			})
		} else {
			dc.Args = append(dc.Args, &Assign{
				Name:  &Lit{Value: tok.val},
				Naked: true,
			})
		}
	}

	return dc, nil
}

func parseLet(t *tokenizer) (Command, error) {
	t.next() // consume "let"
	// Consume remaining words on the line
	for {
		tok := t.peek()
		if tok.kind != tokWord {
			break
		}
		t.next()
	}
	return &LetClause{}, nil
}

// parseSimpleCommand parses assignments and a command with arguments.
func parseSimpleCommand(t *tokenizer) (Command, error) {
	call := &CallExpr{}

	// Parse leading assignments (VAR=value before the command)
	for {
		tok := t.peek()
		if tok.kind != tokWord {
			break
		}
		// Check if this is an assignment (contains = and starts with valid name char)
		if idx := strings.IndexByte(tok.val, '='); idx > 0 && isNameStart(tok.val[0]) && isValidName(tok.val[:idx]) {
			t.next()
			name := tok.val[:idx]
			val := tok.val[idx+1:]
			call.Assigns = append(call.Assigns, &Assign{
				Name:  &Lit{Value: name},
				Value: parseWordString(val),
			})
			continue
		}
		break
	}

	// Parse command and arguments, plus interleaved redirections (`2>&1`,
	// `2>`, `2>>`, `<<DELIM`).
	for {
		tok := t.peek()
		switch tok.kind {
		case tokWord:
			if isStopWord(tok.val) {
				return finalizeCall(call), nil
			}
			t.next()
			call.Args = append(call.Args, parseWordString(tok.val))
		case tokHeredoc:
			t.next()
			call.Heredocs = append(call.Heredocs, tok.hd)
		case tokRedirMerge:
			t.next()
			call.MergeStderr = true
		case tokRedirOut, tokRedirAppend, tokRedirErrOut, tokRedirErrAppend:
			appendMode := tok.kind == tokRedirAppend || tok.kind == tokRedirErrAppend
			stderr := tok.kind == tokRedirErrOut || tok.kind == tokRedirErrAppend
			t.next()
			tgt := t.peek()
			if tgt.kind != tokWord {
				return nil, fmt.Errorf("syntax error: expected filename after %s", redirOpString(appendMode))
			}
			t.next()
			call.Redirect = &Redirect{Target: parseWordString(tgt.val), Append: appendMode, Stderr: stderr}
		default:
			return finalizeCall(call), nil
		}
	}
}

func redirOpString(appendMode bool) string {
	if appendMode {
		return "`>>`"
	}
	return "`>`"
}

func finalizeCall(call *CallExpr) Command {
	// A bare heredoc or redirection with no command is meaningless. Treat
	// it the same as an empty statement so we don't dispatch to an empty
	// CallExpr in the shell.
	if len(call.Assigns) == 0 && len(call.Args) == 0 {
		return nil
	}
	return call
}

func isNameStart(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_'
}

func isValidName(s string) bool {
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if !isNameStart(ch) && (ch < '0' || ch > '9') {
			return false
		}
	}
	return true
}

func isNameChar(ch byte) bool {
	return isNameStart(ch) || (ch >= '0' && ch <= '9')
}

// parseWordString parses a raw word string into a Word AST with parts
// for expansions, quoting, etc.
func parseWordString(s string) *Word {
	parts := parseWordParts(s, false)
	if len(parts) == 0 {
		return &Word{Parts: []WordPart{&Lit{Value: ""}}}
	}
	return &Word{Parts: parts}
}

// parseWordParts parses word parts from a string.
// inDblQuote indicates we're inside double quotes.
func parseWordParts(s string, inDblQuote bool) []WordPart {
	var parts []WordPart
	var lit strings.Builder
	i := 0

	flushLit := func() {
		if lit.Len() > 0 {
			parts = append(parts, &Lit{Value: lit.String()})
			lit.Reset()
		}
	}

	for i < len(s) {
		ch := s[i]

		if ch == '\\' && i+1 < len(s) {
			i++
			lit.WriteByte(s[i])
			i++
			continue
		}

		if ch == '\'' && !inDblQuote {
			flushLit()
			i++ // skip opening '
			start := i
			for i < len(s) && s[i] != '\'' {
				i++
			}
			parts = append(parts, &SglQuoted{Value: s[start:i]})
			if i < len(s) {
				i++ // skip closing '
			}
			continue
		}

		if ch == '"' && !inDblQuote {
			flushLit()
			i++ // skip opening "
			// Find matching close quote
			start := i
			depth := 0
			for i < len(s) {
				if s[i] == '\\' && i+1 < len(s) {
					i += 2
					continue
				}
				if s[i] == '$' && i+1 < len(s) && s[i+1] == '(' {
					depth++
					i += 2
					continue
				}
				if s[i] == ')' && depth > 0 {
					depth--
					i++
					continue
				}
				if s[i] == '"' && depth == 0 {
					break
				}
				i++
			}
			inner := s[start:i]
			if i < len(s) {
				i++ // skip closing "
			}
			innerParts := parseWordParts(inner, true)
			parts = append(parts, &DblQuoted{Parts: innerParts})
			continue
		}

		if ch == '$' {
			if i+1 < len(s) && s[i+1] == '(' {
				flushLit()
				// Command substitution
				i += 2 // skip $(
				depth := 1
				start := i
				for i < len(s) && depth > 0 {
					if s[i] == '(' {
						depth++
					} else if s[i] == ')' {
						depth--
						if depth == 0 {
							break
						}
					} else if s[i] == '\'' {
						i++
						for i < len(s) && s[i] != '\'' {
							i++
						}
					} else if s[i] == '"' {
						i++
						for i < len(s) && s[i] != '"' {
							if s[i] == '\\' {
								i++
							}
							i++
						}
					}
					i++
				}
				body := s[start:i]
				if i < len(s) {
					i++ // skip closing )
				}
				// Parse the command substitution body as statements
				innerFile, err := Parse(body)
				var stmts []*Stmt
				if err == nil {
					stmts = innerFile.Stmts
				}
				parts = append(parts, &CmdSubst{Stmts: stmts})
				continue
			}

			if i+1 < len(s) && s[i+1] == '{' {
				flushLit()
				pe := parseBraceExpansion(s, &i)
				parts = append(parts, pe)
				continue
			}

			// Simple $VAR
			if i+1 < len(s) && (isNameStart(s[i+1]) || s[i+1] == '?' || s[i+1] == '#' || s[i+1] == '0' || s[i+1] == '@' || s[i+1] == '*') {
				flushLit()
				i++ // skip $
				// Special single-char params
				if s[i] == '?' || s[i] == '#' || s[i] == '0' || s[i] == '@' || s[i] == '*' {
					parts = append(parts, &ParamExp{Param: &Lit{Value: string(s[i])}})
					i++
					continue
				}
				start := i
				for i < len(s) && isNameChar(s[i]) {
					i++
				}
				parts = append(parts, &ParamExp{Param: &Lit{Value: s[start:i]}})
				continue
			}

			// Bare $ (e.g., at end of string)
			lit.WriteByte(ch)
			i++
			continue
		}

		lit.WriteByte(ch)
		i++
	}

	flushLit()
	return parts
}

// parseBraceExpansion parses ${...} parameter expansion.
func parseBraceExpansion(s string, pos *int) *ParamExp {
	*pos += 2 // skip ${
	i := *pos

	// ${#VAR}
	if i < len(s) && s[i] == '#' {
		i++ // skip #
		start := i
		for i < len(s) && s[i] != '}' {
			i++
		}
		name := s[start:i]
		if i < len(s) {
			i++ // skip }
		}
		*pos = i
		return &ParamExp{Param: &Lit{Value: name}, Length: true}
	}

	// Read parameter name
	start := i
	for i < len(s) && s[i] != '}' && s[i] != ':' && s[i] != '-' && s[i] != '+' && s[i] != '=' && s[i] != '?' {
		i++
	}
	name := s[start:i]

	if i >= len(s) || s[i] == '}' {
		if i < len(s) {
			i++ // skip }
		}
		*pos = i
		return &ParamExp{Param: &Lit{Value: name}}
	}

	// Parse operator
	hasColon := false
	if s[i] == ':' {
		hasColon = true
		i++
	}

	if i >= len(s) || s[i] == '}' {
		if i < len(s) {
			i++
		}
		*pos = i
		return &ParamExp{Param: &Lit{Value: name}}
	}

	opChar := s[i]
	i++

	var op ParamExpOp
	switch opChar {
	case '-':
		if hasColon {
			op = DefaultUnsetOrNull
		} else {
			op = DefaultUnset
		}
	case '+':
		if hasColon {
			op = AlternateUnsetOrNull
		} else {
			op = AlternateUnset
		}
	case '=':
		if hasColon {
			op = AssignUnsetOrNull
		} else {
			op = AssignUnset
		}
	case '?':
		if hasColon {
			op = ErrorUnsetOrNull
		} else {
			op = ErrorUnset
		}
	default:
		// Unknown operator, just read to }
		for i < len(s) && s[i] != '}' {
			i++
		}
		if i < len(s) {
			i++
		}
		*pos = i
		return &ParamExp{Param: &Lit{Value: name}}
	}

	// Read the word until }
	wordStart := i
	depth := 1
	for i < len(s) && depth > 0 {
		if s[i] == '{' {
			depth++
		} else if s[i] == '}' {
			depth--
			if depth == 0 {
				break
			}
		}
		i++
	}
	wordStr := s[wordStart:i]
	if i < len(s) {
		i++ // skip }
	}
	*pos = i

	return &ParamExp{
		Param: &Lit{Value: name},
		Exp: &Expansion{
			Op:   op,
			Word: parseWordString(wordStr),
		},
	}
}
