package cmds

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

var benchmarkMatches []RipgrepMatch

type benchmarkDiskFS struct {
	root string
}

func (f benchmarkDiskFS) hostPath(p string) string {
	p = vfs.CleanPath(p)
	if p == "/" {
		return f.root
	}
	return filepath.Join(f.root, filepath.FromSlash(p[1:]))
}

func (f benchmarkDiskFS) Stat(p string) (*vfs.FileInfo, error) {
	info, err := os.Stat(f.hostPath(p))
	if err != nil {
		return nil, err
	}
	return &vfs.FileInfo{FileName: info.Name(), FilePath: vfs.CleanPath(p), Dir: info.IsDir(), FileModTime: info.ModTime(), FileSize: info.Size()}, nil
}

func (f benchmarkDiskFS) ReadDir(p string) ([]vfs.FileInfo, error) {
	entries, err := os.ReadDir(f.hostPath(p))
	if err != nil {
		return nil, err
	}
	result := make([]vfs.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !entry.IsDir() && filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		result = append(result, vfs.FileInfo{FileName: entry.Name(), FilePath: vfs.CleanPath(p + "/" + entry.Name()), Dir: entry.IsDir(), FileModTime: info.ModTime(), FileSize: info.Size()})
	}
	return result, nil
}

func (f benchmarkDiskFS) ReadFile(p string) ([]byte, error) {
	return os.ReadFile(f.hostPath(p))
}

func BenchmarkRipgrepCorpus(b *testing.B) {
	root := os.Getenv("OPENLORE_RG_CORPUS")
	if root == "" {
		b.Skip("set OPENLORE_RG_CORPUS to a prepared corpus directory")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		b.Fatal(err)
	}

	var bytes int64
	var files int64
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.Ext(entry.Name()) != ".md" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		bytes += info.Size()
		files++
		return nil
	})
	if err != nil {
		b.Fatal(err)
	}
	if files == 0 {
		b.Fatal("corpus has no files")
	}

	fsys := benchmarkDiskFS{root: root}
	cases := []struct {
		name    string
		pattern string
		opts    RipgrepOptions
	}{
		{name: "miss", pattern: "OPENLORE_RG_NOT_PRESENT_7f42c19b"},
		{name: "rare-literal", pattern: "AbortController"},
		{name: "common-insensitive", pattern: "javascript", opts: RipgrepOptions{CaseInsensitive: true}},
		{name: "heading-regex", pattern: `^#{2,4} `, opts: RipgrepOptions{LineNumbers: true}},
		{name: "files-with-matches", pattern: "javascript", opts: RipgrepOptions{FilesWithMatches: true}},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			if _, err := regexp.Compile(tc.pattern); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.SetBytes(bytes)
			b.ReportMetric(float64(files), "files/op")
			for b.Loop() {
				matches, err := Ripgrep(fsys, []string{"/"}, tc.pattern, tc.opts)
				if err != nil {
					b.Fatal(fmt.Errorf("searching corpus: %w", err))
				}
				benchmarkMatches = matches
			}
		})
	}
}
