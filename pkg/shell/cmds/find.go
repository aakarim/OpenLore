package cmds

import (
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

func CmdFind(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	root := ctx.Cwd()
	var namePattern string
	var predicates []struct{ flag, pattern string }
	minDepth, maxDepth := 0, -1
	followAliases := false
	matchedResults := 0
	matchedFiles := 0

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help":
			return CmdHelp(ctx, []string{"find"}, w, errW, stdin)
		case "-L":
			followAliases = true
		case "-name", "-iname", "-path", "-ipath", "-type", "-maxdepth", "-mindepth":
			flag := args[i]
			if i+1 >= len(args) {
				fmt.Fprintf(errW, "find: option requires an argument -- '%s'\n", flag[1:])
				return 1
			}
			value := args[i+1]
			i++
			if flag == "-maxdepth" || flag == "-mindepth" {
				depth, err := strconv.Atoi(value)
				if err != nil || value == "" || strings.Trim(value, "0123456789") != "" {
					fmt.Fprintf(errW, "find: invalid depth '%s' for %s; expected a non-negative integer\n", value, flag)
					return 1
				}
				if flag == "-maxdepth" {
					maxDepth = depth
				} else {
					minDepth = depth
				}
				continue
			}
			if flag == "-type" && value != "f" && value != "d" && value != "l" {
				fmt.Fprintf(errW, "find: unsupported type '%s'. This shell supports -type f|d|l.\n", value)
				return 1
			}
			if flag == "-name" || flag == "-iname" {
				namePattern = value
			}
			if flag == "-iname" || flag == "-ipath" {
				value = strings.ToLower(value)
			}
			predicates = append(predicates, struct{ flag, pattern string }{flag, value})
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(errW, "find: unsupported flag '%s'. Use 'find --help' for supported options, including -maxdepth N.\n", args[i])
				return 1
			}
			root = ctx.Resolve(args[i])
		}
	}

	root = vfs.CleanPath(root)
	var walk func(p string, info *vfs.FileInfo, depth int)
	walk = func(p string, info *vfs.FileInfo, depth int) {
		// Like find -P, an alias root below the starting point is reported as
		// a link and not descended into, so each file is listed once. -L
		// follows aliases; a starting point is always followed, like find -H.
		link := info.LinkTarget != "" && !followAliases && p != root
		kind := "f"
		if link {
			kind = "l"
		} else if info.Dir {
			kind = "d"
		}
		matched := depth >= minDepth
		for _, predicate := range predicates {
			if !matched {
				break
			}
			if predicate.flag == "-type" {
				matched = predicate.pattern == kind
				continue
			}
			value := info.FileName
			if predicate.flag == "-path" || predicate.flag == "-ipath" {
				value = p
			}
			if predicate.flag == "-iname" || predicate.flag == "-ipath" {
				value = strings.ToLower(value)
			}
			if predicate.flag == "-path" || predicate.flag == "-ipath" {
				matched = findPathMatch(predicate.pattern, value)
			} else {
				matched, _ = path.Match(predicate.pattern, value)
			}
		}
		if matched {
			fmt.Fprintln(w, p)
			matchedResults++
			if kind == "f" {
				matchedFiles++
			}
		}
		if kind != "d" || (maxDepth >= 0 && depth >= maxDepth) {
			return
		}
		entries, err := ctx.FS().ReadDir(p)
		if err != nil {
			return
		}
		for _, entry := range entries {
			childPath := path.Join(p, entry.FileName)
			if child, err := ctx.FS().Stat(childPath); err == nil {
				walk(childPath, child, depth+1)
			}
		}
	}
	if info, err := ctx.FS().Stat(root); err == nil {
		walk(root, info, 0)
	}
	if namePattern != "" {
		emitSearchMetric(ctx, namePattern, []string{root}, matchedFiles, 0, matchedResults > 0)
	}
	return 0
}

// findPathMatch uses path.Match's syntax, but lets * and ? match slashes.
// Character classes are matched unchanged so ranges retain their membership.
func findPathMatch(pattern, name string) bool {
	if _, err := path.Match(pattern, ""); err != nil {
		return false
	}
	var tokens []string
	for p := []rune(pattern); len(p) > 0; {
		end := 1
		switch p[0] {
		case '\\':
			end = 2
		case '[':
			for p[end] != ']' {
				if p[end] == '\\' {
					end++
				}
				end++
			}
			end++
		}
		tokens = append(tokens, string(p[:end]))
		p = p[end:]
	}
	chars := []rune(name)
	i, j, star, retry := 0, 0, -1, 0
	for j < len(chars) {
		if i < len(tokens) && tokens[i] == "*" {
			star, retry = i, j
			i++
			continue
		}
		if i < len(tokens) {
			matched, _ := path.Match(tokens[i], string(chars[j]))
			if tokens[i] == "?" || matched {
				i++
				j++
				continue
			}
		}
		if star < 0 {
			return false
		}
		// Extend the most recent star by one character and retry its suffix.
		retry++
		i, j = star+1, retry
	}
	for i < len(tokens) && tokens[i] == "*" {
		i++
	}
	return i == len(tokens)
}
