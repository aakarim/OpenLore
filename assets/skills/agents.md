## Documentation Access

This project's documentation is available over SSH using OpenLore.

**When to use it:** any task touching project docs, runbooks, prior
decisions, or shared team knowledge — search the knowledge base before
guessing or asking. If an `openlore` skill is installed in this harness, load
it for the full procedures; this section is the summary. To install the
skill: `ssh -p {{.Port}} <host> openlore-skill > <skills-dir>/openlore/SKILL.md`.

### Connecting

```bash
ssh -p {{.Port}} <host>
```

Replace `<host>` with this server's address. Each `ssh` invocation is an
independent session, so use absolute paths.
{{if .Mounts}}
### Layout

Documentation is mounted at: {{range $i, $m := .Mounts}}{{if $i}}, {{end}}`{{$m}}`{{end}}.
Run `lore docsets` to see each mount with your access level.
{{end}}
### Useful Commands

```bash
# List all available documentation
tree -L 2 /

# Search across the docs
grep -r "search term" {{.Mount}}

# Read a specific file
cat {{.File}}

# Find files by name
find {{.Mount}} -name "*.md"

# Query frontmatter as NDJSON
lore meta {{.Mount}} | jq -r '.path'
```

### Publishing
{{if .Publish}}
Publish content into a docset's inbox for human review. The path is
`/<docset>/<file>`; the docset name is the first path segment.

```bash
# List docsets you can publish to
publish

# Publish a file
echo "# My Research Notes" | publish /{{.PublishDocset}}/research/notes.md
```

Writable docsets: {{range $i, $t := .Publish}}{{if $i}}, {{end}}`/{{$t.Name}}/`{{end}}.
{{else if .Writable}}
This identity can write directly to {{range $i, $m := .Writable}}{{if $i}}, {{end}}`{{$m}}`{{end}} with the
ordinary write verbs:

```bash
echo "# My Research Notes" > {{sub (index .Writable 0) "research-notes.md"}}
```
{{else}}
This identity has read-only access. Ask the server operator for a `publish`
grant to store findings; `publish` then lists the docsets you can write to.
{{end}}
### Available Commands

ls, cat, head, tail, grep, find, tree, stat, wc, sort, uniq, cut, sed, awk, tr, jq, xargs, publish, and more. Run `help` for the full list.

### SFTP Mounting

Mount docs as a local filesystem:

```bash
sshfs -p {{.Port}} <host>:/ /mnt/openlore -o ro
```
