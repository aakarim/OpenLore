package rules

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// WriteList renders every registered member in lexical order.
func WriteList(w io.Writer, registry *Registry) {
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tKIND\tSCOPE\tSUMMARY")
	for _, member := range registry.All() {
		manifest := member.Manifest()
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", manifest.Path, manifest.Kind, manifest.Scope, manifest.Summary)
	}
	table.Flush()
}

// PackageMembers returns the registered members beneath a package path.
func PackageMembers(registry *Registry, packagePath string) []Member {
	prefix := strings.TrimSuffix(packagePath, "/") + "/"
	var members []Member
	for _, member := range registry.All() {
		if strings.HasPrefix(member.Manifest().Path, prefix) {
			members = append(members, member)
		}
	}
	return members
}

// WriteDoc renders one member's documentation from its manifest.
func WriteDoc(w io.Writer, manifest Manifest) {
	fmt.Fprintf(w, "%s — %s, scope: %s\n", manifest.Path, manifest.Kind, manifest.Scope)
	if manifest.Scope == ScopeFile {
		fmt.Fprintln(w, "evaluated: on every write (rejects) and by lore validate")
	} else {
		fmt.Fprintln(w, "evaluated: by lore validate only (never on write)")
	}
	fmt.Fprintln(w)
	if manifest.Doc != "" {
		fmt.Fprintln(w, manifest.Doc)
	} else {
		fmt.Fprintln(w, manifest.Summary+".")
	}
	if len(manifest.Params) != 0 {
		fmt.Fprintln(w, "\nPARAMETERS")
		for _, parameter := range manifest.Params {
			setting := "optional"
			if parameter.Required {
				setting = "required"
			} else if parameter.Default != nil {
				setting = fmt.Sprintf("default: %v", parameter.Default)
			}
			fmt.Fprintf(w, "  %-10s %-17s %-12s %s\n", parameter.Name, displayParamType(parameter.Type), setting, parameter.Doc)
		}
	}
	if manifest.Example != "" {
		fmt.Fprintln(w, "\nEXAMPLE")
		for _, line := range strings.Split(manifest.Example, "\n") {
			fmt.Fprintln(w, "  "+line)
		}
	}
}

func displayParamType(parameterType ParamType) string {
	if parameterType == ParamIntegerOrInitial {
		return `integer | "initial"`
	}
	return string(parameterType)
}
