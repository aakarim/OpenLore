package parser

import (
	"strings"
)

// tokenKind classifies top-level tokens.
type tokenKind int

const (
	tokEOF tokenKind = iota
	tokNewline
	tokSemi           // ;
	tokAndIf          // &&
	tokOrIf           // ||
	tokPipe           // |
	tokPipeAll        // |&
	tokBang           // !
	tokLParen         // (
	tokRParen         // )
	tokLBrace         // {
	tokRBrace         // }
	tokDblLBracket    // [[
	tokDblRBracket    // ]]
	tokHeredoc        // <<DELIM heredoc opener
	tokRedirMerge     // 2>&1 stderr→stdout merge
	tokRedirOut       // > stdout overwrite redirection
	tokRedirAppend    // >> stdout append redirection
	tokRedirErrOut    // 2> stderr overwrite redirection
	tokRedirErrAppend // 2>> stderr append redirection
	tokWord           // everything else
)

// token is a lexer token.
type token struct {
	kind tokenKind
	val  string   // raw text for tokWord, delimiter for tokHeredoc
	hd   *Heredoc // populated for tokHeredoc — body filled in at next newline
}

// lexer scans shell input into tokens.
type lexer struct {
	src             []byte
	pos             int
	pendingHeredocs []*Heredoc // heredocs awaiting body capture at next \n
}

func newLexer(src string) *lexer {
	return &lexer{src: []byte(src)}
}

func (l *lexer) peek() byte {
	if l.pos >= len(l.src) {
		return 0
	}
	return l.src[l.pos]
}

func (l *lexer) peekAt(offset int) byte {
	i := l.pos + offset
	if i >= len(l.src) {
		return 0
	}
	return l.src[i]
}

func (l *lexer) advance() byte {
	ch := l.src[l.pos]
	l.pos++
	return ch
}

func (l *lexer) skipSpaces() {
	for l.pos < len(l.src) && (l.src[l.pos] == ' ' || l.src[l.pos] == '\t') {
		l.pos++
	}
}

func (l *lexer) skipComment() {
	if l.pos < len(l.src) && l.src[l.pos] == '#' {
		for l.pos < len(l.src) && l.src[l.pos] != '\n' {
			l.pos++
		}
	}
}

// next returns the next token.
func (l *lexer) next() token {
	l.skipSpaces()
	l.skipComment()

	if l.pos >= len(l.src) {
		return token{kind: tokEOF}
	}

	ch := l.peek()
	if ch == '2' && l.peekAt(1) == '>' {
		l.advance()
		l.advance()
		if l.peek() == '&' {
			l.advance()
			if l.peek() == '1' {
				l.advance()
				return token{kind: tokRedirMerge, val: "2>&1"}
			}
			return token{kind: tokWord, val: "2>&"}
		}
		if l.peek() == '>' {
			l.advance()
			return token{kind: tokRedirErrAppend}
		}
		return token{kind: tokRedirErrOut}
	}

	switch ch {
	case '\n':
		l.advance()
		// Drain any heredocs that opened on the line we just finished —
		// their bodies start on the next physical line.
		if len(l.pendingHeredocs) > 0 {
			l.drainHeredocs()
		}
		return token{kind: tokNewline}

	case ';':
		l.advance()
		return token{kind: tokSemi}

	case '<':
		// Recognize <<DELIM and <<-DELIM heredoc openers. Any other use of
		// `<` (e.g. `< file` redirection) is not supported and is treated as
		// part of a word so it surfaces as a clear "command not found"
		// rather than silently dropping data.
		if l.peekAt(1) == '<' {
			return l.scanHeredocOpener()
		}
		return l.scanWord()

	case '(':
		// Check for (( arithmetic — just consume as no-op
		if l.peekAt(1) == '(' {
			l.advance()
			l.advance()
			// Read until ))
			depth := 1
			for l.pos < len(l.src) && depth > 0 {
				if l.peek() == '(' && l.peekAt(1) == '(' {
					depth++
					l.advance()
				} else if l.peek() == ')' && l.peekAt(1) == ')' {
					depth--
					l.advance()
					if depth > 0 {
						l.advance()
					}
				}
				l.advance()
			}
			return token{kind: tokWord, val: "((...))"}
		}
		l.advance()
		return token{kind: tokLParen}

	case ')':
		l.advance()
		return token{kind: tokRParen}

	case '{':
		// Only treat { as block delimiter if it's standalone (followed by space/newline/EOF)
		next := l.peekAt(1)
		if next == 0 || next == ' ' || next == '\t' || next == '\n' {
			l.advance()
			return token{kind: tokLBrace}
		}
		return l.scanWord()

	case '}':
		// Only treat } as block delimiter if it's standalone (followed by space/newline/EOF/;)
		next := l.peekAt(1)
		if next == 0 || next == ' ' || next == '\t' || next == '\n' || next == ';' {
			l.advance()
			return token{kind: tokRBrace}
		}
		return l.scanWord()

	case '|':
		l.advance()
		if l.pos < len(l.src) && l.peek() == '|' {
			l.advance()
			return token{kind: tokOrIf}
		}
		if l.pos < len(l.src) && l.peek() == '&' {
			l.advance()
			return token{kind: tokPipeAll}
		}
		return token{kind: tokPipe}

	case '&':
		if l.peekAt(1) == '&' {
			l.advance()
			l.advance()
			return token{kind: tokAndIf}
		}
		// Background & — treat as separator like ;
		l.advance()
		return token{kind: tokSemi}

	case '!':
		// Only treat as bang if followed by space/newline/EOF (not part of a word)
		next := l.peekAt(1)
		if next == 0 || next == ' ' || next == '\t' || next == '\n' {
			l.advance()
			return token{kind: tokBang}
		}
		return l.scanWord()

	case '[':
		if l.peekAt(1) == '[' {
			// Check it's not part of a bigger word
			next := l.peekAt(2)
			if next == 0 || next == ' ' || next == '\t' || next == '\n' {
				l.advance()
				l.advance()
				return token{kind: tokDblLBracket}
			}
		}
		return l.scanWord()

	case ']':
		if l.peekAt(1) == ']' {
			l.advance()
			l.advance()
			return token{kind: tokDblRBracket}
		}
		return l.scanWord()

	case '>':
		// `>&2`-style fd redirections are not supported as operators; keep
		// them inside a word so they surface clearly rather than splitting.
		if l.peekAt(1) == '&' {
			return l.scanWord()
		}
		l.advance()
		if l.pos < len(l.src) && l.peek() == '>' {
			l.advance()
			return token{kind: tokRedirAppend}
		}
		return token{kind: tokRedirOut}

	default:
		return l.scanWord()
	}
}

// scanWord reads a shell word, preserving raw text including quotes and
// escapes so the word parser can handle them properly.
func (l *lexer) scanWord() token {
	var sb strings.Builder
	for l.pos < len(l.src) {
		ch := l.peek()
		// Allow `&` to remain part of the word when it's the `&` in an
		// fd-merge redirection like `2>&1` or `>&2`. Without this, `2>&1`
		// would split into the word `2>` plus a `&` separator plus the word
		// `1`, leading to a confusing `1: command not found`.
		if ch == '&' && wordEndsWithRedirOp(sb.String()) {
			if d := l.peekAt(1); d >= '0' && d <= '9' {
				sb.WriteByte(l.advance()) // &
				for l.pos < len(l.src) {
					c := l.peek()
					if c < '0' || c > '9' {
						break
					}
					sb.WriteByte(l.advance())
				}
				// Word is complete (e.g. "2>&1") — return as either a
				// merge-redirection token or a plain word, depending.
				val := sb.String()
				if val == "2>&1" {
					return token{kind: tokRedirMerge, val: val}
				}
				return token{kind: tokWord, val: val}
			}
		}
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == ';' || ch == '|' || ch == '&' || ch == ')' || ch == '>' {
			break
		}
		// { and } are included in words (e.g., {}, 'got: {}')
		// They're only special as standalone { and } tokens handled at the top level
		if ch == '(' {
			break
		}
		if ch == '\\' && l.pos+1 < len(l.src) {
			sb.WriteByte(l.advance()) // backslash
			sb.WriteByte(l.advance()) // escaped char
			continue
		}
		if ch == '\'' {
			sb.WriteByte(l.advance()) // opening '
			for l.pos < len(l.src) && l.peek() != '\'' {
				sb.WriteByte(l.advance())
			}
			if l.pos < len(l.src) {
				sb.WriteByte(l.advance()) // closing '
			}
			continue
		}
		if ch == '"' {
			sb.WriteByte(l.advance()) // opening "
			for l.pos < len(l.src) && l.peek() != '"' {
				if l.peek() == '\\' && l.pos+1 < len(l.src) {
					sb.WriteByte(l.advance()) // backslash
					sb.WriteByte(l.advance()) // escaped char
					continue
				}
				if l.peek() == '$' && l.pos+1 < len(l.src) && l.peekAt(1) == '{' {
					// Parameter expansion inside double quotes
					sb.WriteByte(l.advance()) // $
					sb.WriteByte(l.advance()) // {
					depth := 1
					for l.pos < len(l.src) && depth > 0 {
						c := l.peek()
						if c == '{' {
							depth++
						} else if c == '}' {
							depth--
							if depth == 0 {
								sb.WriteByte(l.advance())
								break
							}
						}
						sb.WriteByte(l.advance())
					}
					continue
				}
				if l.peek() == '$' && l.pos+1 < len(l.src) && l.peekAt(1) == '(' {
					// Command substitution inside double quotes
					sb.WriteByte(l.advance()) // $
					sb.WriteByte(l.advance()) // (
					depth := 1
					for l.pos < len(l.src) && depth > 0 {
						c := l.peek()
						if c == '(' {
							depth++
						} else if c == ')' {
							depth--
							if depth == 0 {
								sb.WriteByte(l.advance())
								break
							}
						}
						sb.WriteByte(l.advance())
					}
					continue
				}
				sb.WriteByte(l.advance())
			}
			if l.pos < len(l.src) {
				sb.WriteByte(l.advance()) // closing "
			}
			continue
		}
		if ch == '$' && l.pos+1 < len(l.src) && l.peekAt(1) == '{' {
			// Parameter expansion ${...} — read until matching }
			sb.WriteByte(l.advance()) // $
			sb.WriteByte(l.advance()) // {
			depth := 1
			for l.pos < len(l.src) && depth > 0 {
				c := l.peek()
				if c == '{' {
					depth++
				} else if c == '}' {
					depth--
					if depth == 0 {
						sb.WriteByte(l.advance())
						break
					}
				}
				sb.WriteByte(l.advance())
			}
			continue
		}
		if ch == '$' && l.pos+1 < len(l.src) && l.peekAt(1) == '(' {
			// Command substitution — include literally for parser
			sb.WriteByte(l.advance()) // $
			sb.WriteByte(l.advance()) // (
			depth := 1
			for l.pos < len(l.src) && depth > 0 {
				c := l.peek()
				if c == '(' {
					depth++
				} else if c == ')' {
					depth--
					if depth == 0 {
						sb.WriteByte(l.advance())
						break
					}
				} else if c == '\'' {
					sb.WriteByte(l.advance())
					for l.pos < len(l.src) && l.peek() != '\'' {
						sb.WriteByte(l.advance())
					}
					if l.pos < len(l.src) {
						sb.WriteByte(l.advance())
					}
					continue
				} else if c == '"' {
					sb.WriteByte(l.advance())
					for l.pos < len(l.src) && l.peek() != '"' {
						if l.peek() == '\\' {
							sb.WriteByte(l.advance())
						}
						sb.WriteByte(l.advance())
					}
					if l.pos < len(l.src) {
						sb.WriteByte(l.advance())
					}
					continue
				}
				sb.WriteByte(l.advance())
			}
			continue
		}
		sb.WriteByte(l.advance())
	}
	return token{kind: tokWord, val: sb.String()}
}

// tokenizer wraps lexer with one-token lookahead.
type tokenizer struct {
	lex     *lexer
	peeked  *token
	prevVal string
}

func newTokenizer(src string) *tokenizer {
	return &tokenizer{lex: newLexer(src)}
}

func (t *tokenizer) peek() token {
	if t.peeked != nil {
		return *t.peeked
	}
	tok := t.lex.next()
	t.peeked = &tok
	return tok
}

func (t *tokenizer) next() token {
	if t.peeked != nil {
		tok := *t.peeked
		t.peeked = nil
		t.prevVal = tok.val
		return tok
	}
	tok := t.lex.next()
	t.prevVal = tok.val
	return tok
}

func (t *tokenizer) expect(kind tokenKind) token {
	tok := t.next()
	if tok.kind != kind {
		// best-effort: just return what we got
	}
	return tok
}

// skipNewlines consumes any newline tokens.
func (t *tokenizer) skipNewlines() {
	for t.peek().kind == tokNewline {
		t.next()
	}
}

// wordEndsWithRedirOp reports whether the running word value looks like a
// redirection operator (digit-prefixed or bare `>` / `>>`) so that a
// following `&` should be folded into the same word rather than ending it.
func wordEndsWithRedirOp(s string) bool {
	if s == "" {
		return false
	}
	last := s[len(s)-1]
	if last != '>' {
		return false
	}
	// Optional fd digits before `>` (e.g. `2>`, `12>`); bare `>` also OK.
	for i := 0; i < len(s)-1; i++ {
		c := s[i]
		if c == '>' {
			// allow `>>` form too
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// scanHeredocOpener consumes `<<` or `<<-` followed by a delimiter word, then
// records a pending heredoc whose body will be captured at the next newline.
// The body is read literally — no parameter expansion is performed.
func (l *lexer) scanHeredocOpener() token {
	l.advance() // first <
	l.advance() // second <

	stripTabs := false
	if l.pos < len(l.src) && l.peek() == '-' {
		stripTabs = true
		l.advance()
	}

	// Skip whitespace between `<<` and the delimiter.
	for l.pos < len(l.src) && (l.peek() == ' ' || l.peek() == '\t') {
		l.advance()
	}

	// Read the delimiter. May be quoted ('EOF', "EOF") or bare (EOF). In
	// either case we always treat the body as literal — see Heredoc doc.
	var delim strings.Builder
	if l.pos < len(l.src) && (l.peek() == '\'' || l.peek() == '"') {
		q := l.advance()
		for l.pos < len(l.src) && l.peek() != q {
			delim.WriteByte(l.advance())
		}
		if l.pos < len(l.src) {
			l.advance() // closing quote
		}
	} else {
		for l.pos < len(l.src) {
			c := l.peek()
			if c == ' ' || c == '\t' || c == '\n' || c == ';' || c == '|' || c == '&' || c == ')' {
				break
			}
			delim.WriteByte(l.advance())
		}
	}

	hd := &Heredoc{Delimiter: delim.String(), StripTabs: stripTabs}
	l.pendingHeredocs = append(l.pendingHeredocs, hd)
	return token{kind: tokHeredoc, val: hd.Delimiter, hd: hd}
}

// drainHeredocs consumes body lines from l.src for each pending heredoc,
// in order, advancing l.pos past the closing delimiter line of the last one.
// If a delimiter is never found (truncated input), the body absorbs the
// rest of the source.
func (l *lexer) drainHeredocs() {
	pending := l.pendingHeredocs
	l.pendingHeredocs = nil

	for _, hd := range pending {
		var body strings.Builder
		for l.pos < len(l.src) {
			// Read one line.
			lineStart := l.pos
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
			line := string(l.src[lineStart:l.pos])
			// Consume the trailing \n if present.
			hadNewline := false
			if l.pos < len(l.src) && l.src[l.pos] == '\n' {
				l.pos++
				hadNewline = true
			}

			cmp := line
			if hd.StripTabs {
				cmp = strings.TrimLeft(line, "\t")
			}
			if cmp == hd.Delimiter {
				// End of this heredoc.
				break
			}

			if hd.StripTabs {
				line = strings.TrimLeft(line, "\t")
			}
			body.WriteString(line)
			if hadNewline {
				body.WriteByte('\n')
			}
		}
		hd.Body = body.String()
	}
}
