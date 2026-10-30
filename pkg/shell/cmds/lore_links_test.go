package cmds_test

import (
	"testing"
)

func TestLoreLinksUsesMarkdownAndCodeSpansNotHistoryOrFences(t *testing.T) {
	fs := newMapFS()
	fs.AddFile("/docs/index.md", "[one](../missing.md#heading) [two](/missing.md)\n")
	fs.AddFile("/docs/SKILL.md", "Read `/missing.md`.\n")
	fs.AddFile("/docs/fenced.md", "```md\n[example](/missing.md)\n`/missing.md`\n```\n")
	fs.AddFile("/docs/external.md", "[external](https://example.org/missing.md)\n")
	fs.AddFile("/data/history/noise.md", "`/missing.md`\n")
	out, errOut, code := execCmd(t, fs, "lore links --to /missing.md")
	if code != 0 || errOut != "" || out != "/docs/SKILL.md\n/docs/index.md\n" {
		t.Fatalf("links exit=%d out=%q err=%q", code, out, errOut)
	}
}
