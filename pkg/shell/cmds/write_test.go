package cmds

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/aakarim/go-openlore/pkg/rules"
)

func TestWriteResultMsgPrintsRuleRejectionVerbatim(t *testing.T) {
	rejection := &rules.Rejection{
		Path:   "/docs/six.md",
		Rule:   "doc-size",
		Member: "size/lines",
		Origin: "lore.json#docsets.docs",
		Findings: []rules.Finding{{
			Measured: "6 lines exceeds the limit of 5 (max: 5)",
			Limit:    "this file cannot grow past 5 lines under this rule",
		}},
	}

	var errOut bytes.Buffer
	if code := writeResultMsg(&errOut, "redirect", "/docs/six.md", fmt.Errorf("admission: %w", rejection)); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	want := rejection.Error() + "\n"
	if got := errOut.String(); got != want {
		t.Fatalf("stderr:\n got %q\nwant %q", got, want)
	}
}

func TestWriteResultMsgKeepsPlainErrorPrefix(t *testing.T) {
	var errOut bytes.Buffer
	if code := writeResultMsg(&errOut, "redirect", "/docs/six.md", errors.New("permission denied")); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	const want = "redirect: /docs/six.md: permission denied\n"
	if got := errOut.String(); got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}
