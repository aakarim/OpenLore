package cmds

import (
	"encoding/base64"
	"fmt"
	"io"
	"strings"
)

func CmdBase64(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	decode := false
	var files []string
	for _, a := range args {
		if a == "-d" || a == "--decode" {
			decode = true
		} else if !strings.HasPrefix(a, "-") {
			files = append(files, a)
		}
	}

	var data []byte
	if len(files) > 0 {
		p := ctx.Resolve(files[0])
		var err error
		data, err = ctx.FS().ReadFile(p)
		if err != nil {
			fmt.Fprintf(errW, "base64: %s: %s\n", files[0], err)
			return 1
		}
	} else if stdin != nil {
		data, _ = io.ReadAll(stdin)
	} else {
		fmt.Fprintln(errW, "base64: missing input")
		return 1
	}

	if decode {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil {
			fmt.Fprintf(errW, "base64: invalid input: %s\n", err)
			return 1
		}
		w.Write(decoded)
	} else {
		encoded := base64.StdEncoding.EncodeToString(data)
		fmt.Fprintln(w, encoded)
	}
	return 0
}
