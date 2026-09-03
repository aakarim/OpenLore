package cmds

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/aakarim/go-openlore/pkg/openlore/validation"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

func cmdLoreValidate(ctx CmdContext, args []string, w io.Writer, errW io.Writer, _ io.Reader) int {
	root := ctx.Cwd()
	if len(args) > 1 {
		fmt.Fprintln(errW, "Usage: lore validate [bundle]")
		return 1
	}
	if len(args) == 1 {
		if strings.HasPrefix(args[0], "-") {
			fmt.Fprintf(errW, "lore validate: unknown flag %q\n", args[0])
			return 1
		}
		root = ctx.Resolve(args[0])
	}
	root = vfs.CleanPath(root)
	info, err := ctx.FS().Stat(root)
	if err != nil {
		fmt.Fprintf(errW, "lore validate: %s\n", err)
		return 1
	}
	if !info.Dir {
		fmt.Fprintf(errW, "lore validate: %s is not a bundle directory\n", root)
		return 1
	}

	validators := ctx.Validators()
	if len(validators) == 0 {
		fmt.Fprintln(errW, "lore validate: warning: no validators enabled")
		return 0
	}
	diagnostics, err := validation.Scan(ctx.FS(), root, validators...)
	if err != nil {
		fmt.Fprintf(errW, "lore validate: %s\n", err)
		return 1
	}
	errors, warnings := 0, 0
	members := map[string]bool{}
	for _, diagnostic := range diagnostics {
		if diagnostic.Rule == "rules/bundle-root" {
			fmt.Fprintf(errW, "lore validate: %s\n", diagnostic.Message)
			return 1
		}
		fmt.Fprintln(w, validation.FormatDiagnostic(diagnostic))
		if diagnostic.Member != "" {
			members[diagnostic.Member] = true
		}
		if diagnostic.Severity == validation.SeverityError {
			errors++
		} else {
			warnings++
		}
	}
	memberNames := make([]string, 0, len(members))
	for member := range members {
		memberNames = append(memberNames, member)
	}
	sort.Strings(memberNames)
	for _, member := range memberNames {
		fmt.Fprintf(w, "see: lore package doc %s\n", member)
	}
	fmt.Fprintf(w, "%d %s, %d %s\n", errors, countLabel(errors, "error"), warnings, countLabel(warnings, "warning"))
	if errors > 0 {
		return 1
	}
	return 0
}

func countLabel(count int, singular string) string {
	if count == 1 {
		return singular
	}
	return singular + "s"
}

func init() {
	RegisterLoreSub(LoreSub{
		Name:    "validate",
		Summary: "Lint a bundle with plugin-provided validators",
		Run:     cmdLoreValidate,
	})
}
