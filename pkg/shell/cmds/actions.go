package cmds

// Capability gating (Part B). Every command is classified by the kind of
// effect it has, and a Shell may be given an allowed set of actions for a
// session. A command whose action is not allowed is treated as if it does not
// exist, so an unauthorized agent cannot even discover the write/publish
// surface. The default classification is ActionRead — the safe, read-only
// bucket — so a newly added read command needs no registration.

// Action classifies what a command does, for per-session capability gating.
type Action string

const (
	// ActionRead is any non-mutating command (ls, cat, grep, …). Default.
	ActionRead Action = "read"
	// ActionWrite mutates docset content (write, patch, tee, `>`/`>>`, sed -i).
	ActionWrite Action = "write"
	// ActionPublish publishes new sources (publish, kb publish).
	ActionPublish Action = "publish"
	// ActionSpawn runs external commands asynchronously and writes their output
	// back into the lore (spawn). It is powerful — the command runs as the
	// OpenLore service user — so it is granted only to identities the operator
	// has explicitly trusted (capability "spawn"), never to anonymous sessions.
	ActionSpawn Action = "spawn"
	// ActionAdmin reconfigures the server (lore-* mutations, config writes).
	ActionAdmin Action = "admin"
)

// commandActions tags the non-read commands. Anything absent is ActionRead.
var commandActions = map[string]Action{
	"write":   ActionWrite,
	"patch":   ActionWrite,
	"tee":     ActionWrite,
	"mkdir":   ActionWrite,
	"mv":      ActionWrite,
	"rm":      ActionWrite,
	"publish": ActionPublish,
	"spawn":   ActionSpawn,
}

// ActionFor returns the capability class for a command name. Unclassified
// commands are ActionRead.
func ActionFor(name string) Action {
	if a, ok := commandActions[name]; ok {
		return a
	}
	return ActionRead
}

// RegisterAction tags a command with a capability class. Host applications that
// register custom commands (e.g. knowledge-backend's `kb` and `lore-*`) use
// this so the gating layer classifies them correctly.
func RegisterAction(name string, a Action) {
	commandActions[name] = a
}

// InvocationAction returns the effective capability class for a specific
// invocation (name + args). It is ActionFor(name) except where a flag turns an
// otherwise read-only command into a mutation — notably `sed -i`, which edits
// in place and is therefore a write.
func InvocationAction(name string, args []string) Action {
	if name == "sed" && hasInPlaceFlag(args) {
		return ActionWrite
	}
	return ActionFor(name)
}

// hasInPlaceFlag reports whether a sed-style arg list requests in-place editing
// (`-i`, `-i.bak`, or a bundled short flag containing `i` like `-ni`). Matching
// stops at `--` and at the first non-flag token (the script).
func hasInPlaceFlag(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if len(a) < 2 || a[0] != '-' {
			return false
		}
		// Scan the short-flag cluster for 'i' — matches -i, -i.bak, -ni, etc.
		for _, c := range a[1:] {
			if c == 'i' {
				return true
			}
		}
	}
	return false
}
