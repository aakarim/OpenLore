package cmds_test

import (
	"slices"
	"strings"
	"testing"
)

func TestFind(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "find /docs -name '*.txt'")
	if !strings.Contains(out, "notes.txt") {
		t.Error("find should find notes.txt")
	}
}

func TestFindTypeDir(t *testing.T) {
	out, _, _ := execCmd(t, testFS(), "find /docs -type d")
	if !strings.Contains(out, "sub") {
		t.Errorf("find -type d: got %q", out)
	}
}

func TestFindRejectsUnsupportedFlag(t *testing.T) {
	for _, flag := range []string{"-exec", "-delete", "-unknown"} {
		t.Run(flag, func(t *testing.T) {
			out, errOut, code := execCmd(t, testFS(), "find /docs "+flag+" 1 -type f")
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if out != "" {
				t.Errorf("stdout = %q, want empty", out)
			}
			if !strings.Contains(errOut, "unsupported flag '"+flag+"'") {
				t.Errorf("stderr = %q, want unsupported flag name", errOut)
			}
			if !strings.Contains(errOut, "-maxdepth N") {
				t.Errorf("stderr = %q, want supported options", errOut)
			}
		})
	}
}

func TestFindValidatesOptionArguments(t *testing.T) {
	for _, tt := range []struct {
		name    string
		command string
		wantErr string
	}{
		{name: "missing name pattern", command: "find /docs -name", wantErr: "option requires an argument -- 'name'"},
		{name: "missing iname pattern", command: "find /docs -iname", wantErr: "option requires an argument -- 'iname'"},
		{name: "missing path pattern", command: "find /docs -path", wantErr: "option requires an argument -- 'path'"},
		{name: "missing ipath pattern", command: "find /docs -ipath", wantErr: "option requires an argument -- 'ipath'"},
		{name: "missing maxdepth", command: "find /docs -maxdepth", wantErr: "option requires an argument -- 'maxdepth'"},
		{name: "missing mindepth", command: "find /docs -mindepth", wantErr: "option requires an argument -- 'mindepth'"},
		{name: "negative depth", command: "find /docs -maxdepth -1", wantErr: "invalid depth '-1'"},
		{name: "nonnumeric depth", command: "find /docs -mindepth abc", wantErr: "invalid depth 'abc'"},
		{name: "empty depth", command: "find /docs -maxdepth ''", wantErr: "invalid depth ''"},
		{name: "overflow depth", command: "find /docs -maxdepth 99999999999999999999999", wantErr: "invalid depth"},
		{name: "missing type", command: "find /docs -type", wantErr: "option requires an argument -- 'type'"},
		{name: "unsupported type", command: "find /docs -type -maxdepth", wantErr: "unsupported type '-maxdepth'"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, errOut, code := execCmd(t, testFS(), tt.command)
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if out != "" {
				t.Errorf("stdout = %q, want empty", out)
			}
			if !strings.Contains(errOut, tt.wantErr) {
				t.Errorf("stderr = %q, want %q", errOut, tt.wantErr)
			}
		})
	}
}

func TestFindAllowsDashPrefixedNamePattern(t *testing.T) {
	fs := testFS()
	fs.AddFile("/docs/-maxdepth", "not a flag\n")

	out, errOut, code := execCmd(t, fs, "find /docs -name -maxdepth")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, errOut)
	}
	if out != "/docs/-maxdepth\n" {
		t.Errorf("stdout = %q, want dash-prefixed filename", out)
	}
}

func TestFindPredicates(t *testing.T) {
	fs := newMapFS()
	fs.AddDir("/")
	fs.AddDir("/Skills")
	fs.AddDir("/Skills/nested")
	fs.AddFile("/Skills/SKILL.md", "root skill\n")
	fs.AddFile("/Skills/nested/SKILL.md", "nested skill\n")
	fs.AddFile("/Skills/nested/notes.txt", "notes\n")
	fs.AddFile("/Skills/nested/é.md", "unicode\n")
	fs.AddFile("/Skills/nested/].txt", "bracket\n")
	fs.AddFile("/Skills/skill.MD", "other case\n")
	fs.AddFile("/Skills/-", "dash\n")
	for _, tt := range []struct{ args, want string }{
		{"/Skills -iname 'skill.md'", "/Skills/SKILL.md\n/Skills/nested/SKILL.md\n/Skills/skill.MD\n"},
		{"/Skills -name 'skill.md'", ""},
		{"/ -path '*Skills*' -name SKILL.md", "/Skills/SKILL.md\n/Skills/nested/SKILL.md\n"},
		{"/ -path '*/SKILL.md'", "/Skills/SKILL.md\n/Skills/nested/SKILL.md\n"},
		{"/ -path '*/skills/*'", ""},
		{"/ -ipath '*/skills/*' -iname 'skill.md'", "/Skills/SKILL.md\n/Skills/nested/SKILL.md\n/Skills/skill.MD\n"},
		{"/ -path '/Skills/?KILL.[mM][dD]'", "/Skills/SKILL.md\n"},
		{"/ -path '[.-0]Skills'", "/Skills\n"},
		{"/ -ipath '[.-0]skills'", "/Skills\n"},
		{"/Skills -path '/Skills/[/-z]'", ""},
		{"/Skills -ipath '/skills/[/-z]'", ""},
		{"/ -path '?Skills'", "/Skills\n"},
		{"/ -path '[^a-z]Skills'", "/Skills\n"},
		{"/ -path '[^.-0]Skills'", ""},
		{"/ -path '/Skills/\\-*'", "/Skills/-\n"},
		{"/ -path '*n*e*/*.md' -name SKILL.md", "/Skills/nested/SKILL.md\n"},
		{"/ -path '*é.?d'", "/Skills/nested/é.md\n"},
		{"/ -path '*/[é].md'", "/Skills/nested/é.md\n"},
		{"/ -path '*/[\\]].txt'", "/Skills/nested/].txt\n"},
		{"/ -path '*/\\].txt'", "/Skills/nested/].txt\n"},
		{"/ -path '[broken'", ""},
		{"/ -path '*/nested' -type f", ""},
		{"/Skills -iname '*.md' -name 'S*' -path '*/nested/*'", "/Skills/nested/SKILL.md\n"},
		{"/Skills -name SKILL.md -name notes.txt", ""},
		{"/Skills -name ''", ""},
		{"/Skills -path ''", ""},
		{"/Skills -maxdepth 0", "/Skills\n"},
		{"/Skills -maxdepth 1 -type d", "/Skills\n/Skills/nested\n"},
		{"/Skills -mindepth 1 -maxdepth 1 -iname '*.md'", "/Skills/SKILL.md\n/Skills/skill.MD\n"},
		{"/Skills -mindepth 2 -maxdepth 2 -name SKILL.md", "/Skills/nested/SKILL.md\n"},
		{"/Skills -mindepth 2 -maxdepth 1", ""},
		{"/Skills/SKILL.md -maxdepth 0", "/Skills/SKILL.md\n"},
		{"/Skills/SKILL.md -mindepth 1", ""},
		{"/ -maxdepth 0", "/\n"},
		{"-maxdepth 0", "/\n"},
		{"/Skills/ -maxdepth 0", "/Skills\n"},
	} {
		t.Run(tt.args, func(t *testing.T) {
			out, errs, code := execCmd(t, fs, "find "+tt.args)
			got, want := strings.Split(out, "\n"), strings.Split(tt.want, "\n")
			slices.Sort(got)
			slices.Sort(want)
			if code != 0 || errs != "" || !slices.Equal(got, want) {
				t.Fatalf("code=%d stderr=%q stdout=%q; want %q", code, errs, out, tt.want)
			}
		})
	}
}

func TestFindHelp(t *testing.T) {
	for _, command := range []string{"help find", "find --help"} {
		out, errs, code := execCmd(t, testFS(), command)
		if code != 0 || errs != "" {
			t.Fatalf("%s: code=%d stderr=%q", command, code, errs)
		}
		for _, flag := range []string{"-iname", "-path", "-ipath", "-maxdepth", "-mindepth"} {
			if !strings.Contains(out, flag) {
				t.Errorf("%s missing %s: %q", command, flag, out)
			}
		}
	}
}

func TestGlobExpansion(t *testing.T) {
	fs := testFS()
	fs.AddFile("/docs/.hidden.md", "hidden\n")

	t.Run("ls with glob", func(t *testing.T) {
		out, _, code := execCmd(t, fs, "ls /docs/*.md")
		if code != 0 {
			t.Fatalf("ls /docs/*.md failed: code=%d", code)
		}
		if !strings.Contains(out, "readme.md") {
			t.Errorf("glob /docs/*.md should match readme.md, got:\n%s", out)
		}
	})

	t.Run("bare relative glob", func(t *testing.T) {
		out, errOut, code := execCmd(t, fs, "cd /docs && echo *.md")
		if code != 0 {
			t.Fatalf("bare relative glob failed: code=%d stderr=%q", code, errOut)
		}
		if out != "/docs/readme.md\n" {
			t.Errorf("bare relative glob should expand against cwd, got %q", out)
		}
	})

	t.Run("unquoted wildcard after quoted prefix", func(t *testing.T) {
		out, errOut, code := execCmd(t, fs, `DIR=/docs; echo "$DIR"/*.md`)
		if code != 0 {
			t.Fatalf("mixed quoted glob failed: code=%d stderr=%q", code, errOut)
		}
		if out != "/docs/readme.md\n" {
			t.Errorf("unquoted wildcard should expand after a quoted prefix, got %q", out)
		}
	})

	t.Run("escaped wildcard stays literal", func(t *testing.T) {
		out, errOut, code := execCmd(t, fs, "cd /docs && echo \\*.md")
		if code != 0 {
			t.Fatalf("escaped wildcard failed: code=%d stderr=%q", code, errOut)
		}
		if out != "*.md\n" {
			t.Errorf("escaped wildcard should remain literal, got %q", out)
		}
	})

	t.Run("glob does not expand in quotes", func(t *testing.T) {
		// find -name '*.md' - the *.md should NOT be expanded
		out, _, code := execCmd(t, fs, "find /docs -name '*.md'")
		if code != 0 {
			t.Fatalf("find with quoted glob failed: code=%d", code)
		}
		if !strings.Contains(out, "readme.md") {
			t.Errorf("find -name '*.md' should find readme.md, got:\n%s", out)
		}
	})
}
