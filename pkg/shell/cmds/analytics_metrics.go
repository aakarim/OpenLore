package cmds

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path"
	"strings"

	"github.com/aakarim/go-openlore/internal/analytics"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

type metricsState interface {
	MetricsEnabled() bool
}

func metricsEnabled(ctx CmdContext) bool {
	state, ok := ctx.(metricsState)
	return !ok || state.MetricsEnabled()
}

func emitDocMetric(ctx CmdContext, eventType, filePath string, content []byte, lines *analytics.LineRange) {
	if !metricsEnabled(ctx) {
		return
	}
	sum := sha256.Sum256(content)
	unit := map[string]any{}
	if lines != nil {
		unit["lines"] = map[string]any{"start": lines.Start, "end": lines.End}
	}
	ctx.EmitMetric(context.Background(), eventType, map[string]any{
		"path":         vfs.CleanPath(filePath),
		"content_hash": hex.EncodeToString(sum[:]),
		"unit":         unit,
		"docset":       docsetForPath(filePath),
	})
}

func emitSearchMetric(ctx CmdContext, pattern string, scope []string, matchedFiles, matchedLines int, filled bool) {
	if !metricsEnabled(ctx) {
		return
	}
	ctx.EmitMetric(context.Background(), "search.query", map[string]any{
		"pattern":       pattern,
		"scope":         scope,
		"matched_files": matchedFiles,
		"matched_lines": matchedLines,
		"filled":        filled,
	})
}

func docsetForPath(filePath string) string {
	clean := strings.TrimPrefix(vfs.CleanPath(filePath), "/")
	if clean == "" {
		return ""
	}
	return strings.SplitN(clean, "/", 2)[0]
}

func contentLineCount(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	lines := bytes.Count(content, []byte{'\n'})
	if content[len(content)-1] != '\n' {
		lines++
	}
	return lines
}

func fullLineRange(content []byte) *analytics.LineRange {
	if lines := contentLineCount(content); lines > 0 {
		return &analytics.LineRange{Start: 1, End: lines}
	}
	return nil
}

func byteLineRange(content []byte, start, end int) *analytics.LineRange {
	if start < 0 {
		start = 0
	}
	if end > len(content) {
		end = len(content)
	}
	if start >= end {
		return nil
	}
	startLine := bytes.Count(content[:start], []byte{'\n'}) + 1
	endLine := bytes.Count(content[:end], []byte{'\n'})
	if content[end-1] != '\n' {
		endLine++
	}
	if endLine < startLine {
		endLine = startLine
	}
	return &analytics.LineRange{Start: startLine, End: endLine}
}

func scopePaths(ctx CmdContext, targets []string) []string {
	scope := make([]string, 0, len(targets))
	for _, target := range targets {
		scope = append(scope, path.Clean(ctx.Resolve(target)))
	}
	return scope
}
