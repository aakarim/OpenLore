// Package validation holds the generic bundle-linting mechanism behind
// `lore validate`. Core owns scanning and diagnostics; plugins contribute the
// policy through Validator functions.
package validation

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

type Diagnostic struct {
	Path     string
	Line     int
	Column   int
	Severity Severity
	Rule     string
	Message  string
}

type File struct {
	AbsolutePath string
	Path         string
	Content      []byte
}

type Bundle struct {
	Root  string
	FS    vfs.FileSystem
	Files []File
}

type Validator func(Bundle) []Diagnostic

func Scan(fsys vfs.FileSystem, root string, validators ...Validator) ([]Diagnostic, error) {
	root = vfs.CleanPath(root)
	bundle := Bundle{Root: root, FS: fsys}
	err := vfs.WalkDir(fsys, root, func(filePath string, info *vfs.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info == nil || info.Dir {
			return nil
		}
		content, err := fsys.ReadFile(filePath)
		if err != nil {
			return err
		}
		bundle.Files = append(bundle.Files, File{
			AbsolutePath: vfs.CleanPath(filePath),
			Path:         relativePath(root, filePath),
			Content:      content,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	var diagnostics []Diagnostic
	for _, validator := range validators {
		diagnostics = append(diagnostics, validator(bundle)...)
	}
	sort.SliceStable(diagnostics, func(i, j int) bool {
		if diagnostics[i].Path != diagnostics[j].Path {
			return diagnostics[i].Path < diagnostics[j].Path
		}
		if diagnostics[i].Line != diagnostics[j].Line {
			return diagnostics[i].Line < diagnostics[j].Line
		}
		if diagnostics[i].Column != diagnostics[j].Column {
			return diagnostics[i].Column < diagnostics[j].Column
		}
		return diagnostics[i].Rule < diagnostics[j].Rule
	})
	return diagnostics, nil
}

func FormatDiagnostic(d Diagnostic) string {
	return fmt.Sprintf("%s:%d:%d: %s [%s] %s", d.Path, d.Line, d.Column, d.Severity, d.Rule, d.Message)
}

func relativePath(root, filePath string) string {
	root = vfs.CleanPath(root)
	filePath = vfs.CleanPath(filePath)
	if root == "/" {
		return strings.TrimPrefix(filePath, "/")
	}
	if filePath == root {
		return path.Base(filePath)
	}
	return strings.TrimPrefix(filePath, root+"/")
}
