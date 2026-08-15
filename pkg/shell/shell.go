package shell

import (
	"bytes"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/aakarim/go-openlore/pkg/openlore/meta"
	"github.com/aakarim/go-openlore/pkg/openlore/validation"
	"github.com/aakarim/go-openlore/pkg/shell/cmds"
	"github.com/aakarim/go-openlore/pkg/shell/parser"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

// Shell is a restricted bash-like shell that operates over any vfs.FileSystem.
type Shell struct {
	fs  vfs.FileSystem
	cwd string
	env map[string]string
	// allowedActions is the per-session capability set (Part B). nil means
	// "unrestricted" — every action is allowed (the default, backward
	// compatible). When non-nil, a command (or redirect) whose capability
	// class is absent is treated as if it does not exist.
	allowedActions   map[cmds.Action]bool
	actionAuthorizer func(cmds.Action) bool
	// conflictPolicyFn resolves the write-conflict policy for a resolved path.
	// nil means "use the default" (vfs.PolicyHash). The server sets this to a
	// per-docset resolver; standalone shells get the default.
	conflictPolicyFn func(resolvedPath string) vfs.WriteConflictPolicy
	// docsets and publishTargets are the per-session views the host computes
	// from the identity's lore. Read by `lore docsets` and `publish`
	// respectively. nil for a standalone shell.
	docsets        []cmds.DocsetInfo
	publishTargets []cmds.PublishTarget
	// metaExtenders are the plugin-contributed extenders applied by `lore meta`,
	// installed by the host per session. nil for a standalone shell.
	metaExtenders []meta.Extender
	metaFilters   []meta.Filter
	// validators are plugin-contributed checks applied by `lore validate`.
	validators           []validation.Validator
	skillsManagement     bool
	skillsRemoteTimeout  time.Duration
	skillsRemoteMaxBytes int64
}

// NewShell creates a new Shell backed by the given vfs.FileSystem.
func NewShell(fs vfs.FileSystem) *Shell {
	return &Shell{
		fs:  fs,
		cwd: "/",
	}
}

// SetAllowedActions restricts the shell to the given capability classes
// (Part B). Passing nil (or not calling this) leaves the shell unrestricted.
// ActionRead is always implied so that read-only commands and introspection
// (help, ls, …) keep working.
func (s *Shell) SetAllowedActions(actions []cmds.Action) {
	set := map[cmds.Action]bool{cmds.ActionRead: true}
	for _, a := range actions {
		set[a] = true
	}
	s.allowedActions = set
}

// ActionAllowed reports whether the session may perform the capability class.
// An unrestricted shell (allowedActions == nil) permits everything.
func (s *Shell) ActionAllowed(a cmds.Action) bool {
	if s.actionAuthorizer != nil && !s.actionAuthorizer(a) {
		return false
	}
	if s.allowedActions == nil {
		return true
	}
	return s.allowedActions[a]
}

// SetActionAuthorizer installs a per-invocation narrowing check.
func (s *Shell) SetActionAuthorizer(fn func(cmds.Action) bool) { s.actionAuthorizer = fn }

// SetConflictPolicyFn installs a resolver that maps a resolved path to its
// write-conflict policy. Passing nil restores the default (vfs.PolicyHash).
func (s *Shell) SetConflictPolicyFn(fn func(resolvedPath string) vfs.WriteConflictPolicy) {
	s.conflictPolicyFn = fn
}

// WriteConflictPolicy reports the policy for overwrites to resolvedPath. With
// no resolver installed it returns the default compare-and-swap policy.
func (s *Shell) WriteConflictPolicy(resolvedPath string) vfs.WriteConflictPolicy {
	if s.conflictPolicyFn == nil {
		return vfs.DefaultWriteConflictPolicy
	}
	return s.conflictPolicyFn(resolvedPath)
}

// SetDocsets installs the per-session docset views surfaced by `lore docsets`.
func (s *Shell) SetDocsets(d []cmds.DocsetInfo) { s.docsets = d }

// Docsets reports the per-session docset views. Implements CmdContext.
func (s *Shell) Docsets() []cmds.DocsetInfo { return s.docsets }

// SetPublishTargets installs the per-session publish inboxes used by `publish`.
func (s *Shell) SetPublishTargets(t []cmds.PublishTarget) { s.publishTargets = t }

// PublishTargets reports the per-session publish inboxes. Implements CmdContext.
func (s *Shell) PublishTargets() []cmds.PublishTarget { return s.publishTargets }

// SetMetaExtenders installs the plugin-contributed extenders applied by `lore
// meta`.
func (s *Shell) SetMetaExtenders(e []meta.Extender) { s.metaExtenders = e }

// MetaExtenders reports the per-session `lore meta` extenders. Implements
// CmdContext.
func (s *Shell) MetaExtenders() []meta.Extender { return s.metaExtenders }
func (s *Shell) SetMetaFilters(f []meta.Filter) { s.metaFilters = f }
func (s *Shell) MetaFilters() []meta.Filter     { return s.metaFilters }

// SetValidators installs plugin-contributed checks for `lore validate`.
func (s *Shell) SetValidators(validators []validation.Validator) { s.validators = validators }

// Validators reports this shell's plugin-contributed validation checks.
func (s *Shell) Validators() []validation.Validator      { return s.validators }
func (s *Shell) SetSkillsManagementEnabled(enabled bool) { s.skillsManagement = enabled }
func (s *Shell) SkillsManagementEnabled() bool           { return s.skillsManagement }
func (s *Shell) SetSkillsRemoteConfig(timeout time.Duration, maxBytes int64) {
	s.skillsRemoteTimeout, s.skillsRemoteMaxBytes = timeout, maxBytes
}
func (s *Shell) SkillsRemoteTimeout() time.Duration { return s.skillsRemoteTimeout }
func (s *Shell) SkillsRemoteMaxBytes() int64        { return s.skillsRemoteMaxBytes }

// --- CmdContext interface implementation ---

func (s *Shell) FS() vfs.FileSystem { return s.fs }
func (s *Shell) Cwd() string        { return s.cwd }
func (s *Shell) SetCwd(dir string)  { s.cwd = dir }
func (s *Shell) Resolve(p string) string {
	if strings.HasPrefix(p, "/") {
		return path.Clean(p)
	}
	return path.Clean(path.Join(s.cwd, p))
}

func (s *Shell) GetEnv(key string) string {
	if s.env == nil {
		return ""
	}
	return s.env[key]
}

func (s *Shell) SetEnv(key, value string) {
	if s.env == nil {
		s.env = make(map[string]string)
	}
	s.env[key] = value
}

func (s *Shell) DeleteEnv(key string) {
	if s.env != nil {
		delete(s.env, key)
	}
}

func (s *Shell) AllEnv() map[string]string {
	if s.env == nil {
		return map[string]string{}
	}
	return s.env
}

// Exec parses and executes a single command line.
func (s *Shell) Exec(cmdLine string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	return s.execLine(cmdLine, w, errW, stdin)
}

// ExecPipeline parses a shell line and executes the resulting AST.
// stdin is optional — pass nil if no external stdin is available.
func (s *Shell) ExecPipeline(line string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	return s.execLine(line, w, errW, stdin)
}

func (s *Shell) execLine(line string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	line = strings.TrimSpace(line)
	if line == "" {
		return 0
	}

	f, err := parser.Parse(line)
	if err != nil {
		fmt.Fprintf(errW, "parse error: %s\n", err)
		return 2
	}

	var exitCode int
	for _, stmt := range f.Stmts {
		exitCode = s.execStmt(stmt, w, errW, stdin)
	}
	return exitCode
}

func (s *Shell) execStmt(stmt *parser.Stmt, w io.Writer, errW io.Writer, stdin io.Reader) int {
	if stmt.Cmd == nil {
		return 0
	}

	code := s.execCmd(stmt.Cmd, w, errW, stdin)

	if stmt.Negated {
		if code == 0 {
			code = 1
		} else {
			code = 0
		}
	}

	return code
}

func (s *Shell) execCmd(cmd parser.Command, w io.Writer, errW io.Writer, stdin io.Reader) int {
	switch c := cmd.(type) {
	case *parser.CallExpr:
		return s.execCall(c, w, errW, stdin)

	case *parser.BinaryCmd:
		return s.execBinary(c, w, errW, stdin)

	case *parser.Subshell:
		var code int
		for _, st := range c.Stmts {
			code = s.execStmt(st, w, errW, stdin)
		}
		return code

	case *parser.Block:
		var code int
		for _, st := range c.Stmts {
			code = s.execStmt(st, w, errW, stdin)
		}
		return code

	case *parser.IfClause:
		return s.execIf(c, w, errW, stdin)

	case *parser.WhileClause:
		return s.execWhile(c, w, errW, stdin)

	case *parser.ForClause:
		return s.execFor(c, w, errW, stdin)

	case *parser.DeclClause:
		return s.execDecl(c, w, errW, stdin)

	case *parser.TestClause:
		return s.execTestClause(c, w, errW, stdin)

	case *parser.TimeClause:
		if c.Stmt != nil {
			start := time.Now()
			code := s.execStmt(c.Stmt, w, errW, stdin)
			elapsed := time.Since(start)
			fmt.Fprintf(errW, "\nreal\t%s\n", elapsed.Round(time.Millisecond))
			return code
		}
		return 0

	case *parser.LetClause:
		return 0

	case *parser.ArithmCmd:
		return 0

	default:
		fmt.Fprintf(errW, "unsupported syntax: %T\n", cmd)
		return 2
	}
}

func (s *Shell) execCall(call *parser.CallExpr, w io.Writer, errW io.Writer, stdin io.Reader) int {
	// A `> file` / `>> file` redirection buffers the command's stdout and
	// commits it as a single atomic whole-object write. Nothing is committed
	// unless the command succeeds (commit-on-success only — no half-files).
	if call.Redirect != nil {
		target := s.expandWord(call.Redirect.Target)
		// /dev/null is a built-in sink, not a path in the virtual filesystem.
		// It does not require write capability because it cannot mutate state.
		if target == "/dev/null" {
			return s.execCallInner(call, io.Discard, errW, stdin)
		}
		// A file redirect is a write; gate it at parse time (Part B) so a
		// read-only session cannot use `>`/`>>` to mutate a docset.
		if !s.ActionAllowed(cmds.ActionWrite) {
			fmt.Fprintf(errW, "redirect: %s: read-only filesystem\n", target)
			return 1
		}
		var buf bytes.Buffer
		code := s.execCallInner(call, &buf, errW, stdin)
		if code != 0 {
			return code
		}
		return cmds.WriteFileMsg(s, errW, "redirect", target, buf.Bytes(), call.Redirect.Append)
	}
	return s.execCallInner(call, w, errW, stdin)
}

func (s *Shell) execCallInner(call *parser.CallExpr, w io.Writer, errW io.Writer, stdin io.Reader) int {
	if len(call.Args) == 0 {
		for _, assign := range call.Assigns {
			s.SetEnv(assign.Name.Value, s.expandWord(assign.Value))
		}
		return 0
	}

	args := make([]string, 0, len(call.Args))
	for _, word := range call.Args {
		expanded := s.expandWord(word)
		// Only glob-expand unquoted words that have a directory component
		// (e.g. /docs/*.md but not *.md alone, which is likely a find pattern)
		if !isQuotedWord(word) && strings.ContainsAny(expanded, "*?") && strings.Contains(expanded, "/") {
			matches := s.globExpand(expanded)
			if len(matches) > 0 {
				args = append(args, matches...)
				continue
			}
		}
		args = append(args, expanded)
	}

	cmdName := args[0]
	cmdArgs := args[1:]

	// A heredoc on the command replaces stdin with the heredoc body. We
	// concatenate multiple heredoc bodies in declaration order to match bash.
	if len(call.Heredocs) > 0 {
		var buf bytes.Buffer
		for _, hd := range call.Heredocs {
			buf.WriteString(hd.Body)
		}
		stdin = &buf
	}

	// `2>&1` routes the command's stderr into the same writer as stdout.
	if call.MergeStderr {
		errW = w
	}

	if cmdName == "pwd" {
		fmt.Fprintln(w, s.cwd)
		return 0
	}

	if fn, ok := cmds.Registry[cmdName]; ok {
		// Capability gating (Part B): a command whose action the session is
		// not allowed to perform is hidden — it reports "command not found"
		// so the restricted surface is not even discoverable.
		if !s.ActionAllowed(cmds.InvocationAction(cmdName, cmdArgs)) {
			fmt.Fprintf(errW, "%s: command not found\n", cmdName)
			fmt.Fprintln(errW, "Type 'help' for available commands.")
			return 127
		}
		return fn(s, cmdArgs, w, errW, stdin)
	}

	fmt.Fprintf(errW, "%s: command not found\n", cmdName)
	fmt.Fprintln(errW, "Type 'help' for available commands.")
	return 127
}

func (s *Shell) execBinary(bc *parser.BinaryCmd, w io.Writer, errW io.Writer, stdin io.Reader) int {
	switch bc.Op {
	case parser.Pipe:
		var buf bytes.Buffer
		s.execStmt(bc.X, &buf, errW, stdin)
		return s.execStmt(bc.Y, w, errW, &buf)

	case parser.PipeAll:
		var buf bytes.Buffer
		s.execStmt(bc.X, &buf, &buf, stdin)
		return s.execStmt(bc.Y, w, errW, &buf)

	case parser.AndStmt:
		code := s.execStmt(bc.X, w, errW, stdin)
		if code != 0 {
			return code
		}
		return s.execStmt(bc.Y, w, errW, stdin)

	case parser.OrStmt:
		code := s.execStmt(bc.X, w, errW, stdin)
		if code == 0 {
			return 0
		}
		return s.execStmt(bc.Y, w, errW, stdin)

	default:
		fmt.Fprintf(errW, "unsupported operator: %d\n", bc.Op)
		return 2
	}
}

func (s *Shell) execIf(ic *parser.IfClause, w io.Writer, errW io.Writer, stdin io.Reader) int {
	// Plain else: Cond is nil
	if ic.Cond == nil {
		var code int
		for _, st := range ic.Then {
			code = s.execStmt(st, w, errW, stdin)
		}
		return code
	}

	var condCode int
	for _, st := range ic.Cond {
		condCode = s.execStmt(st, io.Discard, errW, stdin)
	}

	if condCode == 0 {
		var code int
		for _, st := range ic.Then {
			code = s.execStmt(st, w, errW, stdin)
		}
		return code
	}

	if ic.Else != nil {
		return s.execIf(ic.Else, w, errW, stdin)
	}

	return 0
}

func (s *Shell) execWhile(wc *parser.WhileClause, w io.Writer, errW io.Writer, stdin io.Reader) int {
	var code int
	for i := 0; i < 10000; i++ {
		var condCode int
		for _, st := range wc.Cond {
			condCode = s.execStmt(st, io.Discard, errW, stdin)
		}
		shouldRun := condCode == 0
		if wc.Until {
			shouldRun = condCode != 0
		}
		if !shouldRun {
			break
		}
		for _, st := range wc.Do {
			code = s.execStmt(st, w, errW, stdin)
		}
	}
	return code
}

func (s *Shell) execFor(fc *parser.ForClause, w io.Writer, errW io.Writer, stdin io.Reader) int {
	varName := fc.Loop.Name.Value
	var items []string
	for _, word := range fc.Loop.Items {
		expanded := s.expandWord(word)
		if parser.ContainsExpansion(word) {
			items = append(items, strings.Fields(expanded)...)
		} else {
			items = append(items, expanded)
		}
	}

	var code int
	for _, item := range items {
		s.SetEnv(varName, item)
		for _, st := range fc.Do {
			code = s.execStmt(st, w, errW, stdin)
		}
	}
	return code
}

func (s *Shell) execDecl(dc *parser.DeclClause, w io.Writer, errW io.Writer, stdin io.Reader) int {
	cmdName := dc.Variant.Value

	var args []string
	for _, assign := range dc.Args {
		if assign.Naked {
			args = append(args, assign.Name.Value)
		} else if assign.Value != nil {
			args = append(args, assign.Name.Value+"="+s.expandWord(assign.Value))
		} else {
			args = append(args, assign.Name.Value)
		}
	}

	if fn, ok := cmds.Registry[cmdName]; ok {
		return fn(s, args, w, errW, stdin)
	}

	for _, assign := range dc.Args {
		if assign.Value != nil {
			s.SetEnv(assign.Name.Value, s.expandWord(assign.Value))
		}
	}
	return 0
}

func (s *Shell) execTestClause(tc *parser.TestClause, w io.Writer, errW io.Writer, stdin io.Reader) int {
	var args []string
	for _, word := range tc.Words {
		args = append(args, s.expandWord(word))
	}
	if fn, ok := cmds.Registry["test"]; ok {
		return fn(s, args, w, errW, stdin)
	}
	return 1
}

// --- word/variable expansion ---

func (s *Shell) expandWord(word *parser.Word) string {
	if word == nil {
		return ""
	}
	var sb strings.Builder
	for i, part := range word.Parts {
		str := s.expandPart(part)
		// Tilde expansion applies only to an unquoted literal at the start of
		// a word: `~` or `~/…` becomes $HOME. Quoted parts are untouched.
		if i == 0 {
			if _, ok := part.(*parser.Lit); ok {
				str = s.expandTilde(str)
			}
		}
		sb.WriteString(str)
	}
	return sb.String()
}

// expandTilde replaces a leading ~ (as `~` or `~/…`) with $HOME. If HOME is
// unset the tilde is left untouched, matching bash.
func (s *Shell) expandTilde(v string) string {
	home := s.GetEnv("HOME")
	if home == "" {
		return v
	}
	if v == "~" {
		return home
	}
	if strings.HasPrefix(v, "~/") {
		return home + v[1:]
	}
	return v
}

func (s *Shell) expandPart(part parser.WordPart) string {
	switch p := part.(type) {
	case *parser.Lit:
		return p.Value

	case *parser.SglQuoted:
		return p.Value

	case *parser.DblQuoted:
		var sb strings.Builder
		for _, sub := range p.Parts {
			sb.WriteString(s.expandPart(sub))
		}
		return sb.String()

	case *parser.ParamExp:
		return s.expandParam(p)

	case *parser.CmdSubst:
		var buf bytes.Buffer
		for _, st := range p.Stmts {
			s.execStmt(st, &buf, io.Discard, nil)
		}
		return strings.TrimRight(buf.String(), "\n")

	default:
		return ""
	}
}

func (s *Shell) expandParam(pe *parser.ParamExp) string {
	name := pe.Param.Value

	switch name {
	case "?":
		return "0"
	case "#":
		return s.GetEnv("#")
	case "0":
		return "lore-shell"
	}

	if pe.Length {
		return fmt.Sprintf("%d", len(s.GetEnv(name)))
	}

	val := s.GetEnv(name)

	if pe.Exp != nil {
		word := s.expandWord(pe.Exp.Word)
		switch pe.Exp.Op {
		case parser.DefaultUnset:
			if val == "" {
				return word
			}
		case parser.DefaultUnsetOrNull:
			if val == "" {
				return word
			}
		case parser.AlternateUnset:
			if val != "" {
				return word
			}
			return ""
		case parser.AlternateUnsetOrNull:
			if val != "" {
				return word
			}
			return ""
		case parser.AssignUnset:
			if val == "" {
				s.SetEnv(name, word)
				return word
			}
		case parser.AssignUnsetOrNull:
			if val == "" {
				s.SetEnv(name, word)
				return word
			}
		case parser.ErrorUnset, parser.ErrorUnsetOrNull:
			if val == "" {
				return ""
			}
		}
	}

	return val
}

// isQuotedWord returns true if the word is wrapped in quotes (single or double).
func isQuotedWord(word *parser.Word) bool {
	if word == nil || len(word.Parts) == 0 {
		return false
	}
	for _, part := range word.Parts {
		switch part.(type) {
		case *parser.SglQuoted, *parser.DblQuoted:
			return true
		}
	}
	return false
}

// globExpand expands a glob pattern against the virtual filesystem.
func (s *Shell) globExpand(pattern string) []string {
	dir := path.Dir(s.Resolve(pattern))
	base := path.Base(pattern)

	entries, err := s.fs.ReadDir(dir)
	if err != nil {
		return nil
	}

	var matches []string
	for _, entry := range entries {
		matched, _ := path.Match(base, entry.FileName)
		if matched {
			matches = append(matches, path.Join(dir, entry.FileName))
		}
	}
	sort.Strings(matches)
	return matches
}

// RunInteractive runs an interactive shell session.
// rw is used for both reading input and writing output. When running over
// an SSH session with an allocated PTY, the session's ptyWriter already
// converts \n to \r\n, so no additional CRLFWriter wrapping is needed.
func (s *Shell) RunInteractive(rw io.ReadWriter, errW io.Writer, motd string, prompt string) {
	if motd != "" {
		fmt.Fprintln(rw, motd)
		fmt.Fprintln(rw, "")
	}

	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1)
	lastWasCR := false

	printPrompt := func() {
		fmt.Fprintf(rw, "%s:%s $ ", prompt, s.cwd)
	}

	printPrompt()

	for {
		n, err := rw.Read(tmp)
		if err != nil {
			break
		}
		if n == 0 {
			continue
		}

		ch := tmp[0]

		// Skip \n immediately following \r (SSH sends \r\n)
		if ch == '\n' && lastWasCR {
			lastWasCR = false
			continue
		}
		lastWasCR = ch == '\r'

		switch {
		case ch == 4: // Ctrl-D
			fmt.Fprintln(rw, "\r\nGoodbye!")
			return
		case ch == 3: // Ctrl-C
			buf = buf[:0]
			fmt.Fprint(rw, "\r\n")
			printPrompt()
		case ch == 127 || ch == 8: // Backspace
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
				rw.Write([]byte("\b \b"))
			}
		case ch == '\r' || ch == '\n':
			fmt.Fprint(rw, "\r\n")
			line := strings.TrimSpace(string(buf))
			buf = buf[:0]
			if line == "exit" || line == "quit" {
				fmt.Fprintln(rw, "Goodbye!")
				return
			}
			if line != "" {
				s.ExecPipeline(line, rw, rw, nil)
			}
			printPrompt()
		case ch == '\t': // Tab — ignore
		default:
			buf = append(buf, ch)
			rw.Write([]byte{ch})
		}
	}
}

// SplitArgs splits a command line into args, respecting single and double quotes.
func SplitArgs(line string) []string {
	var args []string
	var current strings.Builder
	inSingle := false
	inDouble := false

	for i := 0; i < len(line); i++ {
		ch := line[i]
		switch {
		case ch == '\'' && !inDouble:
			inSingle = !inSingle
		case ch == '"' && !inSingle:
			inDouble = !inDouble
		case ch == ' ' && !inSingle && !inDouble:
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}
