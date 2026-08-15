package cmds

import (
	"io"
)

// CmdFunc is the signature for all shell commands.
type CmdFunc func(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int

// Registry maps command names to their implementations.
var Registry = map[string]CmdFunc{}

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
