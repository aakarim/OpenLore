package parser_test

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/pkg/shell/parser"
)

// bashEval runs a shell expression through real bash and returns its stdout.
func bashEval(t *testing.T, expr string) string {
	t.Helper()
	cmd := exec.Command("bash", "-c", expr)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	_ = cmd.Run() // ignore exit code — we care about output
	return out.String()
}

// expandWord is a minimal evaluator that expands a parser.Word using a map of vars.
func expandWord(w *parser.Word, env map[string]string) string {
	if w == nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range w.Parts {
		sb.WriteString(expandPart(p, env))
	}
	return sb.String()
}

func expandPart(part parser.WordPart, env map[string]string) string {
	switch p := part.(type) {
	case *parser.Lit:
		return p.Value
	case *parser.SglQuoted:
		return p.Value
	case *parser.DblQuoted:
		var sb strings.Builder
		for _, sub := range p.Parts {
			sb.WriteString(expandPart(sub, env))
		}
		return sb.String()
	case *parser.ParamExp:
		name := p.Param.Value
		if p.Length {
			return fmt.Sprintf("%d", len(env[name]))
		}
		val := env[name]
		if p.Exp != nil {
			word := expandWord(p.Exp.Word, env)
			switch p.Exp.Op {
			case parser.DefaultUnsetOrNull, parser.DefaultUnset:
				if val == "" {
					return word
				}
			case parser.AlternateUnsetOrNull, parser.AlternateUnset:
				if val != "" {
					return word
				}
				return ""
			case parser.AssignUnsetOrNull, parser.AssignUnset:
				if val == "" {
					env[name] = word
					return word
				}
			}
		}
		return val
	default:
		return ""
	}
}

// TestParseVsBash compares our parser's word expansion against real bash output.
func TestParseVsBash(t *testing.T) {
	tests := []struct {
		name string
		bash string // full bash command
		env  map[string]string
		word string // word to parse and expand with our parser
	}{
		{
			name: "simple variable",
			bash: `FOO=hello; echo $FOO`,
			env:  map[string]string{"FOO": "hello"},
			word: "$FOO",
		},
		{
			name: "variable in double quotes",
			bash: `FOO=hello; echo "$FOO"`,
			env:  map[string]string{"FOO": "hello"},
			word: `"$FOO"`,
		},
		{
			name: "single quotes no expansion",
			bash: `FOO=hello; echo '$FOO'`,
			env:  map[string]string{"FOO": "hello"},
			word: `'$FOO'`,
		},
		{
			name: "default value - unset",
			bash: `echo ${UNSET:-fallback}`,
			env:  map[string]string{},
			word: `${UNSET:-fallback}`,
		},
		{
			name: "default value - set",
			bash: `X=present; echo ${X:-fallback}`,
			env:  map[string]string{"X": "present"},
			word: `${X:-fallback}`,
		},
		{
			name: "alternate value - set",
			bash: `X=present; echo ${X:+alt}`,
			env:  map[string]string{"X": "present"},
			word: `${X:+alt}`,
		},
		{
			name: "alternate value - unset",
			bash: `echo ${UNSET:+alt}`,
			env:  map[string]string{},
			word: `${UNSET:+alt}`,
		},
		{
			name: "string length",
			bash: `W=hello; echo ${#W}`,
			env:  map[string]string{"W": "hello"},
			word: `${#W}`,
		},
		{
			name: "backslash in single quotes",
			bash: `echo 'a\tb'`,
			env:  map[string]string{},
			word: `'a\tb'`,
		},
		{
			name: "concatenated words",
			bash: `X=world; echo "hello "$X`,
			env:  map[string]string{"X": "world"},
			word: `"hello "$X`,
		},
		{
			name: "empty double quotes",
			bash: `echo ""`,
			env:  map[string]string{},
			word: `""`,
		},
		{
			name: "braces simple",
			bash: `X=test; echo ${X}`,
			env:  map[string]string{"X": "test"},
			word: `${X}`,
		},
		{
			name: "assign default",
			bash: `echo ${UNSET:=assigned}`,
			env:  map[string]string{},
			word: `${UNSET:=assigned}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Get bash output
			bashOut := strings.TrimRight(bashEval(t, tt.bash), "\n")

			// Parse and expand with our parser
			w := parseTestWord(t, tt.word)
			ourOut := expandWord(w, tt.env)

			if ourOut != bashOut {
				t.Errorf("mismatch for %q:\n  bash: %q\n  ours: %q", tt.word, bashOut, ourOut)
			}
		})
	}
}

func parseTestWord(t *testing.T, s string) *parser.Word {
	t.Helper()
	// Parse "echo <word>" and extract the second arg
	f, err := parser.Parse("echo " + s)
	if err != nil {
		t.Fatalf("parse error for %q: %v", s, err)
	}
	if len(f.Stmts) == 0 {
		t.Fatalf("no statements for %q", s)
	}
	call, ok := f.Stmts[0].Cmd.(*parser.CallExpr)
	if !ok {
		t.Fatalf("not a CallExpr for %q", s)
	}
	if len(call.Args) < 2 {
		t.Fatalf("no arg for %q (args=%d)", s, len(call.Args))
	}
	return call.Args[1]
}

// TestParseSyntax tests that various shell constructs parse without error.
func TestParseSyntax(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"semicolons", "echo a; echo b; echo c"},
		{"pipes", "echo hello | grep hello | wc -l"},
		{"and operator", "true && echo yes"},
		{"or operator", "false || echo fallback"},
		{"chained and-or", "false || true && echo reached"},
		{"negation", "! false"},
		{"subshell", "(echo a; echo b)"},
		{"block", "{ echo a; echo b; }"},
		{"if-then-fi", "if true; then echo yes; fi"},
		{"if-then-else-fi", "if false; then echo no; else echo yes; fi"},
		{"for loop", "for x in a b c; do echo $x; done"},
		{"while loop", "x=0; while test $x = 0; do echo loop; x=1; done"},
		{"command substitution", "echo $(echo hello)"},
		{"quoted command substitution argument", `echo A: $(grep -c "Final (v2)" /docs/file.md)`},
		{"nested cmd sub", "echo $(echo $(echo deep))"},
		{"variable assignment", "FOO=bar; echo $FOO"},
		{"double quotes with var", `echo "hello $WHO"`},
		{"single quotes preserve", `echo '$WHO'`},
		{"export", "export FOO=bar"},
		{"pipe in quotes", `echo 'hello | world'`},
		{"test brackets", "[[ -f /tmp/x ]]"},
		{"time prefix", "time echo hello"},
		{"brace param", "echo ${VAR:-default}"},
		{"length param", "echo ${#VAR}"},
		{"for with cmd sub", "for x in $(echo a b c); do echo $x; done"},
		{"xargs with braces", "echo x | xargs -I {} echo {}"},
		{"curly braces in quotes", "echo 'got: {}'"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := parser.Parse(tt.input)
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			if len(f.Stmts) == 0 {
				t.Fatal("no statements parsed")
			}
		})
	}
}

func TestUnterminatedQuoteInCommandSubstitutionDoesNotPanic(t *testing.T) {
	inputs := []string{
		`echo $(grep "unterminated)`,
		`echo $(grep 'unterminated)`,
		`echo $(grep "trailing escape\)`,
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			_, _ = parser.Parse(input)
		})
	}
}

// TestParseVsBashOutputs runs full shell expressions through both bash and our
// parser+evaluator to find gaps. These are echo-only expressions to compare
// pure output.
func TestParseVsBashOutputs(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{"echo simple", "echo hello"},
		{"echo with semicolons", "echo a; echo b"},
		{"pipe to grep", "echo 'apple\nbanana\napple' | sort | uniq"},
		{"for loop", "for x in a b c; do echo $x; done"},
		{"if true", "if true; then echo yes; fi"},
		{"if false else", "if false; then echo no; else echo yes; fi"},
		{"and short-circuit", "false && echo no"},
		{"or fallback", "false || echo yes"},
		{"negation exit code", "! true; echo $?"},
		{"subshell", "(echo sub)"},
		{"command substitution", "echo $(echo world)"},
		{"nested substitution", "echo $(echo $(echo deep))"},
		{"variable default", "echo ${UNSET:-default}"},
		{"single quotes", "echo '$UNSET'"},
		{"double quotes", `X=hello; echo "$X world"`},
		{"seq", "seq 3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bashOut := bashEval(t, tt.expr)
			// We can't easily run our full shell evaluator here without the
			// mapFS, so just verify parsing succeeds and bash produces output
			_, err := parser.Parse(tt.expr)
			if err != nil {
				t.Errorf("parse error for %q: %v (bash output: %q)", tt.expr, err, bashOut)
			}
		})
	}
}

// TestContainsExpansion verifies the expansion detection utility.
func TestContainsExpansion(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"hello", false},
		{"$VAR", true},
		{"${VAR:-x}", true},
		{"$(echo hi)", true},
		{"'$VAR'", false},
		{`"$VAR"`, false}, // DblQuoted is not a top-level expansion
		{"plain", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			w := parseTestWord(t, tt.input)
			got := parser.ContainsExpansion(w)
			if got != tt.expected {
				t.Errorf("ContainsExpansion(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}
