package cmds

import (
	"bytes"
	"path"
	"strconv"
	"strings"
	"text/template"

	"github.com/aakarim/go-openlore/pkg/vfs"
)

// SkillData is the view a skill document renders against. It is computed per
// invocation from the calling session, so the examples an agent copies name
// mounts, files, and publish targets that exist for that caller rather than a
// guessed layout such as /docs.
type SkillData struct {
	// Port is the advertised SSH port, or the literal placeholder "<port>"
	// when the host did not supply one.
	Port string
	// Mounts lists the canonical mount paths the session can read, sorted.
	// Alias rows are omitted.
	Mounts []string
	// Mount is the mount the read examples target: the one containing the
	// session's working directory when there is one, otherwise the first
	// mount, otherwise "/".
	Mount string
	// File is an existing file beneath Mount for `cat` examples. When the
	// mount holds no files it falls back to "<Mount>/<file>" so the example
	// is visibly a placeholder.
	File string
	// Publish lists the inboxes the session may publish to.
	Publish []PublishTarget
	// PublishDocset is the name of the first publish target, or "" when the
	// session cannot publish.
	PublishDocset string
	// Writable lists the mounts the session may write directly with the
	// ordinary write verbs (redirects, tee, mkdir, …).
	Writable []string
}

// sshPortContext is implemented by hosts that know the SSH port agents should
// connect to. A standalone shell does not.
type sshPortContext interface{ AdvertisedSSHPort() int }

// exampleFileLimit bounds the directory walk that picks File so a large mount
// does not make a skill command slow.
const exampleFileLimit = 512

// NewSkillData gathers the per-session facts skill templates render against.
func NewSkillData(ctx CmdContext) SkillData {
	d := SkillData{Port: "<port>", Mount: "/"}
	if p, ok := ctx.(sshPortContext); ok && p.AdvertisedSSHPort() > 0 {
		d.Port = strconv.Itoa(p.AdvertisedSSHPort())
	}
	for _, ds := range ctx.Docsets() {
		if ds.AliasTarget != "" || len(ds.Paths) == 0 {
			continue
		}
		mount := vfs.CleanPath(ds.Paths[0])
		d.Mounts = append(d.Mounts, mount)
		if ds.Writable {
			d.Writable = append(d.Writable, mount)
		}
	}
	d.Mounts = uniqueSorted(d.Mounts)
	d.Writable = uniqueSorted(d.Writable)
	if len(d.Mounts) > 0 {
		d.Mount = d.Mounts[0]
		cwd := vfs.CleanPath(ctx.Cwd())
		for _, m := range d.Mounts {
			if cwd == m || stringsHasRoot(cwd, m) {
				d.Mount = m
				break
			}
		}
	}
	d.File = exampleFile(ctx.FS(), d.Mount)
	d.Publish = ctx.PublishTargets()
	if len(d.Publish) > 0 {
		d.PublishDocset = d.Publish[0].Name
	}
	return d
}

// exampleFile returns the first Markdown file found beneath root by a bounded
// breadth-first walk, falling back to any regular file, then to a visible
// placeholder. Hidden entries and the synthetic /jobs mount are skipped so the
// example points at content, not bookkeeping.
func exampleFile(fsys vfs.FileSystem, root string) string {
	placeholder := path.Join(root, "<file>")
	if fsys == nil {
		return placeholder
	}
	queue := []string{root}
	seen := 0
	fallback := ""
	for len(queue) > 0 && seen < exampleFileLimit {
		dir := queue[0]
		queue = queue[1:]
		entries, err := fsys.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			seen++
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			p := path.Join(dir, name)
			if e.IsDir() {
				if p != "/jobs" {
					queue = append(queue, p)
				}
				continue
			}
			if strings.HasSuffix(name, ".md") {
				return p
			}
			if fallback == "" {
				fallback = p
			}
		}
	}
	if fallback != "" {
		return fallback
	}
	return placeholder
}

// renderSkill executes a skill document as a text/template against the
// session. Documents that are not valid templates (for example a runtime skill
// from --skills-dir that happens to contain "{{") are emitted verbatim, as is
// any document whose execution fails, so a template mistake degrades to the
// old static behaviour instead of hiding the skill.
func renderSkill(ctx CmdContext, content string) string {
	tmpl, err := template.New("skill").Funcs(skillFuncs(ctx)).Parse(content)
	if err != nil {
		return content
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, NewSkillData(ctx)); err != nil {
		return content
	}
	return buf.String()
}

func skillFuncs(ctx CmdContext) template.FuncMap {
	return template.FuncMap{
		// join renders a list of mounts for prose, e.g. "/backend, /notes".
		"join": func(items []string, sep string) string { return strings.Join(items, sep) },
		// sub joins a relative path beneath a mount without doubling slashes
		// when the mount is "/".
		"sub": func(mount, rel string) string { return path.Join(mount, rel) },
		// exists reports whether the session can see a path, so a document can
		// include a section only when the folder it targets is really there.
		"exists": func(p string) bool {
			if ctx.FS() == nil {
				return false
			}
			_, err := ctx.FS().Stat(vfs.CleanPath(p))
			return err == nil
		},
	}
}
