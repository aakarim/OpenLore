package cmds_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/pkg/shell"
)

func TestSplitArgs(t *testing.T) {
	got := shell.SplitArgs(`echo "hello world"`)
	if len(got) != 2 || got[1] != "hello world" {
		t.Errorf("SplitArgs: got %v", got)
	}
}

func TestComplexPipe(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "cat /docs/notes.txt | sort | uniq -c | sort -rn")
	if len(strings.TrimSpace(out)) == 0 {
		t.Error("complex pipe should produce output")
	}
}

func TestPipeInsideQuotes(t *testing.T) {
	// The pipe character inside quotes should NOT split the pipeline
	out, _, _ := execCmd(t, testFS(), "echo 'hello | world'")
	if strings.TrimSpace(out) != "hello | world" {
		t.Errorf("pipe in quotes: got %q", strings.TrimSpace(out))
	}
}

func TestSemicolon(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "echo hello; echo world")
	if !strings.Contains(out, "hello") || !strings.Contains(out, "world") {
		t.Errorf("semicolon: got %q", out)
	}
}

func TestAndOperator(t *testing.T) {
	// true && echo yes → should print yes
	out, _, _ := execCmd(t, testFS(), "true && echo yes")
	if strings.TrimSpace(out) != "yes" {
		t.Errorf("&&: got %q", strings.TrimSpace(out))
	}
}

func TestAndOperatorShortCircuit(t *testing.T) {
	// false && echo no → should NOT print
	out, _, _ := execCmd(t, testFS(), "false && echo no")
	if strings.TrimSpace(out) != "" {
		t.Errorf("&& short-circuit: got %q", strings.TrimSpace(out))
	}
}

func TestOrOperator(t *testing.T) {
	// false || echo fallback → should print fallback
	out, _, _ := execCmd(t, testFS(), "false || echo fallback")
	if strings.TrimSpace(out) != "fallback" {
		t.Errorf("||: got %q", strings.TrimSpace(out))
	}
}

func TestOrOperatorShortCircuit(t *testing.T) {
	// true || echo no → should NOT print
	out, _, _ := execCmd(t, testFS(), "true || echo no")
	if strings.TrimSpace(out) != "" {
		t.Errorf("|| short-circuit: got %q", strings.TrimSpace(out))
	}
}

func TestNegation(t *testing.T) {
	assertExitCode(t, testFS(), "! false", 0)
	assertExitCode(t, testFS(), "! true", 1)
}

func TestVariableExpansion(t *testing.T) {
	sh := shell.NewShell(testFS())
	sh.SetEnv("NAME", "alice")
	var out bytes.Buffer
	sh.ExecPipeline("echo $NAME", &out, &bytes.Buffer{}, nil)
	if strings.TrimSpace(out.String()) != "alice" {
		t.Errorf("$NAME: got %q", strings.TrimSpace(out.String()))
	}
}

func TestVariableAssignment(t *testing.T) {
	sh := shell.NewShell(testFS())
	var out bytes.Buffer
	sh.ExecPipeline("FOO=hello; echo $FOO", &out, &bytes.Buffer{}, nil)
	if strings.TrimSpace(out.String()) != "hello" {
		t.Errorf("var assign: got %q", strings.TrimSpace(out.String()))
	}
}

func TestVariableDefault(t *testing.T) {
	sh := shell.NewShell(testFS())
	var out bytes.Buffer
	sh.ExecPipeline("echo ${UNSET:-default}", &out, &bytes.Buffer{}, nil)
	if strings.TrimSpace(out.String()) != "default" {
		t.Errorf("${:-default}: got %q", strings.TrimSpace(out.String()))
	}
}

func TestCommandSubstitution(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "echo $(echo hello)")
	if strings.TrimSpace(out) != "hello" {
		t.Errorf("$(echo hello): got %q", strings.TrimSpace(out))
	}
}

func TestForLoop(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "for x in a b c; do echo $x; done")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 || lines[0] != "a" || lines[2] != "c" {
		t.Errorf("for loop: got %q", out)
	}
}

func TestIfThenElse(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "if true; then echo yes; else echo no; fi")
	if strings.TrimSpace(out) != "yes" {
		t.Errorf("if: got %q", strings.TrimSpace(out))
	}
}

func TestIfElse(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "if false; then echo yes; else echo no; fi")
	if strings.TrimSpace(out) != "no" {
		t.Errorf("if else: got %q", strings.TrimSpace(out))
	}
}

func TestDoubleQuoteExpansion(t *testing.T) {
	sh := shell.NewShell(testFS())
	sh.SetEnv("WHO", "world")
	var out bytes.Buffer
	sh.ExecPipeline(`echo "hello $WHO"`, &out, &bytes.Buffer{}, nil)
	if strings.TrimSpace(out.String()) != "hello world" {
		t.Errorf("double quote expansion: got %q", strings.TrimSpace(out.String()))
	}
}

func TestSingleQuoteNoExpansion(t *testing.T) {
	sh := shell.NewShell(testFS())
	sh.SetEnv("WHO", "world")
	var out bytes.Buffer
	sh.ExecPipeline("echo '$WHO'", &out, &bytes.Buffer{}, nil)
	if strings.TrimSpace(out.String()) != "$WHO" {
		t.Errorf("single quote: got %q", strings.TrimSpace(out.String()))
	}
}

func TestSubshell(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "(echo hello; echo world)")
	if !strings.Contains(out, "hello") || !strings.Contains(out, "world") {
		t.Errorf("subshell: got %q", out)
	}
}

func TestPipeWithVariableExpansion(t *testing.T) {
	sh := shell.NewShell(testFS())
	sh.SetEnv("PAT", "alice")
	var out bytes.Buffer
	sh.ExecPipeline("cat /docs/data.csv | grep $PAT", &out, &bytes.Buffer{}, nil)
	if !strings.Contains(out.String(), "alice") {
		t.Errorf("pipe+var: got %q", out.String())
	}
}

func TestChainedAndOr(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "false || true && echo reached")
	if strings.TrimSpace(out) != "reached" {
		t.Errorf("chained &&/||: got %q", strings.TrimSpace(out))
	}
}

func TestNestedCommandSubstitution(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "echo $(echo $(echo deep))")
	if strings.TrimSpace(out) != "deep" {
		t.Errorf("nested $(): got %q", strings.TrimSpace(out))
	}
}

func TestVariableLengthExpansion(t *testing.T) {
	sh := shell.NewShell(testFS())
	sh.SetEnv("WORD", "hello")
	var out bytes.Buffer
	sh.ExecPipeline("echo ${#WORD}", &out, &bytes.Buffer{}, nil)
	if strings.TrimSpace(out.String()) != "5" {
		t.Errorf("${#}: got %q", strings.TrimSpace(out.String()))
	}
}

func TestVariableAlternate(t *testing.T) {
	sh := shell.NewShell(testFS())
	sh.SetEnv("SET", "yes")
	var out bytes.Buffer
	sh.ExecPipeline("echo ${SET:+was set}", &out, &bytes.Buffer{}, nil)
	if strings.TrimSpace(out.String()) != "was set" {
		t.Errorf("${:+}: got %q", strings.TrimSpace(out.String()))
	}
}

func TestForLoopWithCmdSub(t *testing.T) {
	fs := testFS()
	fs.AddFile("/docs/list.txt", "one\ntwo\nthree\n")
	out, _, _ := execCmd(t, fs, "for x in $(cat /docs/list.txt); do echo item:$x; done")
	if !strings.Contains(out, "item:one") || !strings.Contains(out, "item:three") {
		t.Errorf("for+cmdsub: got %q", out)
	}
}

func TestPromptFormat(t *testing.T) {
	// Ensure the prompt doesn't produce Go fmt EXTRA warnings
	sh := shell.NewShell(testFS())
	sh.SetCwd("/docs")
	var out bytes.Buffer
	// The prompt format should work without %!(EXTRA ...) errors
	prompt := "lore"
	result := fmt.Sprintf("%s:%s $ ", prompt, sh.Cwd())
	if strings.Contains(result, "EXTRA") {
		t.Errorf("prompt format has EXTRA warning: %q", result)
	}
	if result != "lore:/docs $ " {
		t.Errorf("prompt format: got %q, want 'lore:/docs $ '", result)
	}
	_ = out
}

func TestUnsupportedUsageHandler(t *testing.T) {
	sh := shell.NewShell(testFS())
	var got []shell.UnsupportedUsage
	sh.SetUnsupportedUsageHandler(func(usage shell.UnsupportedUsage) {
		got = append(got, usage)
	})

	sh.ExecPipeline("missing-command", &bytes.Buffer{}, &bytes.Buffer{}, nil)
	sh.ExecPipeline("grep -inz hello /docs/readme.md", &bytes.Buffer{}, &bytes.Buffer{}, nil)

	want := []shell.UnsupportedUsage{
		{Command: "missing-command"},
		{Command: "grep", Flag: "-z"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("unsupported usage = %#v, want %#v", got, want)
	}
}

func TestUnsupportedUsageHandlerDoesNotReportSupportedFlags(t *testing.T) {
	sh := shell.NewShell(testFS())
	var got []shell.UnsupportedUsage
	sh.SetUnsupportedUsageHandler(func(usage shell.UnsupportedUsage) {
		got = append(got, usage)
	})

	sh.ExecPipeline("grep -in hello /docs/readme.md", &bytes.Buffer{}, &bytes.Buffer{}, nil)

	if len(got) != 0 {
		t.Fatalf("reported supported usage: %#v", got)
	}
}

func TestUnsupportedUsageSanitizesFlags(t *testing.T) {
	sh := shell.NewShell(testFS())
	var got []shell.UnsupportedUsage
	sh.SetUnsupportedUsageHandler(func(usage shell.UnsupportedUsage) {
		got = append(got, usage)
	})

	sh.ExecPipeline("grep --token=secret pattern", &bytes.Buffer{}, &bytes.Buffer{}, nil)
	sh.ExecPipeline("cat -private-value", &bytes.Buffer{}, &bytes.Buffer{}, nil)
	sh.ExecPipeline("base64 -", &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader("input"))

	want := []shell.UnsupportedUsage{
		{Command: "grep", Flag: "--token"},
		{Command: "cat", Flag: "-p"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("unsupported usage = %#v, want %#v", got, want)
	}
}

func TestUnsupportedUsageHandlerDoesNotChangeCommandResult(t *testing.T) {
	run := func(handler func(shell.UnsupportedUsage)) (string, string, int) {
		sh := shell.NewShell(testFS())
		sh.SetUnsupportedUsageHandler(handler)
		var out, errOut bytes.Buffer
		code := sh.ExecPipeline("grep -z Hello /docs/readme.md", &out, &errOut, nil)
		return out.String(), errOut.String(), code
	}

	withoutOut, withoutErr, withoutCode := run(nil)
	withOut, withErr, withCode := run(func(shell.UnsupportedUsage) {})
	if withOut != withoutOut || withErr != withoutErr || withCode != withoutCode {
		t.Fatalf("logging changed result: with=(%q, %q, %d) without=(%q, %q, %d)",
			withOut, withErr, withCode, withoutOut, withoutErr, withoutCode)
	}
}
