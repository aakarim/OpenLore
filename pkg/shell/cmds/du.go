package cmds

import (
	"fmt"
	"io"
	"strings"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

func CmdDu(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	allFiles := false
	humanReadable := false
	summary := false
	grandTotal := false

	var targets []string
	for _, a := range args {
		switch a {
		case "-a":
			allFiles = true
		case "-h":
			humanReadable = true
		case "-s":
			summary = true
		case "-c":
			grandTotal = true
		default:
			if !strings.HasPrefix(a, "-") {
				targets = append(targets, a)
			}
		}
	}

	if len(targets) == 0 {
		targets = []string{ctx.Cwd()}
	}

	var total int64
	for _, target := range targets {
		p := ctx.Resolve(target)
		var size int64
		vfs.WalkDir(ctx.FS(), p, func(walkPath string, info *vfs.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.Dir {
				return nil
			}
			size += info.FileSize
			if allFiles && !summary {
				printDuSize(w, info.FileSize, walkPath, humanReadable)
			}
			return nil
		})
		if !allFiles || summary {
			printDuSize(w, size, target, humanReadable)
		}
		total += size
	}

	if grandTotal {
		printDuSize(w, total, "total", humanReadable)
	}
	return 0
}

func printDuSize(w io.Writer, bytes int64, name string, human bool) {
	if human {
		fmt.Fprintf(w, "%s\t%s\n", humanSize(bytes), name)
	} else {
		// Display in 1K blocks like du
		blocks := (bytes + 1023) / 1024
		fmt.Fprintf(w, "%d\t%s\n", blocks, name)
	}
}

func humanSize(b int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1fG", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1fM", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1fK", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%dB", b)
	}
}
