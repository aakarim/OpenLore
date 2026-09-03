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
	registry := rules.DefaultRegistry()
	if len(args) == 1 && args[0] == "list" {
		rules.WriteList(w, registry.All())
		return 0
	}
	if len(args) == 2 && args[0] == "doc" {
		name := args[1]
		members := rules.PackageMembers(registry, name)
		if member, ok := registry.Lookup(name); ok {
			rules.WriteDoc(w, member.Manifest())
			if len(members) != 0 {
				fmt.Fprintln(w, "\nMEMBERS")
				rules.WriteList(w, members)
			}
			return 0
		}
		if len(members) != 0 {
			rules.WriteList(w, members)
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
