package cmds

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// LoreSub is a registered core `lore` subcommand.
type LoreSub struct {
	// Name is the subcommand word, e.g. "meta" in `lore meta`.
	Name string
	// Summary is the one-line description shown in `lore` usage.
	Summary string
	// Run executes the subcommand. args are the tokens after the subcommand
	// name (so `lore meta backend` calls Run with ["backend"]).
	Run CmdFunc
}

// loreSubs is the registry of `lore` subcommands, keyed by name.
var loreSubs = map[string]LoreSub{}

// RegisterLoreSub adds (or replaces) a core `lore` subcommand.
func RegisterLoreSub(sub LoreSub) {
	loreSubs[sub.Name] = sub
}

// CmdLore is the `lore` introspection dispatcher. Bare `lore` prints usage and
// exits 0; an unknown subcommand errors to stderr and exits 1. Subcommands are
// resolved from the loreSubs registry.
func CmdLore(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	if len(args) == 0 {
		printLoreUsage(w)
		return 0
	}
	if sub, ok := loreSubs[args[0]]; ok {
		return sub.Run(ctx, args[1:], w, errW, stdin)
	}
	fmt.Fprintf(errW, "lore: unknown command %q\n", args[0])
	printLoreUsage(errW)
	return 1
}

func printLoreUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: lore <command>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	subs := make([]LoreSub, 0, len(loreSubs))
	for _, s := range loreSubs {
		subs = append(subs, s)
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].Name < subs[j].Name })
	var nameW int
	for _, s := range subs {
		if len(s.Name) > nameW {
			nameW = len(s.Name)
		}
	}
	for _, s := range subs {
		fmt.Fprintf(w, "  %-*s   %s\n", nameW, s.Name, s.Summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'lore <command>' for a specific view.")
}

func init() {
	RegisterLoreSub(LoreSub{
		Name:    "docsets",
		Summary: "List the docsets you can access, their paths, and attributes",
		Run:     cmdLoreDocsets,
	})
	// `lore meta` is registered by the openlore package (its scanning logic is
	// domain logic, so it lives there and plugs into this dispatcher).
}

// cmdLoreDocsets prints an aligned, greppable table of the session's accessible
// docset mounts: name, grants, attribute tokens, display path, and canonical
// target for alias rows.
func cmdLoreDocsets(ctx CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
	docsets := ctx.Docsets()

	rows := [][5]string{{"DOCSET", "GRANTS", "ATTRIBUTES", "PATH", "TARGET"}}
	for _, d := range docsets {
		grant := strings.Join(d.Grants, ",")
		if grant == "" {
			grant = d.Grant
		}
		if grant == "" {
			grant = "ro"
		}
		var attrs []string
		if d.Home {
			attrs = append(attrs, "home")
		}
		if d.Inbox {
			attrs = append(attrs, "inbox")
		}
		if d.AgentSkills {
			attrs = append(attrs, "agent-skills")
		}
		if d.AliasTarget != "" {
			attrs = append(attrs, "alias")
		}
		attrStr := "-"
		if len(attrs) > 0 {
			attrStr = strings.Join(attrs, ",")
		}
		target := d.AliasTarget
		if target == "" {
			target = "-"
		}
		rows = append(rows, [5]string{d.Name, grant, attrStr, strings.Join(d.Paths, ","), target})
	}

	// Column widths from every cell except the last column (which is ragged).
	var w0, w1, w2, w3 int
	for _, r := range rows {
		if len(r[0]) > w0 {
			w0 = len(r[0])
		}
		if len(r[1]) > w1 {
			w1 = len(r[1])
		}
		if len(r[2]) > w2 {
			w2 = len(r[2])
		}
		if len(r[3]) > w3 {
			w3 = len(r[3])
		}
	}
	for _, r := range rows {
		fmt.Fprintf(w, "%-*s  %-*s  %-*s  %-*s  %s\n", w0, r[0], w1, r[1], w2, r[2], w3, r[3], r[4])
	}
	return 0
}
