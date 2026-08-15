package parser

// File is the top-level AST node representing a parsed shell input.
type File struct {
	Stmts []*Stmt
}

// Stmt is a single statement, optionally negated with !.
type Stmt struct {
	Negated bool
	Cmd     Command
}

// Command is implemented by all command AST nodes.
type Command interface{ isCommand() }

// CallExpr is a simple command: optional assignments followed by words.
type CallExpr struct {
	Assigns  []*Assign
	Args     []*Word
	Heredocs []*Heredoc // <<DELIM heredocs attached to this command
	// MergeStderr is set when the command had a `2>&1` redirection,
	// causing the shell to send the command's stderr down its stdout.
	MergeStderr bool
	// Redirect is set when the command's stdout or stderr is redirected to a
	// file. The shell buffers the selected stream and commits it as a single
	// atomic whole-object write (never a stream).
	Redirect *Redirect
}

func (*CallExpr) isCommand() {}

// Redirect represents a stdout- or stderr-to-file redirection.
type Redirect struct {
	Target *Word // the destination path
	Append bool  // true for `>>`
	Stderr bool  // true for `2>` or `2>>`
}

// Heredoc holds the body of a `<<DELIM` here-document. Body is captured
// verbatim by the lexer at the first newline after the heredoc opener.
//
// Variable expansion inside the body is intentionally NOT supported in this
// shell — heredocs are always treated as if the delimiter were quoted
// (i.e. `<<'EOF'`). This keeps the implementation small and matches the
// dominant use case: piping multi-line content into commands like
// `kb publish`.
type Heredoc struct {
	Delimiter string // e.g. "EOF"
	StripTabs bool   // true when opener was `<<-DELIM` (strips leading TABs)
	Body      string // body text, filled in by the lexer
}

// BinaryOp is the operator in a BinaryCmd.
type BinaryOp int

const (
	Pipe    BinaryOp = iota // |
	PipeAll                 // |&
	AndStmt                 // &&
	OrStmt                  // ||
)

// BinaryCmd connects two statements with an operator.
type BinaryCmd struct {
	Op BinaryOp
	X  *Stmt
	Y  *Stmt
}

func (*BinaryCmd) isCommand() {}

// Subshell wraps statements in ( ... ).
type Subshell struct{ Stmts []*Stmt }

func (*Subshell) isCommand() {}

// Block wraps statements in { ... }.
type Block struct{ Stmts []*Stmt }

func (*Block) isCommand() {}

// IfClause represents if/elif/else.
// A plain "else" is encoded as &IfClause{Then: body} with nil Cond.
type IfClause struct {
	Cond []*Stmt
	Then []*Stmt
	Else *IfClause
}

func (*IfClause) isCommand() {}

// WhileClause represents while/until loops.
type WhileClause struct {
	Until bool
	Cond  []*Stmt
	Do    []*Stmt
}

func (*WhileClause) isCommand() {}

// ForClause represents for-in loops.
type ForClause struct {
	Loop *WordIter
	Do   []*Stmt
}

func (*ForClause) isCommand() {}

// WordIter is the iterator for a for-in loop.
type WordIter struct {
	Name  *Lit
	Items []*Word
}

// DeclClause represents export/local/declare/readonly.
type DeclClause struct {
	Variant *Lit
	Args    []*Assign
}

func (*DeclClause) isCommand() {}

// TestClause represents [[ ... ]] test expressions.
type TestClause struct {
	Words []*Word
}

func (*TestClause) isCommand() {}

// TimeClause represents the time prefix.
type TimeClause struct {
	Stmt *Stmt
}

func (*TimeClause) isCommand() {}

// LetClause represents let expressions (no-op in our shell).
type LetClause struct{}

func (*LetClause) isCommand() {}

// ArithmCmd represents (( ... )) (no-op in our shell).
type ArithmCmd struct{}

func (*ArithmCmd) isCommand() {}

// Assign represents a variable assignment like NAME=VALUE.
type Assign struct {
	Name  *Lit
	Value *Word
	Naked bool // true for bare names or flags like -p
}

// Word is a shell word composed of parts.
type Word struct {
	Parts []WordPart
}

// WordPart is implemented by all word-part AST nodes.
type WordPart interface{ isWordPart() }

// Lit is a literal string.
type Lit struct{ Value string }

func (*Lit) isWordPart() {}

// SglQuoted is a single-quoted string.
type SglQuoted struct{ Value string }

func (*SglQuoted) isWordPart() {}

// DblQuoted is a double-quoted string with nested expansions.
type DblQuoted struct{ Parts []WordPart }

func (*DblQuoted) isWordPart() {}

// ParamExp represents parameter expansion: $VAR, ${VAR}, ${VAR:-default}, ${#VAR}.
type ParamExp struct {
	Param  *Lit
	Length bool
	Exp    *Expansion
}

func (*ParamExp) isWordPart() {}

// Expansion holds the operator and word for parameter expansion modifiers.
type Expansion struct {
	Op   ParamExpOp
	Word *Word
}

// ParamExpOp is the operator in a parameter expansion.
type ParamExpOp int

const (
	DefaultUnset         ParamExpOp = iota // ${VAR-word}
	DefaultUnsetOrNull                     // ${VAR:-word}
	AlternateUnset                         // ${VAR+word}
	AlternateUnsetOrNull                   // ${VAR:+word}
	AssignUnset                            // ${VAR=word}
	AssignUnsetOrNull                      // ${VAR:=word}
	ErrorUnset                             // ${VAR?word}
	ErrorUnsetOrNull                       // ${VAR:?word}
)

// CmdSubst represents command substitution $(...).
type CmdSubst struct {
	Stmts []*Stmt
}

func (*CmdSubst) isWordPart() {}

// ContainsExpansion checks if a word has any unquoted expansions.
func ContainsExpansion(w *Word) bool {
	for _, part := range w.Parts {
		switch part.(type) {
		case *CmdSubst, *ParamExp:
			return true
		}
	}
	return false
}
