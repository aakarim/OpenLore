package cmds

import "testing"

func TestAwkEvalExprUnclosedBracketIsNotArrayAccess(t *testing.T) {
	a := &awkInterpreter{}
	const expr = "^## ["
	if got := a.evalExpr(expr); got != expr {
		t.Fatalf("evalExpr(%q) = %q, want unchanged expression", expr, got)
	}
}
