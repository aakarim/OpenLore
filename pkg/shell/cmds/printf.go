package cmds

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

func CmdPrintf(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	if len(args) == 0 {
		fmt.Fprintln(errW, "printf: missing format string")
		return 1
	}

	if args[0] == "--" {
		args = args[1:]
		if len(args) == 0 {
			fmt.Fprintln(errW, "printf: missing format string")
			return 1
		}
	}
	format := args[0]
	fmtArgs := args[1:]

	// POSIX printf reuses the format while arguments remain. A format with no
	// conversions consumes nothing, so without the progress check below
	// `printf 'a' x` would loop forever and grow the output without bound.
	argIdx := 0
	for {
		before := argIdx
		fmt.Fprint(w, applyPrintf(format, fmtArgs, &argIdx))
		if argIdx >= len(fmtArgs) || argIdx == before {
			break
		}
	}
	return 0
}

func applyPrintf(format string, args []string, argIdx *int) string {
	var sb strings.Builder
	i := 0
	for i < len(format) {
		if format[i] == '\\' && i+1 < len(format) {
			switch format[i+1] {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case '\\':
				sb.WriteByte('\\')
			case '"':
				sb.WriteByte('"')
			case '0':
				// Octal
				j := i + 2
				for j < len(format) && j < i+5 && format[j] >= '0' && format[j] <= '7' {
					j++
				}
				if j > i+2 {
					val, _ := strconv.ParseUint(format[i+2:j], 8, 8)
					sb.WriteByte(byte(val))
					i = j
					continue
				}
				sb.WriteByte(0)
			default:
				sb.WriteByte('\\')
				sb.WriteByte(format[i+1])
			}
			i += 2
			continue
		}
		if format[i] == '%' && i+1 < len(format) {
			j := i + 1
			for j < len(format) && (format[j] == '-' || format[j] == '+' || format[j] == '0' || format[j] == ' ' || format[j] == '#') {
				j++
			}
			for j < len(format) && format[j] >= '0' && format[j] <= '9' {
				j++
			}
			if j < len(format) && format[j] == '.' {
				j++
				for j < len(format) && format[j] >= '0' && format[j] <= '9' {
					j++
				}
			}
			if j < len(format) {
				spec := format[i : j+1]
				ch := format[j]
				var arg string
				if *argIdx < len(args) {
					arg = args[*argIdx]
					*argIdx++
				}
				switch ch {
				case 'd', 'i':
					n, _ := strconv.ParseFloat(arg, 64)
					sb.WriteString(fmt.Sprintf(strings.Replace(spec, string(ch), "d", 1), int64(n)))
				case 'f', 'e', 'g':
					n, _ := strconv.ParseFloat(arg, 64)
					sb.WriteString(fmt.Sprintf(spec, n))
				case 's':
					sb.WriteString(fmt.Sprintf(spec, arg))
				case 'c':
					if len(arg) > 0 {
						sb.WriteByte(arg[0])
					}
				case 'x', 'o':
					n, _ := strconv.ParseFloat(arg, 64)
					sb.WriteString(fmt.Sprintf(spec, int64(n)))
				case '%':
					sb.WriteByte('%')
					*argIdx-- // no arg consumed
				default:
					sb.WriteString(spec)
				}
				i = j + 1
				continue
			}
		}
		sb.WriteByte(format[i])
		i++
	}
	return sb.String()
}
