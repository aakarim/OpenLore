package cmds

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"syscall"

	"github.com/aakarim/go-openlore/pkg/agentskills"
	"github.com/aakarim/go-openlore/pkg/vfs"
)

const SkillsMarker = "user.lore.plugins.openlore.skills.v1"
const skillsMarkerPrefix = "user.lore.plugins.openlore.skills.v"

type SkillEntry struct{ Name, Description, Content string }

var Skills []SkillEntry

func RegisterSkill(name, description, content string) {
	Skills = append(Skills, SkillEntry{name, description, content})
	Register(name, makeSkillCmd(content))
}

func makeSkillCmd(content string) CmdFunc {
	return func(_ CmdContext, _ []string, w io.Writer, _ io.Writer, _ io.Reader) int {
		fmt.Fprint(w, content)
		return 0
	}
}

// CmdSkills keeps the historical human-readable bare command while all
// management commands below use records exclusively.
func CmdSkills(ctx CmdContext, args []string, w, errW io.Writer, stdin io.Reader) int {
	if len(args) != 0 {
		return manageSkills(ctx, args, w)
	}
	if len(Skills) == 0 {
		fmt.Fprintln(w, "No skills installed.")
		return 0
	}
	fmt.Fprintln(w, "Available skills:")
	fmt.Fprintln(w)
	sorted := append([]SkillEntry(nil), Skills...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, s := range sorted {
		fmt.Fprintf(w, "  %-16s %s\n", s.Name, s.Description)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run a skill name as a command to see its content.")
	return 0
}

type skillResult struct {
	Type            string `json:"type"`
	Path            string `json:"path"`
	Operation       string `json:"operation"`
	Status          string `json:"status"`
	Collections     int    `json:"collections"`
	Errors          int    `json:"errors"`
	Warnings        int    `json:"warnings"`
	Ref             string `json:"ref,omitempty"`
	EffectiveStatus string `json:"effective_status,omitempty"`
	Source          string `json:"source,omitempty"`
}

type skillFinding struct {
	Type       string `json:"type"`
	Collection string `json:"collection"`
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Column     int    `json:"column"`
	Severity   string `json:"severity"`
	Rule       string `json:"rule"`
	Message    string `json:"message"`
}

type skillCollection struct {
	Type   string `json:"type"`
	Path   string `json:"path"`
	Status string `json:"status"`
	Errors int    `json:"errors"`
	Direct *bool  `json:"direct,omitempty"`
	Source string `json:"source,omitempty"`
}

func emitSkill(w io.Writer, v any) { _ = json.NewEncoder(w).Encode(v) }

func manageSkills(ctx CmdContext, args []string, w io.Writer) int {
	op := args[0]
	if op != "status" && op != "enable" && op != "disable" && op != "validate" {
		return finish(w, skillResult{Path: canonical(ctx, ctx.Cwd()), Operation: op, Status: "rejected"}, 2)
	}
	target, recreate, ok := parseSkillsArgs(ctx, op, args[1:])
	if !ok {
		return finish(w, skillResult{Path: canonical(ctx, ctx.Cwd()), Operation: op, Status: "rejected"}, 2)
	}
	r := skillResult{Path: target, Operation: op}
	if !ctx.SkillsManagementEnabled() {
		r.Status = "unsupported"
		return finish(w, r, 1)
	}
	root, doc, accessible := governingDocset(ctx.Docsets(), target)
	if target == "/" && op == "validate" {
		accessible = true
	}
	if !accessible || (target != "/" && !existingDirectory(ctx.FS(), target)) || (target == "/" && op != "validate") {
		r.Status = "rejected"
		return finish(w, r, 1)
	}
	switch op {
	case "status":
		return skillsStatus(ctx.FS(), target, root, r, w)
	case "validate":
		return skillsValidate(ctx.FS(), ctx.Docsets(), target, r, w)
	}
	if !doc.Writable || !hasNamedRW(doc) {
		r.Status = "rejected"
		return finish(w, r, 1)
	}
	return skillsMutate(ctx.FS(), ctx.Docsets(), target, root, op, recreate, r, w)
}

func parseSkillsArgs(ctx CmdContext, op string, args []string) (string, bool, bool) {
	recreate := false
	var folder string
	for _, a := range args {
		if a == "--recreate-xattrs" && (op == "enable" || op == "disable") && !recreate {
			recreate = true
			continue
		}
		if strings.HasPrefix(a, "-") || folder != "" {
			return "", false, false
		}
		folder = a
	}
	if folder == "" {
		folder = ctx.Cwd()
	} else {
		folder = ctx.Resolve(folder)
	}
	return canonical(ctx, vfs.CleanPath(folder)), recreate, true
}

func canonical(ctx CmdContext, p string) string {
	if c, ok := ctx.FS().(vfs.PathCanonicalizer); ok {
		return vfs.CleanPath(c.CanonicalPath(p))
	}
	return vfs.CleanPath(p)
}

func finish(w io.Writer, r skillResult, code int) int {
	r.Type = "result"
	emitSkill(w, r)
	return code
}
func existingDirectory(f vfs.FileSystem, p string) bool {
	i, e := f.Stat(p)
	return e == nil && i.IsDir()
}
func hasNamedRW(d DocsetInfo) bool {
	if len(d.Grants) == 0 {
		return d.Grant == "rw"
	}
	for _, g := range d.Grants {
		if g == "rw" {
			return true
		}
	}
	return false
}

func skillsStatus(f vfs.FileSystem, target, root string, r skillResult, w io.Writer) int {
	direct, effective, source, unknown, err := markerStatus(f, target, root)
	status := "disabled"
	if effective {
		status = "enabled"
	}
	if unknown {
		status = "degraded"
	}
	if err != nil {
		status = errnoStatus(err)
	}
	c := skillCollection{Type: "collection", Path: target, Status: status, Errors: boolInt(status == "degraded" || status == "conflict"), Direct: &direct, Source: source}
	emitSkill(w, c)
	r.Status, r.Collections, r.Errors, r.Source = status, 1, c.Errors, source
	if status == "unsupported" || status == "degraded" || status == "conflict" {
		return finish(w, r, 1)
	}
	return finish(w, r, 0)
}

func skillsValidate(f vfs.FileSystem, ds []DocsetInfo, scope string, r skillResult, w io.Writer) int {
	collections := discoverCollections(f, ds, scope)
	if len(collections) == 0 {
		emitSkill(w, skillFinding{Type: "finding", Collection: scope, Path: ".", Line: 1, Column: 1, Severity: "warning", Rule: "agent-skills/no-collections", Message: "no enabled Skills collections"})
		r.Status, r.Warnings = "no_collections", 1
		return finish(w, r, 0)
	}
	findings, counts := validateOwnedTrees(f, collections, nestedDocsetRoots(ds, scope))
	for _, f := range findings {
		emitSkill(w, f)
		if f.Severity == "error" {
			r.Errors++
		} else {
			r.Warnings++
		}
	}
	for _, c := range collections {
		n := counts[c]
		st := "valid"
		if n > 0 {
			st = "invalid"
		}
		emitSkill(w, skillCollection{Type: "collection", Path: c, Status: st, Errors: n})
	}
	r.Collections = len(collections)
	r.Status = "valid"
	if r.Errors > 0 {
		r.Status = "invalid"
		return finish(w, r, 1)
	}
	return finish(w, r, 0)
}

func skillsMutate(f vfs.FileSystem, ds []DocsetInfo, target, root, op string, recreate bool, r skillResult, w io.Writer) int {
	direct, _, source, _, probeErr := markerStatus(f, target, root)
	if probeErr != nil {
		if recreate && errors.Is(probeErr, syscall.EIO) {
			xm, ok := f.(vfs.XattrMaintenance)
			if !ok {
				r.Status = "unsupported"
				return finish(w, r, 1)
			}
			attrs := map[string][]byte{}
			if op == "enable" {
				attrs[SkillsMarker] = []byte{}
			}
			if err := xm.PreserveAndRecreateXattrs(target, attrs); err != nil {
				return mutationError(w, r, err)
			}
			emitSkill(w, skillFinding{Type: "finding", Collection: target, Path: ".", Line: 1, Column: 1, Severity: "warning", Rule: "agent-skills/xattrs-unrecoverable", Message: "unrelated attributes could not be recovered automatically"})
			r.Warnings, r.Collections = 1, 1
			if op == "enable" {
				r.Status = "enabled"
			} else {
				r.Status = "disabled"
			}
			return finish(w, r, 0)
		}
		return mutationError(w, r, probeErr)
	}
	if recreate {
		r.Status = "rejected"
		return finish(w, r, 1)
	}
	if op == "enable" {
		collections := append([]string{target}, descendantMarkers(f, ds, target)...)
		collections = uniqueSorted(collections)
		findings, counts := validateOwnedTrees(f, collections, nestedDocsetRoots(ds, target))
		for _, x := range findings {
			emitSkill(w, x)
			r.Errors++
		}
		for _, c := range collections {
			n := counts[c]
			st := "valid"
			if n > 0 {
				st = "invalid"
			}
			emitSkill(w, skillCollection{Type: "collection", Path: c, Status: st, Errors: n})
		}
		r.Collections = len(collections)
		if r.Errors > 0 {
			r.Status = "rejected"
			return finish(w, r, 1)
		}
		if direct {
			r.Status = "already_enabled"
			return finish(w, r, 0)
		}
		xw, ok := f.(vfs.XattrWriter)
		if !ok {
			r.Status = "unsupported"
			return finish(w, r, 1)
		}
		if err := xw.SetXattr(target, SkillsMarker, []byte{}, 0); err != nil {
			return mutationError(w, r, err)
		}
		r.Status = "enabled"
		return finish(w, r, 0)
	}
	if !direct {
		r.Status = "already_disabled"
		r.Source = source
		return finish(w, r, 0)
	}
	xw, ok := f.(vfs.XattrWriter)
	if !ok {
		r.Status = "unsupported"
		return finish(w, r, 1)
	}
	if err := xw.RemoveXattr(target, SkillsMarker); err != nil {
		var p *vfs.PendingChangeError
		if errors.As(err, &p) {
			r.Status = "pending"
			r.Ref = p.Ref
			return finish(w, r, 0)
		}
		return mutationError(w, r, err)
	}
	_, inherited, inheritedSource, _, err := markerStatus(f, target, root)
	if err != nil {
		return mutationError(w, r, err)
	}
	nested := descendantMarkers(f, ds, target)
	r.Collections = len(nested)
	for _, c := range nested {
		emitSkill(w, skillCollection{Type: "collection", Path: c, Status: "enabled", Errors: 0})
	}
	if inherited {
		r.Status = "marker_removed"
		r.EffectiveStatus = "enabled"
		r.Source = inheritedSource
	} else {
		r.Status = "disabled"
	}
	return finish(w, r, 0)
}

func mutationError(w io.Writer, r skillResult, err error) int {
	var p *vfs.PendingChangeError
	if errors.As(err, &p) {
		r.Status = "pending"
		r.Ref = p.Ref
		return finish(w, r, 0)
	}
	r.Status = errnoStatus(err)
	return finish(w, r, 1)
}
func errnoStatus(err error) string {
	if errors.Is(err, syscall.ENOTSUP) {
		return "unsupported"
	}
	if errors.Is(err, syscall.EIO) {
		return "conflict"
	}
	return "degraded"
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func markerStatus(f vfs.FileSystem, p, root string) (direct, effective bool, source string, unknown bool, err error) {
	x, ok := f.(vfs.XattrReader)
	if !ok {
		return false, false, "", false, syscall.ENOTSUP
	}
	for cur := p; ; cur = path.Dir(cur) {
		names, e := x.ListXattrs(cur)
		if e != nil {
			return false, false, "", false, e
		}
		for _, n := range names {
			if strings.HasPrefix(n, skillsMarkerPrefix) && n != SkillsMarker {
				unknown = true
			}
		}
		b, e := x.GetXattr(cur, SkillsMarker)
		if e == nil {
			if len(b) != 0 {
				return false, false, "", unknown, syscall.EIO
			}
			return cur == p, true, cur, unknown, nil
		}
		if !errors.Is(e, syscall.ENODATA) {
			return false, false, "", unknown, e
		}
		if cur == root {
			break
		}
	}
	return false, false, "", unknown, nil
}

func governingDocset(ds []DocsetInfo, p string) (string, DocsetInfo, bool) {
	best := ""
	var out DocsetInfo
	for _, d := range ds {
		for _, q := range d.Paths {
			q = vfs.CleanPath(q)
			if (p == q || stringsHasRoot(p, q)) && len(q) > len(best) {
				best = q
				out = d
			}
		}
	}
	return best, out, best != ""
}
func stringsHasRoot(p, r string) bool {
	return r == "/" || len(p) > len(r) && strings.HasPrefix(p, r) && p[len(r)] == '/'
}
func nestedDocsetRoots(ds []DocsetInfo, p string) map[string]bool {
	m := map[string]bool{}
	for _, d := range ds {
		for _, r := range d.Paths {
			r = vfs.CleanPath(r)
			if r != p && stringsHasRoot(r, p) {
				m[r] = true
			}
		}
	}
	return m
}

func directMarker(f vfs.FileSystem, p string) bool {
	x, ok := f.(vfs.XattrReader)
	if !ok {
		return false
	}
	b, e := x.GetXattr(p, SkillsMarker)
	return e == nil && len(b) == 0
}
func descendantMarkers(f vfs.FileSystem, ds []DocsetInfo, root string) []string {
	return discoverDirect(f, root, nestedDocsetRoots(ds, root))
}
func discoverDirect(f vfs.FileSystem, root string, bounds map[string]bool) []string {
	var out []string
	var walk func(string)
	walk = func(d string) {
		if d != root && bounds[d] {
			return
		}
		if directMarker(f, d) {
			out = append(out, d)
		}
		es, e := f.ReadDir(d)
		if e != nil {
			return
		}
		for _, x := range es {
			if x.IsDir() {
				walk(path.Join(d, x.Name()))
			}
		}
	}
	walk(root)
	return uniqueSorted(out)
}
func discoverCollections(f vfs.FileSystem, ds []DocsetInfo, scope string) []string {
	set := map[string]bool{}
	for _, d := range ds {
		for _, root := range d.Paths {
			root = vfs.CleanPath(root)
			start := root
			if scope != "/" {
				if scope == root || stringsHasRoot(scope, root) {
					start = scope
				} else if !stringsHasRoot(root, scope) {
					continue
				}
			}
			_, eff, src, _, e := markerStatus(f, start, root)
			if e == nil && eff {
				set[src] = true
			}
			for _, c := range discoverDirect(f, start, nestedDocsetRoots(ds, start)) {
				set[c] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	return uniqueSorted(out)
}
func uniqueSorted(in []string) []string {
	m := map[string]bool{}
	for _, x := range in {
		m[x] = true
	}
	out := make([]string, 0, len(m))
	for x := range m {
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}

func validateOwnedTrees(f vfs.FileSystem, collections []string, bounds map[string]bool) ([]skillFinding, map[string]int) {
	counts := map[string]int{}
	seen := map[string]bool{}
	var out []skillFinding
	for _, root := range collections {
		var walk func(string, string)
		walk = func(d, owner string) {
			if d != root && bounds[d] {
				return
			}
			if d != root && directMarker(f, d) {
				owner = d
			}
			es, e := f.ReadDir(d)
			if e != nil {
				return
			}
			for _, x := range es {
				p := path.Join(d, x.Name())
				if x.IsDir() {
					walk(p, owner)
					continue
				}
				if x.Name() != "SKILL.md" || seen[p] {
					continue
				}
				seen[p] = true
				b, e := f.ReadFile(p)
				if e == nil {
					res, ve := agentskills.Validate(path.Base(d), b)
					if ve == nil && res.Disabled {
						continue
					}
					e = ve
				}
				if e != nil {
					rule := "agent-skills/invalid"
					msg := e.Error()
					if strings.Contains(msg, "name") && (strings.Contains(msg, "match") || strings.Contains(msg, "1-64")) {
						rule = "agent-skills/name"
					}
					out = append(out, skillFinding{Type: "finding", Collection: owner, Path: stringsTrimRoot(p, owner), Line: 1, Column: 1, Severity: "error", Rule: rule, Message: msg})
					counts[owner]++
				}
			}
		}
		walk(root, root)
	}
	return out, counts
}
func stringsTrimRoot(p, r string) string {
	if p == r {
		return "."
	}
	if stringsHasRoot(p, r) {
		return strings.TrimPrefix(p, r+"/")
	}
	return p
}
