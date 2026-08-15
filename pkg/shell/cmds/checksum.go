package cmds

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"hash"
	"io"
	"strings"
)

func CmdMd5sum(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	return hashSum(ctx, args, w, errW, stdin, "md5sum", md5.New)
}

func CmdSha1sum(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	return hashSum(ctx, args, w, errW, stdin, "sha1sum", sha1.New)
}

func CmdSha256sum(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	return hashSum(ctx, args, w, errW, stdin, "sha256sum", sha256.New)
}

func hashSum(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader, cmdName string, newHash func() hash.Hash) int {
	checkMode := false
	var files []string
	for _, a := range args {
		if a == "-c" || a == "--check" {
			checkMode = true
		} else if !strings.HasPrefix(a, "-") {
			files = append(files, a)
		}
	}

	if checkMode && len(files) > 0 {
		return verifyChecksums(ctx, files[0], w, errW, cmdName, newHash)
	}

	if len(files) == 0 {
		if stdin == nil {
			fmt.Fprintf(errW, "%s: missing input\n", cmdName)
			return 1
		}
		data, _ := io.ReadAll(stdin)
		h := newHash()
		h.Write(data)
		fmt.Fprintf(w, "%x  -\n", h.Sum(nil))
		return 0
	}

	exitCode := 0
	for _, f := range files {
		p := ctx.Resolve(f)
		data, err := ctx.FS().ReadFile(p)
		if err != nil {
			fmt.Fprintf(errW, "%s: %s: %s\n", cmdName, f, err)
			exitCode = 1
			continue
		}
		h := newHash()
		h.Write(data)
		fmt.Fprintf(w, "%x  %s\n", h.Sum(nil), f)
	}
	return exitCode
}

func verifyChecksums(ctx CmdContext, checksumFile string, w io.Writer, errW io.Writer, cmdName string, newHash func() hash.Hash) int {
	p := ctx.Resolve(checksumFile)
	data, err := ctx.FS().ReadFile(p)
	if err != nil {
		fmt.Fprintf(errW, "%s: %s: %s\n", cmdName, checksumFile, err)
		return 1
	}

	exitCode := 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 {
			continue
		}
		expectedHash := parts[0]
		fileName := parts[1]

		fp := ctx.Resolve(fileName)
		fileData, err := ctx.FS().ReadFile(fp)
		if err != nil {
			fmt.Fprintf(w, "%s: FAILED open or read\n", fileName)
			exitCode = 1
			continue
		}
		h := newHash()
		h.Write(fileData)
		actual := fmt.Sprintf("%x", h.Sum(nil))
		if actual == expectedHash {
			fmt.Fprintf(w, "%s: OK\n", fileName)
		} else {
			fmt.Fprintf(w, "%s: FAILED\n", fileName)
			exitCode = 1
		}
	}
	return exitCode
}
