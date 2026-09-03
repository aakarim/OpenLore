package cmds

import (
	"fmt"
	"io"

	"github.com/aakarim/go-openlore/pkg/rules"
	_ "github.com/aakarim/go-openlore/pkg/rules/link"
	_ "github.com/aakarim/go-openlore/pkg/rules/okfrule"
	_ "github.com/aakarim/go-openlore/pkg/rules/size"
)

func cmdLorePackage(_ CmdContext, args []string, w io.Writer, errW io.Writer, _ io.Reader) int {
	if len(args) == 1 && args[0] == "list" {
		rules.WriteList(w, rules.DefaultRegistry())
		return 0
	}
	if len(args) == 2 && args[0] == "doc" {
		name := args[1]
		if member, ok := rules.DefaultRegistry().Lookup(name); ok {
			rules.WriteDoc(w, member.Manifest())
			return 0
		}
		if members := rules.PackageMembers(rules.DefaultRegistry(), name); len(members) != 0 {
			for _, member := range members {
				manifest := member.Manifest()
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", manifest.Path, manifest.Kind, manifest.Scope, manifest.Summary)
			}
			return 0
		}
		fmt.Fprintf(errW, "lore package doc: unknown package member %q\n", name)
		return 1
	}
	fmt.Fprintln(errW, "usage: lore package list | lore package doc <path>")
	return 1
}

func init() {
	RegisterLoreSub(LoreSub{
		Name:    "package",
		Summary: "List and document compiled-in package members",
		Run:     cmdLorePackage,
	})
}
