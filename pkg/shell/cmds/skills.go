package cmds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/aakarim/go-openlore/pkg/agentskills"
	"github.com/aakarim/go-openlore/pkg/okf"
	"github.com/aakarim/go-openlore/pkg/openlore/skillsremote"
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

// CmdSkills prints agent-facing usage with no arguments while management
// commands emit records exclusively.
func CmdSkills(ctx CmdContext, args []string, w, errW io.Writer, stdin io.Reader) int {
	if len(args) == 0 || len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		printSkillsUsage(w)
		return 0
	}
	return manageSkills(ctx, args, w)
}

func printSkillsUsage(w io.Writer) {
	fmt.Fprintln(w, "# Managing Agent Skills")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Use OpenLore to import public Agent Skills into a writable collection from")
	fmt.Fprintln(w, "GitHub, GitLab, Bitbucket, Codeberg, or self-hosted GitLab/Gitea/Forgejo over")
	fmt.Fprintln(w, "HTTPS. Shorthand owner/repo means GitHub. A branch import tracks upstream and")
	fmt.Fprintln(w, "checks for updates when SKILL.md is read.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "## Find skills")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "    lore meta --filter skills")
	fmt.Fprintln(w, "    lore meta --filter skills | jq -r 'select((.name + \" \" + .description) | test(\"pdf\"; \"i\")) | .path'")
	fmt.Fprintln(w, "    cat <path>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Records are SKILL.md frontmatter plus path. Pass a directory to scope the")
	fmt.Fprintln(w, "scan. Only valid skills appear; run `skills validate` to surface broken ones.")
	fmt.Fprintln(w, "Read the chosen SKILL.md and follow it, including any files it references.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "## Import into your home")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "    skills enable \"$HOME\"")
	fmt.Fprintln(w, "    skills import https://github.com/owner/repo \"$HOME\"")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "The destination defaults to the current directory. A repository with a root")
	fmt.Fprintln(w, "SKILL.md, or exactly one elsewhere, imports directly. Otherwise the command")
	fmt.Fprintln(w, "makes no changes and returns candidates with their names and descriptions;")
	fmt.Fprintln(w, "rerun with the selected path:")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "    skills import owner/repo/path/from/candidate@main \"$HOME\"")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "An omitted ref uses and tracks the default branch. A branch ref such as @main")
	fmt.Fprintln(w, "tracks updates; a tag or full commit SHA is pinned.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "## Manage")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "    skills status [collection]")
	fmt.Fprintln(w, "    skills validate [scope]")
	fmt.Fprintln(w, "    skills update [skill-folder]")
	fmt.Fprintln(w, "    skills remove-remote [skill-folder]")
	fmt.Fprintln(w, "    skills disable [collection]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Linked skill files are read-only locally. Change them upstream or remove the")
	fmt.Fprintln(w, "remote link. Management commands emit NDJSON for agents to parse.")
	if len(Skills) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "## Installed instruction commands")
	fmt.Fprintln(w)
	sorted := append([]SkillEntry(nil), Skills...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, s := range sorted {
		fmt.Fprintf(w, "  %-16s %s\n", s.Name, s.Description)
	}
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
	OldCommit       string `json:"old_commit,omitempty"`
	NewCommit       string `json:"new_commit,omitempty"`
}

type remoteSkillUpdater interface {
	UpdateRemoteSkill(context.Context, string) (oldCommit, newCommit string, err error)
}

type remoteSkillClientProvider interface {
	SkillsRemoteClient() *skillsremote.Client
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

type skillCandidate struct {
	Type        string `json:"type"`
	Path        string `json:"path"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
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
	if op == "import" {
		return skillsImport(ctx, args[1:], w)
	}
	if op == "remove-remote" {
		return skillsRemoveRemote(ctx, args[1:], w)
	}
	if op == "update" {
		return skillsUpdate(ctx, args[1:], w)
	}
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

func skillsUpdate(ctx CmdContext, args []string, w io.Writer) int {
	target := ctx.Cwd()
	if len(args) > 1 {
		return finish(w, skillResult{Path: canonical(ctx, target), Operation: "update", Status: "rejected"}, 2)
	}
	if len(args) == 1 {
		target = ctx.Resolve(args[0])
	}
	target = canonical(ctx, target)
	r := skillResult{Path: target, Operation: "update"}
	if !ctx.SkillsManagementEnabled() {
		r.Status = "unsupported"
		return finish(w, r, 1)
	}
	_, doc, accessible := governingDocset(ctx.Docsets(), target)
	if !accessible || !doc.Writable || !hasNamedRW(doc) {
		r.Status = "rejected"
		return finish(w, r, 1)
	}
	updater, ok := ctx.FS().(remoteSkillUpdater)
	if !ok {
		r.Status = "unsupported"
		return finish(w, r, 1)
	}
	oldCommit, newCommit, err := updater.UpdateRemoteSkill(context.Background(), target)
	r.OldCommit, r.NewCommit = oldCommit, newCommit
	if err != nil {
		r.Status = "degraded"
		return finish(w, r, 1)
	}
	r.Status = "updated"
	if oldCommit == newCommit {
		r.Status = "current"
	}
	return finish(w, r, 0)
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
	_ = vfs.WalkDir(f, target, func(skillFile string, info *vfs.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() || path.Base(skillFile) != "SKILL.md" {
			return walkErr
		}
		b, e := f.ReadFile(skillFile)
		if e != nil {
			return nil
		}
		remote, linked, _ := agentskills.ReadRemote(b)
		if !linked {
			return nil
		}
		record := map[string]any{"type": "remote", "path": path.Dir(skillFile), "repo": remote.Repo, "ref": remote.Ref, "ref_type": remote.Kind, "commit": remote.Commit}
		if frontmatter, _, ok, _ := okf.ParseFrontmatter(b); ok {
			if outcome, ok := frontmatter["remote-status"].(string); ok {
				record["last_check"] = outcome
			}
		}
		emitSkill(w, record)
		return nil
	})
	r.Status, r.Collections, r.Errors, r.Source = status, 1, c.Errors, source
	if status == "unsupported" || status == "degraded" || status == "conflict" {
		return finish(w, r, 1)
	}
	return finish(w, r, 0)
}

func skillsRemoveRemote(ctx CmdContext, args []string, w io.Writer) int {
	target := ctx.Cwd()
	if len(args) > 1 {
		return finish(w, skillResult{Path: canonical(ctx, target), Operation: "remove-remote", Status: "rejected"}, 2)
	}
	if len(args) == 1 {
		target = ctx.Resolve(args[0])
	}
	target = canonical(ctx, target)
	r := skillResult{Path: target, Operation: "remove-remote"}
	if !ctx.SkillsManagementEnabled() {
		r.Status = "unsupported"
		return finish(w, r, 1)
	}
	skillFile := path.Join(target, "SKILL.md")
	b, err := ctx.FS().ReadFile(skillFile)
	if err != nil {
		r.Status = "rejected"
		return finish(w, r, 1)
	}
	if _, linked, err := agentskills.ReadRemote(b); err != nil || !linked {
		r.Status = "rejected"
		return finish(w, r, 1)
	}
	clean, err := agentskills.StripRemote(b)
	if err != nil {
		r.Status = "rejected"
		return finish(w, r, 1)
	}
	wfs, ok := ctx.FS().(vfs.WritableFS)
	if !ok {
		r.Status = "unsupported"
		return finish(w, r, 1)
	}
	if _, err = wfs.WriteFileAtomic(skillFile, clean, overwriteOpts(ctx, skillFile, b, true)); err != nil {
		return mutationError(w, r, err)
	}
	r.Status = "unlinked"
	return finish(w, r, 0)
}

func skillsImport(ctx CmdContext, args []string, w io.Writer) int {
	r := skillResult{Path: canonical(ctx, ctx.Cwd()), Operation: "import"}
	if len(args) < 1 || len(args) > 2 {
		return finish(w, r, 2)
	}
	if !ctx.SkillsManagementEnabled() {
		r.Status = "unsupported"
		return finish(w, r, 1)
	}
	spec, err := skillsremote.ParseSpec(args[0])
	if err != nil {
		r.Status = "rejected"
		return finish(w, r, 1)
	}
	parent := ctx.Cwd()
	if len(args) == 2 {
		parent = ctx.Resolve(args[1])
	}
	parent = canonical(ctx, parent)
	root, doc, ok := governingDocset(ctx.Docsets(), parent)
	if !ok || !doc.Writable || !hasNamedRW(doc) {
		r.Status = "rejected"
		return finish(w, r, 1)
	}
	_, enabled, _, _, _ := markerStatus(ctx.FS(), parent, root)
	if !enabled {
		r.Status = "rejected"
		return finish(w, r, 1)
	}
	timeout := ctx.SkillsRemoteTimeout()
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	maxBytes := ctx.SkillsRemoteMaxBytes()
	if maxBytes == 0 {
		maxBytes = 10 * 1024 * 1024
	}
	client := skillsremote.Client{HTTP: skillsremote.NewPublicHTTPClient(timeout), GitHubBase: "https://github.com", CodeloadBase: "https://codeload.github.com", MaxBytes: maxBytes, MaxFiles: skillsremote.MaxFiles}
	if provider, ok := ctx.FS().(remoteSkillClientProvider); ok && provider.SkillsRemoteClient() != nil {
		client = *provider.SkillsRemoteClient()
	}
	refs, err := client.Resolve(context.Background(), spec.Repo)
	if err != nil {
		r.Status = "degraded"
		return finish(w, r, 1)
	}
	sha, kind, ref, err := refs.Resolve(spec.Ref)
	if err != nil {
		r.Status = "rejected"
		return finish(w, r, 1)
	}
	files, err := client.Fetch(context.Background(), spec.Repo, sha, spec.Path)
	if err != nil {
		r.Status = "rejected"
		return finish(w, r, 1)
	}
	if err = skillsremote.ValidateFiles(files); err != nil {
		r.Status = "rejected"
		return finish(w, r, 1)
	}
	if spec.Path == "" {
		if files["SKILL.md"] == nil {
			candidates := []string{}
			for name := range files {
				if path.Base(name) == "SKILL.md" {
					candidates = append(candidates, path.Dir(name))
				}
			}
			sort.Strings(candidates)
			if len(candidates) != 1 {
				for _, candidate := range candidates {
					item := skillCandidate{Type: "candidate", Path: candidate}
					if fm, _, ok, _ := okf.ParseFrontmatter(files[path.Join(candidate, "SKILL.md")]); ok {
						item.Name, _ = fm["name"].(string)
						item.Description, _ = fm["description"].(string)
					}
					emitSkill(w, item)
				}
				r.Status = "rejected"
				r.Errors = len(candidates)
				return finish(w, r, 1)
			}
			spec.Path = candidates[0]
			files, err = client.Fetch(context.Background(), spec.Repo, sha, spec.Path)
			if err != nil {
				r.Status = "rejected"
				return finish(w, r, 1)
			}
			if err = skillsremote.ValidateFiles(files); err != nil {
				r.Status = "rejected"
				return finish(w, r, 1)
			}
		}
	}
	skill := files["SKILL.md"]
	name, skill, err := agentskills.Normalize("", skill)
	if err != nil {
		r.Status = "rejected"
		return finish(w, r, 1)
	}
	remote := agentskills.Remote{Repo: spec.Repo, Path: spec.Path, Ref: ref, Commit: sha, Kind: kind}
	skill, err = agentskills.GraftRemote(skill, remote)
	if err != nil {
		r.Status = "rejected"
		return finish(w, r, 1)
	}
	validation, err := agentskills.Validate(name, skill)
	if err != nil || validation.Disabled {
		r.Status = "rejected"
		return finish(w, r, 1)
	}
	dest := path.Join(parent, name)
	if path.Dir(dest) != parent || path.Base(dest) != name {
		r.Status = "rejected"
		return finish(w, r, 1)
	}
	r.Path = dest
	if _, err := ctx.FS().Stat(dest); err == nil {
		r.Status = "rejected"
		return finish(w, r, 1)
	}
	admitter, ok := ctx.FS().(vfs.ChangeSetAdmitter)
	if !ok {
		r.Status = "unsupported"
		return finish(w, r, 1)
	}
	changes := []vfs.Change{{Target: dest, Action: vfs.ChangeActionMkdir}}
	dirs := map[string]bool{}
	delete(files, "SKILL.md")
	for rel, data := range files {
		for dir := path.Dir(rel); dir != "."; dir = path.Dir(dir) {
			dirs[path.Join(dest, dir)] = true
		}
		changes = append(changes, vfs.Change{Target: path.Join(dest, rel), Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: data, Opts: vfs.WriteOpts{IfNoneMatch: true}}})
	}
	orderedDirs := make([]string, 0, len(dirs))
	for dir := range dirs {
		orderedDirs = append(orderedDirs, dir)
	}
	sort.Slice(orderedDirs, func(i, j int) bool {
		return strings.Count(orderedDirs[i], "/") < strings.Count(orderedDirs[j], "/") || strings.Count(orderedDirs[i], "/") == strings.Count(orderedDirs[j], "/") && orderedDirs[i] < orderedDirs[j]
	})
	sort.Slice(changes[1:], func(i, j int) bool { return changes[i+1].Target < changes[j+1].Target })
	batch := []vfs.Change{changes[0]}
	for _, dir := range orderedDirs {
		batch = append(batch, vfs.Change{Target: dir, Action: vfs.ChangeActionMkdir})
	}
	batch = append(batch, changes[1:]...)
	batch = append(batch, vfs.Change{Target: path.Join(dest, "SKILL.md"), Action: vfs.ChangeActionWrite, Write: &vfs.WriteChange{Bytes: skill, Opts: vfs.WriteOpts{IfNoneMatch: true}}})
	if err = admitter.AdmitChangeSet(vfs.ChangeSet{Changes: batch}); err != nil {
		r.Status = "degraded"
		return finish(w, r, 1)
	}
	r.Status = "imported"
	r.Source = kind
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
