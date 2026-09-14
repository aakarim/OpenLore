package cmds

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path"

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
	contentHash := ""
	if tracker, ok := ctx.FS().(vfs.ReadTracker); ok {
		contentHash, _ = tracker.LastReadHash(filePath)
	}
	if contentHash == "" {
		sum := sha256.Sum256(content)
		contentHash = hex.EncodeToString(sum[:])
	}
	unit := map[string]any{}
	if lines != nil {
		unit["lines"] = map[string]any{"start": lines.Start, "end": lines.End}
	}
	ctx.EmitMetric(context.Background(), eventType, map[string]any{
		"path":         vfs.CleanPath(filePath),
		"content_hash": contentHash,
		"unit":         unit,
	})
}

func emitDocLineMetrics(ctx CmdContext, eventType, filePath string, content []byte, lines []int) {
	for start := 0; start < len(lines); {
		end := start
		for end+1 < len(lines) && lines[end+1] == lines[end]+1 {
			end++
		}
		emitDocMetric(ctx, eventType, filePath, content, &analytics.LineRange{Start: lines[start], End: lines[end]})
		start = end + 1
	}
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
