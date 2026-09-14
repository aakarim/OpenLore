package cmds

import (
	"strings"
	"testing"
)

func TestParseUnifiedDiffIgnoresBlankLinesOutsideHunks(t *testing.T) {
	diff := "--- a/doc.md\n+++ b/doc.md\n" +
		"@@ -1 +1 @@\n-old one\n+new one\n\n" +
		"@@ -3 +3 @@\n-old three\n+new three\n\n\n"

	hunks, err := parseUnifiedDiff(diff)
	if err != nil {
		t.Fatalf("parseUnifiedDiff: %v", err)
	}
	got, err := applyUnifiedDiff([]byte("old one\ntwo\nold three\n"), hunks)
	if err != nil {
		t.Fatalf("applyUnifiedDiff: %v", err)
	}
	if want := "new one\ntwo\nnew three\n"; string(got) != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
}

func TestParseUnifiedDiffRejectsHunkCountMismatch(t *testing.T) {
	tests := []struct {
		name string
		diff string
	}{
		{
			name: "short",
			diff: "@@ -1,2 +1,2 @@\n line one\n",
		},
		{
			name: "overlong",
			diff: "@@ -1 +1 @@\n line one\n line two\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseUnifiedDiff(tt.diff)
			if err == nil {
				t.Fatal("parseUnifiedDiff unexpectedly succeeded")
			}
			for _, want := range []string{"hunk 1", "old-side", "new-side", "header specifies"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}
