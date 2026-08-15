package parser_test

import (
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/pkg/shell/parser"
)

func TestHeredocCapturesBody(t *testing.T) {
	src := "cat <<EOF\nhello\nworld\nEOF\n"
	f, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(f.Stmts) != 1 {
		t.Fatalf("expected 1 stmt, got %d", len(f.Stmts))
	}
	call, ok := f.Stmts[0].Cmd.(*parser.CallExpr)
	if !ok {
		t.Fatalf("expected CallExpr, got %T", f.Stmts[0].Cmd)
	}
	if len(call.Heredocs) != 1 {
		t.Fatalf("expected 1 heredoc, got %d", len(call.Heredocs))
	}
	want := "hello\nworld\n"
	if call.Heredocs[0].Body != want {
		t.Errorf("body: got %q, want %q", call.Heredocs[0].Body, want)
	}
}

func TestHeredocQuotedDelimiter(t *testing.T) {
	src := "cat <<'EOF'\n$X is literal\nEOF\n"
	f, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	call := f.Stmts[0].Cmd.(*parser.CallExpr)
	if len(call.Heredocs) != 1 {
		t.Fatalf("expected 1 heredoc, got %d", len(call.Heredocs))
	}
	if !strings.Contains(call.Heredocs[0].Body, "$X is literal") {
		t.Errorf("expected literal body, got %q", call.Heredocs[0].Body)
	}
}

func TestHeredocInPipeline(t *testing.T) {
	src := "cat <<EOF | wc -l\na\nb\nc\nEOF\n"
	f, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(f.Stmts) != 1 {
		t.Fatalf("expected 1 stmt, got %d", len(f.Stmts))
	}
	// Should be a pipeline (BinaryCmd) whose left side is `cat` with a heredoc.
	bc, ok := f.Stmts[0].Cmd.(*parser.BinaryCmd)
	if !ok {
		t.Fatalf("expected BinaryCmd, got %T", f.Stmts[0].Cmd)
	}
	left, ok := bc.X.Cmd.(*parser.CallExpr)
	if !ok {
		t.Fatalf("left side: expected CallExpr, got %T", bc.X.Cmd)
	}
	if len(left.Heredocs) != 1 {
		t.Fatalf("left side: expected 1 heredoc, got %d", len(left.Heredocs))
	}
	if left.Heredocs[0].Body != "a\nb\nc\n" {
		t.Errorf("body: got %q", left.Heredocs[0].Body)
	}
}

func TestHeredocStripTabs(t *testing.T) {
	src := "cat <<-EOF\n\thello\n\t\tworld\n\tEOF\n"
	f, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	call := f.Stmts[0].Cmd.(*parser.CallExpr)
	if len(call.Heredocs) != 1 {
		t.Fatalf("expected 1 heredoc, got %d", len(call.Heredocs))
	}
	want := "hello\nworld\n"
	if call.Heredocs[0].Body != want {
		t.Errorf("body: got %q, want %q", call.Heredocs[0].Body, want)
	}
}

func TestHeredocFollowedByMoreCommands(t *testing.T) {
	src := "cat <<EOF\nfirst\nEOF\necho second\n"
	f, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(f.Stmts) != 2 {
		t.Fatalf("expected 2 stmts, got %d", len(f.Stmts))
	}
	first := f.Stmts[0].Cmd.(*parser.CallExpr)
	if first.Heredocs[0].Body != "first\n" {
		t.Errorf("first body: got %q", first.Heredocs[0].Body)
	}
	second := f.Stmts[1].Cmd.(*parser.CallExpr)
	if len(second.Args) < 2 || second.Args[1].Parts[0].(*parser.Lit).Value != "second" {
		t.Errorf("second stmt args: %#v", second.Args)
	}
}

func TestStderrMergeToken(t *testing.T) {
	src := "kb publish --help 2>&1"
	f, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(f.Stmts) != 1 {
		t.Fatalf("expected 1 stmt, got %d", len(f.Stmts))
	}
	call, ok := f.Stmts[0].Cmd.(*parser.CallExpr)
	if !ok {
		t.Fatalf("expected CallExpr, got %T", f.Stmts[0].Cmd)
	}
	if !call.MergeStderr {
		t.Errorf("MergeStderr should be true")
	}
	// 2>&1 should NOT appear in Args.
	for _, a := range call.Args {
		for _, p := range a.Parts {
			if lit, ok := p.(*parser.Lit); ok && lit.Value == "2>&1" {
				t.Errorf("2>&1 leaked into args: %q", lit.Value)
			}
		}
	}
}

func TestStderrMergeBeforePipe(t *testing.T) {
	src := "kb publish 2>&1 | head"
	f, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bc, ok := f.Stmts[0].Cmd.(*parser.BinaryCmd)
	if !ok {
		t.Fatalf("expected BinaryCmd, got %T", f.Stmts[0].Cmd)
	}
	left := bc.X.Cmd.(*parser.CallExpr)
	if !left.MergeStderr {
		t.Errorf("left side MergeStderr should be true")
	}
}

func TestStderrFileRedirectTokens(t *testing.T) {
	for _, tc := range []struct {
		src    string
		target string
		append bool
	}{
		{src: "cat /missing 2>/dev/null", target: "/dev/null"},
		{src: "cat /missing 2>> errors.log", target: "errors.log", append: true},
	} {
		f, err := parser.Parse(tc.src)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.src, err)
		}
		call := f.Stmts[0].Cmd.(*parser.CallExpr)
		if call.Redirect == nil || !call.Redirect.Stderr || call.Redirect.Append != tc.append {
			t.Fatalf("%q: redirect = %#v", tc.src, call.Redirect)
		}
		if got := call.Redirect.Target.Parts[0].(*parser.Lit).Value; got != tc.target {
			t.Errorf("%q: target = %q, want %q", tc.src, got, tc.target)
		}
	}
}
