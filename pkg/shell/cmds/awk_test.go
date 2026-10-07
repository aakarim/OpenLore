package cmds_test

import (
	"strings"
	"testing"
)

func TestAwkFieldSep(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "awk -F , '{print $1}' /docs/data.csv")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if lines[0] != "name" {
		t.Errorf("awk -F,: got %q", lines[0])
	}
}

func TestAwkPipe(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "cat /docs/numbers.txt | awk '{sum += $1} END{print sum}'")
	if strings.TrimSpace(out) != "66" {
		t.Errorf("awk sum: got %q, want 66", strings.TrimSpace(out))
	}
}

func TestAwkRegexPattern(t *testing.T) {
	fs := testFS()
	out, _, code := execCmd(t, fs, "cat /docs/readme.md | awk '/^#/{print}'")
	if code != 0 {
		t.Fatalf("awk regex pattern failed: code=%d", code)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 {
		t.Errorf("awk /^#/: expected 1 header line, got %d lines:\n%s", len(lines), out)
	}
	if lines[0] != "# Hello World" {
		t.Errorf("awk /^#/: expected '# Hello World', got %q", lines[0])
	}
}

func TestAwkRegexDoesNotMatchAll(t *testing.T) {
	fs := testFS()
	// Pattern /^Line/ should only match lines starting with "Line"
	out, _, code := execCmd(t, fs, "cat /docs/readme.md | awk '/^Line/{print}'")
	if code != 0 {
		t.Fatalf("awk regex pattern failed: code=%d", code)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "Line") {
			t.Errorf("awk /^Line/: unexpected line %q", line)
		}
	}
	if len(lines) != 3 {
		t.Errorf("awk /^Line/: expected 3 lines, got %d", len(lines))
	}
}

// OPE-45: regex patterns, uninitialised variables, numeric literals, and
// string comparison on $0 must behave as in POSIX awk.
func TestAwkOPE45Regressions(t *testing.T) {
	fs := testFS()
	assertOutput(t, fs, `printf 'x\n## F\ny\n' | awk '/^## F$/ { print "HIT" } { print }'`, "x\nHIT\n## F\ny\n")
	assertOutput(t, fs, `printf 'x\ny\nz\n' | awk '{ n=n+1; print n }'`, "1\n2\n3\n")
	assertOutput(t, fs, `printf 'x\n## F\ny\n' | awk '$0 == "## F" && !d { print "HIT"; d=1 } { print }'`, "x\nHIT\n## F\ny\n")
	// The real-world insert from the issue: regex combined with a flag.
	assertOutput(t, fs, `printf 'a\n## Follow-ups\nb\n' | awk '/^## Follow-ups$/ && !done { print "## New"; done=1 } { print }'`, "a\n## New\n## Follow-ups\nb\n")
}

func TestAwkExpressions(t *testing.T) {
	fs := testFS()
	assertOutput(t, fs, `printf 'a b\nc d\n' | awk '{ print $2 " " $1 }'`, "b a\nd c\n")
	assertOutput(t, fs, `printf 'a b c\n' | awk '{ print $NF, $(NF-1), NF }'`, "c b 3\n")
	assertOutput(t, fs, `printf 'a\nb\na\n' | awk '{ c[$1]++ } END { for (k in c) print k, c[k] }'`, "a 2\nb 1\n")
	assertOutput(t, fs, `printf 'foo bar\n' | awk '{ gsub(/o/, "0"); sub(/a/, "[&]"); print }'`, "f00 b[a]r\n")
	assertOutput(t, fs, `printf 'a,b,c\n' | awk -F , '{ $2 = "X"; print }'`, "a X c\n")
	assertOutput(t, fs, `printf 'a,b\n' | awk 'BEGIN { FS = "," } { print $2 }'`, "b\n")
	assertOutput(t, fs, `printf '1 2\n' | awk '{ for (i = 1; i <= NF; i++) print $i * 2 }'`, "2\n4\n")
	assertOutput(t, fs, `printf '2\n5\n' | awk '{ if ($1 > 3) print "big"; else print "small" }'`, "small\nbig\n")
	assertOutput(t, fs, `printf 'ab\ncd\n' | awk '!/c/ || $0 ~ "d" { print NR ": " $0 }'`, "1: ab\n2: cd\n")
	assertOutput(t, fs, `printf 'a\n' | awk '{ x = "a+=b"; print x, 1 + 2 " items" }'`, "a+=b 3 items\n")
	// Zero-valued numeric literals are false, and numbers print normalised.
	assertOutput(t, fs, `printf 'a\n' | awk '{ x = 0.0; if (!0.0 && !x) print "f"; print (0.0 || 0), 1.50 }'`, "f\n0 1.5\n")
	assertOutput(t, fs, `printf 'a\n' | awk '{ x = "0.0"; if (x) print "string is true" }'`, "string is true\n")
}

// OPE-51: next stops the remaining rules and statements for the current
// record, including from inside if and loop bodies.
func TestAwkNext(t *testing.T) {
	fs := testFS()
	assertOutput(t, fs, `printf 'one\ntwo\nthree\n' | awk 'NR==2{print "X"; next} {print}'`, "one\nX\nthree\n")
	assertOutput(t, fs, `printf 'one\ntwo\nthree\n' | awk '{if (NR==2) next; print}'`, "one\nthree\n")
	assertOutput(t, fs, `printf 'a\nb\n' | awk '{ if (NR==1) { print "skip"; next } else print "else" } { print }'`, "skip\nelse\nb\n")
	assertOutput(t, fs, `printf 'a b c\nd\n' | awk '{ for (i = 1; i <= NF; i++) { if ($i == "b") next; print $i } } END { print NR }'`, "a\nd\n2\n")
	assertOutput(t, fs, `printf 'a\nb\n' | awk '{ n++; next; print "never" } END { print n }'`, "2\n")
}

func TestAwkUnsupportedConstructsFail(t *testing.T) {
	for _, cmd := range []string{
		`printf 'x\n' | awk 'BEGIN { next }'`,
		`printf 'x\n' | awk 'END { next }'`,
		`printf 'x\n' | awk '{ nextfile }'`,
		`printf 'x\n' | awk '{ print "a" > "/docs/out" }'`,
		`printf 'x\n' | awk '{ print foo(1) }'`,
		`printf 'x\n' | awk '{ getline line }'`,
		`printf 'x\n' | awk '{ print 1 +* 2 }'`,
		`printf 'x\n' | awk '/[/'`,
		`printf 'x\n' | awk '/abc'`,
		`printf 'x\n' | awk '/x/ { print "abc }'`,
		`printf 'x\n' | awk 'BEGIN { print "start" } { print "abc }'`,
	} {
		out, errOut, code := execCmd(t, testFS(), cmd)
		if code != 2 || !strings.HasPrefix(errOut, "awk: ") {
			t.Errorf("%s: code=%d stderr=%q, want exit 2 with awk error", cmd, code, errOut)
		}
		if strings.Contains(cmd, "abc") && out != "" {
			t.Errorf("%s: printed %q before rejecting an unterminated literal", cmd, out)
		}
	}
}
