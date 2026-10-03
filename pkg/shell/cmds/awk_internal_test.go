package cmds

import "testing"

func TestAwkEvalExprUnclosedBracketIsNotArrayAccess(t *testing.T) {
	a := &awkInterpreter{}
	const expr = "^## ["
	if got := a.evalExpr(expr); got != "" {
		t.Fatalf("evalExpr(%q) = %q, want empty result", expr, got)
	}
	if a.err == "" {
		t.Fatalf("evalExpr(%q) did not report an unsupported expression", expr)
	}
}
