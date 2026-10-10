package cmds_test

import "testing"

func TestPrintf(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "printf '%s is %d years old\\n' alice 30")
	if out != "alice is 30 years old\n" {
		t.Errorf("printf: got %q", out)
	}
}

func TestPrintfReusesFormatWhileArgumentsRemain(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "printf '%s\\n' a b c")
	if out != "a\nb\nc\n" {
		t.Errorf("printf: got %q", out)
	}
}

// A format with no conversions consumes no arguments. Before the progress
// check this looped forever and exhausted memory from any session.
func TestPrintfFormatWithoutConversionsAndExtraArgsTerminates(t *testing.T) {
	out, _, code := execCmd(t, testFS(), "printf 'a\\n' x y")
	if code != 0 || out != "a\n" {
		t.Errorf("printf: code=%d out=%q", code, out)
	}
}

// `%%` consumes no argument. It used to decrement the argument index
// unconditionally, so with no operands the index went to -1 and the next
// pass (or a following %s) indexed args[-1] and panicked.
func TestPrintfLiteralPercent(t *testing.T) {
	for _, tc := range []struct{ cmd, want string }{
		{"printf '%%'", "%"},
		{"printf '100%%\\n'", "100%\n"},
		{"printf '%%%s\\n' a", "%a\n"},
		{"printf '%s%%\\n' a b", "a%\nb%\n"},
	} {
		out, errOut, code := execCmd(t, testFS(), tc.cmd)
		if code != 0 || out != tc.want {
			t.Errorf("%s: code=%d out=%q stderr=%q, want %q", tc.cmd, code, out, errOut, tc.want)
		}
	}
}

func TestPrintfDoubleDashEndsOptions(t *testing.T) {
	out, _, code := execCmd(t, testFS(), "printf -- '-%s-\\n' x")
	if code != 0 || out != "-x-\n" {
		t.Errorf("printf --: code=%d out=%q", code, out)
	}
	_, errOut, code := execCmd(t, testFS(), "printf --")
	if code != 1 || errOut == "" {
		t.Errorf("printf -- alone: code=%d stderr=%q", code, errOut)
	}
}
