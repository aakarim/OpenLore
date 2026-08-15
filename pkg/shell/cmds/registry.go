package cmds

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// CmdFunc is the signature for all shell commands.
type CmdFunc func(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int

// Registry maps command names to their implementations.
var Registry = map[string]CmdFunc{}

// UnsupportedFlagError identifies an option which is not implemented by a
// registered command.
type UnsupportedFlagError struct {
	Command string
	Flag    string
}

func (e *UnsupportedFlagError) Error() string {
	return fmt.Sprintf("%s: unsupported option %q", e.Command, e.Flag)
}

type optionSpec struct {
	short          string
	clusters       bool
	exact          map[string]bool
	values         map[string]bool
	reject         map[string]bool
	doubleDash     bool
	numericFlag    bool
	numericOperand bool
	stopAtOperand  bool
	plusOptions    bool
}

func spec(short string, exact, values []string) optionSpec {
	s := optionSpec{short: short, exact: map[string]bool{}, values: map[string]bool{}}
	for _, v := range exact {
		s.exact[v] = true
	}
	for _, v := range values {
		s.values[v] = true
	}
	return s
}

var invocationSpecs = map[string]optionSpec{
	"awk":       spec("", nil, []string{"-F", "-v"}),
	"base64":    spec("", []string{"-d", "--decode"}, nil),
	"cat":       spec("", nil, nil),
	"md5sum":    spec("", []string{"-c", "--check"}, nil),
	"sha1sum":   spec("", []string{"-c", "--check"}, nil),
	"sha256sum": spec("", []string{"-c", "--check"}, nil),
	"column":    spec("", []string{"-t"}, []string{"-s"}),
	"comm":      spec("", []string{"-1", "-2", "-3", "-12", "-13", "-23"}, nil),
	"cut":       spec("", []string{"-s"}, []string{"-d", "-f", "-c"}),
	"date":      spec("", []string{"-u"}, nil),
	"diff":      spec("", []string{"-u", "-q"}, nil),
	"du":        spec("", []string{"-a", "-h", "-s", "-c"}, nil),
	"expand":    spec("", nil, []string{"-t"}),
	"unexpand":  spec("", []string{"-a"}, []string{"-t"}),
	"find":      spec("", nil, []string{"-name", "-type"}),
	"fold":      spec("", []string{"-s"}, []string{"-w"}),
	"grep":      spec("inrRohcvl", nil, nil),
	"join":      spec("", nil, []string{"-1", "-2", "-t"}),
	"jq":        spec("rces", nil, nil),
	"ls":        spec("", []string{"-l", "-a", "-la", "-al"}, nil),
	"nl":        spec("", nil, []string{"-b", "-n", "-w", "-s"}),
	"paste":     spec("", []string{"-s"}, []string{"-d"}),
	"patch":     spec("", nil, nil),
	"read":      spec("", []string{"-r"}, []string{"-p", "-a", "-d", "-n"}),
	"sed":       spec("", []string{"-n", "-i", "--in-place"}, []string{"-e"}),
	"sort":      spec("rnuf", nil, []string{"-k", "-t"}),
	"stat":      spec("", nil, nil),
	"tee":       spec("", []string{"-a", "--append"}, nil),
	"tree":      spec("", nil, []string{"-L"}),
	"uniq":      spec("", []string{"-c", "-d", "-i", "-u"}, nil),
	"wc":        spec("lwcm", nil, nil),
	"pwd":       spec("", nil, nil),
}

func init() {
	s := spec("", nil, nil)
	s.reject = map[string]bool{"-e": true, "-x": true, "-u": true, "+e": true, "+x": true, "+u": true}
	s.doubleDash = true
	s.plusOptions = true
	invocationSpecs["set"] = s

	s = spec("", []string{"-p", "--parents"}, nil)
	s.doubleDash = true
	invocationSpecs["mkdir"] = s

	s = spec("", []string{"-r", "-R", "-f", "-rf", "-fr", "-Rf", "-fR", "--recursive", "--force"}, nil)
	s.doubleDash = true
	invocationSpecs["rm"] = s

	for _, name := range []string{"head", "tail"} {
		s = spec("", nil, []string{"-n"})
		s.numericFlag = true
		invocationSpecs[name] = s
	}

	for _, name := range []string{"grep", "jq", "sort", "wc"} {
		s = invocationSpecs[name]
		s.clusters = true
		invocationSpecs[name] = s
	}

	s = spec("", []string{"-0"}, []string{"-I", "-d", "-n"})
	s.stopAtOperand = true
	invocationSpecs["xargs"] = s

	s = spec("", []string{"-w"}, []string{"-s"})
	s.numericOperand = true
	invocationSpecs["seq"] = s

	s = spec("", []string{"-d", "-s", "-c", "-C"}, nil)
	s.stopAtOperand = true
	invocationSpecs["tr"] = s
}

// ValidateInvocation checks options without changing Registry's public shape.
func ValidateInvocation(command string, args []string) error {
	s, ok := invocationSpecs[command]
	if !ok {
		return nil
	}
	options := true
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !options || a == "-" {
			continue
		}
		if a == "--" && s.doubleDash {
			options = false
			continue
		}
		if s.reject[a] {
			return &UnsupportedFlagError{Command: command, Flag: a}
		}
		if s.plusOptions && strings.HasPrefix(a, "+") && len(a) > 1 {
			return &UnsupportedFlagError{Command: command, Flag: a}
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			if s.stopAtOperand {
				options = false
			}
			continue
		}
		if s.numericOperand {
			if _, err := strconv.ParseFloat(a, 64); err == nil {
				continue
			}
		}
		if s.exact[a] {
			continue
		}
		if s.values[a] {
			if i+1 < len(args) {
				i++
			}
			continue
		}
		if s.numericFlag {
			if isNumericFlag(a) {
				continue
			}
		}
		if s.clusters && len(a) > 1 && !strings.HasPrefix(a, "--") {
			for _, ch := range a[1:] {
				if !strings.ContainsRune(s.short, ch) {
					return &UnsupportedFlagError{Command: command, Flag: "-" + string(ch)}
				}
			}
			continue
		}
		return &UnsupportedFlagError{Command: command, Flag: a}
	}
	return nil
}

func isNumericFlag(arg string) bool {
	if len(arg) < 2 || arg[0] != '-' || arg[1] == '-' {
		return false
	}
	for _, ch := range arg[1:] {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func init() {
	// filesystem
	Register("ls", CmdLs)
	Register("cat", CmdCat)
	Register("head", CmdHead)
	Register("tail", CmdTail)
	Register("find", CmdFind)
	Register("grep", CmdGrep)
	Register("wc", CmdWc)
	Register("cd", CmdCd)
	Register("tree", CmdTree)
	Register("stat", CmdStat)
	Register("mkdir", CmdMkdir)
	Register("mv", CmdMv)
	Register("rm", CmdRm)
	// text
	Register("sort", CmdSort)
	Register("uniq", CmdUniq)
	Register("cut", CmdCut)
	Register("sed", CmdSed)
	Register("awk", CmdAwk)
	Register("tr", CmdTr)
	Register("rev", CmdRev)
	Register("tac", CmdTac)
	Register("nl", CmdNl)
	Register("fold", CmdFold)
	Register("paste", CmdPaste)
	Register("column", CmdColumn)
	Register("diff", CmdDiff)
	Register("join", CmdJoin)
	Register("comm", CmdComm)
	Register("expand", CmdExpand)
	Register("unexpand", CmdUnexpand)
	// utils
	Register("xargs", CmdXargs)
	Register("seq", CmdSeq)
	Register("printf", CmdPrintf)
	Register("date", CmdDate)
	Register("basename", CmdBasename)
	Register("dirname", CmdDirname)
	Register("tee", CmdTee)
	Register("base64", CmdBase64)
	Register("md5sum", CmdMd5sum)
	Register("sha1sum", CmdSha1sum)
	Register("sha256sum", CmdSha256sum)
	Register("du", CmdDu)
	Register("which", CmdWhich)
	Register("env", CmdEnv)
	Register("printenv", CmdPrintenv)
	Register("whoami", CmdWhoami)
	Register("hostname", CmdHostname)
	Register("true", CmdTrue)
	Register("false", CmdFalse)
	Register("clear", CmdClear)
	Register("sleep", CmdSleep)
	Register("timeout", CmdTimeout)
	Register("time", CmdTime)
	Register("expr", CmdExpr)
	Register("history", CmdHistory)
	Register("alias", CmdAlias)
	Register("unalias", CmdUnalias)
	Register("type", CmdType)
	Register("command", CmdCommand)
	// data
	Register("jq", CmdJq)
	// builtins
	Register("echo", CmdEcho)
	Register("test", CmdTest)
	Register("[", CmdBracket)
	Register("read", CmdRead)
	Register("export", CmdExport)
	Register("unset", CmdUnset)
	Register("set", CmdSet)
	Register("source", CmdSource)
	Register(".", CmdSource)
	Register("eval", CmdEval)
	// help
	Register("help", CmdHelp)
	Register("skills", CmdSkills)
	Register("version", CmdVersion)
	// writes
	Register("patch", CmdPatch)
	// publishing
	Register("publish", CmdPublish)
	// async external work (Part D)
	Register("spawn", CmdSpawn)
	// identity
	Register("whoami", CmdWhoami)
	// introspection
	Register("lore", CmdLore)
}

// Register adds a command to the registry.
func Register(name string, fn CmdFunc) {
	Registry[name] = fn
}

// IsKnown returns true if the command name is registered.
func IsKnown(name string) bool {
	_, ok := Registry[name]
	return ok
}
