package cmds

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path"
	"strings"
	"unicode/utf8"

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
	emitDocMetricSelection(ctx, eventType, filePath, content, lines, contentForLineRange(content, lines))
}

func emitDocMetricSelection(ctx CmdContext, eventType, filePath string, content []byte, lines *analytics.LineRange, selection []byte) {
	if !metricsEnabled(ctx) {
		return
	}
	emitDocMetricWithHash(ctx, eventType, canonicalMetricPath(ctx, filePath), metricContentHash(ctx, filePath, content), lines, selection)
}

func emitDocMetricWithHash(ctx CmdContext, eventType, filePath, contentHash string, lines *analytics.LineRange, selection []byte) {
	unit := map[string]any{}
	if lines != nil {
		unit["lines"] = map[string]any{"start": lines.Start, "end": lines.End}
	}
	ctx.EmitMetric(context.Background(), eventType, map[string]any{
		"path":         filePath,
		"content_hash": contentHash,
		"unit":         unit,
		"bytes":        len(selection),
		"characters":   utf8.RuneCount(selection),
	})
}

func emitDocLineMetrics(ctx CmdContext, eventType, filePath string, content []byte, lines []int) {
	if !metricsEnabled(ctx) || len(lines) == 0 {
		return
	}
	metricPath := canonicalMetricPath(ctx, filePath)
	contentHash := metricContentHash(ctx, filePath, content)
	for start := 0; start < len(lines); {
		end := start
		for end+1 < len(lines) && lines[end+1] == lines[end]+1 {
			end++
		}
		lineRange := &analytics.LineRange{Start: lines[start], End: lines[end]}
		emitDocMetricWithHash(ctx, eventType, metricPath, contentHash, lineRange, contentForLineRange(content, lineRange))
		start = end + 1
	}
}

func metricContentHash(ctx CmdContext, filePath string, content []byte) string {
	if hasher, ok := ctx.FS().(vfs.ReadContentHasher); ok {
		return hasher.ReadContentHash(filePath, content)
	}
	if tracker, ok := ctx.FS().(vfs.ReadTracker); ok {
		if contentHash, seen := tracker.LastReadHash(filePath); seen && contentHash != "" {
			return contentHash
		}
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func canonicalMetricPath(ctx CmdContext, filePath string) string {
	if canonicalizer, ok := ctx.FS().(vfs.PathCanonicalizer); ok {
		return canonicalizer.CanonicalPath(filePath)
	}
	return vfs.CleanPath(filePath)
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

func contentLines(content []byte) []string {
	if len(content) == 0 {
		return nil
	}
	lines := strings.Split(string(content), "\n")
	if content[len(content)-1] == '\n' {
		lines = lines[:len(lines)-1]
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

func contentForLineRange(content []byte, lines *analytics.LineRange) []byte {
	if lines == nil || lines.Start < 1 || lines.End < lines.Start {
		return nil
	}
	start, end, line := 0, len(content), 1
	for i, b := range content {
		if line == lines.Start {
			start = i
			break
		}
		if b == '\n' {
			line++
		}
	}
	line = 1
	for i, b := range content {
		if b == '\n' {
			if line == lines.End {
				end = i + 1
				break
			}
			line++
		}
	}
	if start > end || start >= len(content) {
		return nil
	}
	return content[start:end]
}

func scopePaths(ctx CmdContext, targets []string) []string {
	scope := make([]string, 0, len(targets))
	for _, target := range targets {
		scope = append(scope, path.Clean(ctx.Resolve(target)))
	}
	return scope
}
