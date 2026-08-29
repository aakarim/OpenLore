package shell

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

type completionFS struct {
	dirs map[string][]vfs.FileInfo
}

func (f completionFS) Stat(name string) (*vfs.FileInfo, error) {
	name = path.Clean(name)
	if _, ok := f.dirs[name]; ok {
		return &vfs.FileInfo{FileName: path.Base(name), FilePath: name, Dir: true}, nil
	}
	for dir, entries := range f.dirs {
		for _, entry := range entries {
			if path.Join(dir, entry.FileName) == name {
				copy := entry
				return &copy, nil
			}
		}
	}
	return nil, errors.New("not found")
}

func (f completionFS) ReadDir(name string) ([]vfs.FileInfo, error) {
	entries, ok := f.dirs[path.Clean(name)]
	if !ok {
		return nil, errors.New("not found")
	}
	return append([]vfs.FileInfo(nil), entries...), nil
}

func (f completionFS) ReadFile(string) ([]byte, error) { return nil, errors.New("not found") }

func testCompletionFS() completionFS {
	return completionFS{dirs: map[string][]vfs.FileInfo{
		"/": {
			{FileName: "docs", FilePath: "/docs", Dir: true},
			{FileName: "jobs", FilePath: "/jobs", Dir: true},
		},
		"/docs": {
			{FileName: ".private", FilePath: "/docs/.private"},
			{FileName: "my file.md", FilePath: "/docs/my file.md"},
			{FileName: "notes.md", FilePath: "/docs/notes.md"},
			{FileName: "notebook.md", FilePath: "/docs/notebook.md"},
			{FileName: "subdir", FilePath: "/docs/subdir", Dir: true},
		},
		"/docs/subdir": {},
		"/jobs":        {},
	}}
}

func TestCompleteCommandsAndShellOperators(t *testing.T) {
	sh := NewShell(testCompletionFS())
	for _, test := range []struct {
		line string
		want string
	}{
		{"pw", "pwd "},
		{"echo ok | pw", "echo ok | pwd "},
		{"true && pw", "true && pwd "},
		{"echo ok; pw", "echo ok; pwd "},
		{"NAME=value pw", "NAME=value pwd "},
		{"lore docs", "lore docsets "},
	} {
		t.Run(test.line, func(t *testing.T) {
			if got := sh.complete(test.line).line; got != test.want {
				t.Fatalf("complete(%q) = %q, want %q", test.line, got, test.want)
			}
		})
	}
}

func TestCompleteCommandsFiltersCapabilities(t *testing.T) {
	sh := NewShell(testCompletionFS())
	sh.SetAllowedActions(nil)
	result := sh.complete("r")
	for _, candidate := range result.candidates {
		if candidate.value == "rm" {
			t.Fatal("read-only completion exposed rm")
		}
	}
}

func TestCompletePaths(t *testing.T) {
	sh := NewShell(testCompletionFS())
	sh.SetEnv("HOME", "/docs")
	for _, test := range []struct {
		line string
		want string
	}{
		{"cat /docs/my", `cat /docs/my\ file.md `},
		{`cat "/docs/my`, `cat "/docs/my file.md" `},
		{"cat /docs/sub", "cat /docs/subdir/"},
		{"cat ~/my", `cat ~/my\ file.md `},
		{"echo ok > /docs/my", `echo ok > /docs/my\ file.md `},
		{"echo ok 2> /docs/my", `echo ok 2> /docs/my\ file.md `},
	} {
		t.Run(test.line, func(t *testing.T) {
			if got := sh.complete(test.line).line; got != test.want {
				t.Fatalf("complete(%q) = %q, want %q", test.line, got, test.want)
			}
		})
	}

	result := sh.complete("cat /docs/no")
	if result.line != "cat /docs/note" || len(result.candidates) != 2 {
		t.Fatalf("common prefix completion = %q (%d candidates)", result.line, len(result.candidates))
	}
	for _, candidate := range sh.complete("cat /docs/").candidates {
		if candidate.value == "/docs/.private" {
			t.Fatal("completion exposed an implicit hidden entry")
		}
	}
	if got := sh.complete("cat /docs/.").line; got != "cat /docs/.private " {
		t.Fatalf("explicit hidden completion = %q", got)
	}
}

func TestCompletionDisplayEscapesControlsAndUsesUnicodeWidth(t *testing.T) {
	if got := escapeDisplay("bad\x1b\nname"); got != `bad\x1b\nname` {
		t.Fatalf("escapeDisplay = %q", got)
	}
	candidates := []completionCandidate{
		{display: "界"}, {display: "a"}, {display: "bb"}, {display: "ccc"},
	}
	wide := formatCandidateColumns(candidates, 20)
	narrow := formatCandidateColumns(candidates, 4)
	if strings.Count(wide, "\r\n") >= strings.Count(narrow, "\r\n") {
		t.Fatalf("candidate layout did not adapt to width: wide=%q narrow=%q", wide, narrow)
	}
}

func TestCompletionDoesNotInsertControlCharacters(t *testing.T) {
	fs := completionFS{dirs: map[string][]vfs.FileInfo{
		"/": {{FileName: "bad\x1bname"}},
	}}
	result := NewShell(fs).complete("cat b")
	if strings.ContainsRune(result.line, '\x1b') || result.finished {
		t.Fatalf("unsafe completion = %#v", result)
	}
	if len(result.candidates) != 1 || result.candidates[0].display != `bad\x1bname` {
		t.Fatalf("unsafe candidate was not escaped: %#v", result.candidates)
	}
}

type scriptedReadWriter struct {
	reader io.Reader
	output bytes.Buffer
}

func (rw *scriptedReadWriter) Read(p []byte) (int, error)  { return rw.reader.Read(p) }
func (rw *scriptedReadWriter) Write(p []byte) (int, error) { return rw.output.Write(p) }

func TestRunInteractiveTabCompletionAndListing(t *testing.T) {
	rw := &scriptedReadWriter{reader: strings.NewReader("pw\t\x04")}
	NewShell(testCompletionFS()).RunInteractive(rw, io.Discard, "", "lore")
	if !strings.Contains(rw.output.String(), "pwd ") {
		t.Fatalf("interactive completion output = %q", rw.output.String())
	}

	rw = &scriptedReadWriter{reader: strings.NewReader("cat /docs/no\t\t\x04")}
	NewShell(testCompletionFS()).RunInteractive(rw, io.Discard, "", "lore")
	out := rw.output.String()
	if !strings.Contains(out, "notebook.md") || !strings.Contains(out, "notes.md") || !strings.Contains(out, "lore:/ $ cat /docs/note") {
		t.Fatalf("interactive candidate listing output = %q", out)
	}

	rw = &scriptedReadWriter{reader: strings.NewReader("does-not-exist\t\x04")}
	NewShell(testCompletionFS()).RunInteractive(rw, io.Discard, "", "lore")
	if !strings.Contains(rw.output.String(), "\a") {
		t.Fatalf("missing completion did not ring bell: %q", rw.output.String())
	}
}

func TestRunInteractiveConfirmsLargeListing(t *testing.T) {
	entries := make([]vfs.FileInfo, 101)
	for i := range entries {
		entries[i] = vfs.FileInfo{FileName: "file" + string(rune(0x100+i))}
	}
	fs := completionFS{dirs: map[string][]vfs.FileInfo{"/": entries}}
	rw := &scriptedReadWriter{reader: strings.NewReader("cat \t\tn\x04")}
	NewShell(fs).RunInteractive(rw, io.Discard, "", "lore")
	if !strings.Contains(rw.output.String(), "Display all 101 possibilities? (y or n)") {
		t.Fatalf("large completion output = %q", rw.output.String())
	}
}

func TestRunInteractiveConsumesWidthChanges(t *testing.T) {
	changes := make(chan int, 1)
	changes <- 12
	close(changes)
	rw := &scriptedReadWriter{reader: strings.NewReader("cat /docs/no\t\t\x04")}
	NewShell(testCompletionFS()).RunInteractiveWithOptions(rw, io.Discard, "", "lore", InteractiveOptions{
		Width:        80,
		WidthChanges: changes,
	})
	if out := rw.output.String(); !strings.Contains(out, "notebook.md\r\nnotes.md\r\n") ||
		!strings.Contains(out, "\r\x1b[2Klore:/ $ cat /docs/note") {
		t.Fatalf("resized interactive output was not redrawn: %q", rw.output.String())
	}
}

func TestInteractiveExitAfterSeparator(t *testing.T) {
	rw := &scriptedReadWriter{reader: strings.NewReader("echo ready; exit\r")}
	NewShell(testCompletionFS()).RunInteractive(rw, io.Discard, "", "lore")
	if out := rw.output.String(); !strings.Contains(out, "ready") || !strings.HasSuffix(out, "Goodbye!\n") {
		t.Fatalf("compound exit output = %q", out)
	}
}

// TestLibghosttyTranscript writes raw interactive output for the pinned
// libghostty-vt conformance harness. Ordinary Go test runs skip it.
func TestLibghosttyTranscript(t *testing.T) {
	dir := os.Getenv("OPENLORE_LIBGHOSTTY_TRANSCRIPTS")
	if dir == "" {
		t.Skip("libghostty transcript output not requested")
	}
	for _, width := range []int{80, 20} {
		rw := &scriptedReadWriter{reader: strings.NewReader("cat /docs/no\t\t")}
		NewShell(testCompletionFS()).RunInteractiveWithOptions(rw, io.Discard, "", "lore", InteractiveOptions{Width: width})
		name := filepath.Join(dir, fmt.Sprintf("terminal-%d.bin", width))
		if err := os.WriteFile(name, rw.output.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
