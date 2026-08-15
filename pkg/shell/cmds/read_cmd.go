package cmds

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func CmdRead(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	prompt := ""
	raw := false
	arrayMode := false
	delimiter := "\n"
	nchars := -1
	var varNames []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p":
			if i+1 < len(args) {
				prompt = args[i+1]
				i++
			}
		case "-r":
			raw = true
		case "-a":
			arrayMode = true
			if i+1 < len(args) {
				varNames = append(varNames, args[i+1])
				i++
			}
		case "-d":
			if i+1 < len(args) {
				delimiter = args[i+1]
				i++
			}
		case "-n":
			if i+1 < len(args) {
				nchars, _ = strconv.Atoi(args[i+1])
				i++
			}
		default:
			if len(args[i]) > 1 && args[i][0] == '-' {
				ReportUnsupportedFlag(ctx, "read", args[i])
			}
			varNames = append(varNames, args[i])
		}
	}

	if prompt != "" {
		fmt.Fprint(w, prompt)
	}

	if stdin == nil {
		return 1
	}

	var line string
	if nchars > 0 {
		buf := make([]byte, nchars)
		n, err := io.ReadAtLeast(stdin, buf, 1)
		if err != nil && n == 0 {
			return 1
		}
		line = string(buf[:n])
	} else if delimiter == "\n" {
		reader := bufio.NewReader(stdin)
		var err error
		line, err = reader.ReadString('\n')
		if err != nil && line == "" {
			return 1
		}
		line = strings.TrimRight(line, "\n")
	} else {
		reader := bufio.NewReader(stdin)
		var err error
		line, err = reader.ReadString(delimiter[0])
		if err != nil && line == "" {
			return 1
		}
		line = strings.TrimRight(line, delimiter)
	}

	if !raw {
		line = strings.ReplaceAll(line, "\\\n", "")
	}

	if arrayMode {
		arrName := "REPLY"
		if len(varNames) > 0 {
			arrName = varNames[0]
		}
		fields := strings.Fields(line)
		for i, f := range fields {
			ctx.SetEnv(fmt.Sprintf("%s[%d]", arrName, i), f)
		}
		return 0
	}

	if len(varNames) == 0 {
		ctx.SetEnv("REPLY", line)
		return 0
	}

	fields := strings.Fields(line)
	for i, name := range varNames {
		if i < len(fields) {
			if i == len(varNames)-1 && i < len(fields)-1 {
				// Last variable gets the remainder
				ctx.SetEnv(name, strings.Join(fields[i:], " "))
			} else {
				ctx.SetEnv(name, fields[i])
			}
		} else {
			ctx.SetEnv(name, "")
		}
	}
	return 0
}
