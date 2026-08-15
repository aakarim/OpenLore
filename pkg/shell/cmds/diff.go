package cmds

import (
	"fmt"
	"io"
	"strings"
)

func CmdDiff(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	unified := false
	brief := false

	var files []string
	for _, a := range args {
		switch a {
		case "-u":
			unified = true
		case "-q":
			brief = true
		default:
			if strings.HasPrefix(a, "-") {
				ReportUnsupportedFlag(ctx, "diff", a)
			} else {
				files = append(files, a)
			}
		}
	}

	if len(files) < 2 {
		fmt.Fprintln(errW, "diff: missing operand")
		return 1
	}

	p1 := ctx.Resolve(files[0])
	p2 := ctx.Resolve(files[1])

	c1, err := ctx.FS().ReadFile(p1)
	if err != nil {
		fmt.Fprintf(errW, "diff: %s: %s\n", files[0], err)
		return 2
	}
	c2, err := ctx.FS().ReadFile(p2)
	if err != nil {
		fmt.Fprintf(errW, "diff: %s: %s\n", files[1], err)
		return 2
	}

	lines1 := strings.Split(string(c1), "\n")
	lines2 := strings.Split(string(c2), "\n")

	if brief {
		if string(c1) != string(c2) {
			fmt.Fprintf(w, "Files %s and %s differ\n", files[0], files[1])
			return 1
		}
		return 0
	}

	// Simple LCS-based diff
	diffs := simpleDiff(lines1, lines2)
	if len(diffs) == 0 {
		return 0
	}

	if unified {
		fmt.Fprintf(w, "--- %s\n", files[0])
		fmt.Fprintf(w, "+++ %s\n", files[1])
	}

	for _, d := range diffs {
		switch d.op {
		case '-':
			fmt.Fprintf(w, "-%s\n", d.line)
		case '+':
			fmt.Fprintf(w, "+%s\n", d.line)
		case ' ':
			fmt.Fprintf(w, " %s\n", d.line)
		}
	}

	return 1
}

type diffLine struct {
	op   byte
	line string
}

func simpleDiff(a, b []string) []diffLine {
	m, n := len(a), len(b)

	// LCS table
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] > dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	// Backtrack
	var result []diffLine
	i, j := m, n
	for i > 0 || j > 0 {
		if i > 0 && j > 0 && a[i-1] == b[j-1] {
			result = append(result, diffLine{' ', a[i-1]})
			i--
			j--
		} else if j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]) {
			result = append(result, diffLine{'+', b[j-1]})
			j--
		} else {
			result = append(result, diffLine{'-', a[i-1]})
			i--
		}
	}

	// Reverse
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	// Check if there are any actual differences
	hasDiff := false
	for _, d := range result {
		if d.op != ' ' {
			hasDiff = true
			break
		}
	}
	if !hasDiff {
		return nil
	}

	return result
}
