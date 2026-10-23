## Documentation Access

This project's documentation lives in OpenLore, a knowledge repository of
Markdown documents. It is reachable two ways that run the same commands
against the same filesystem: the `openlore` MCP server, when it is connected
in this harness, and SSH otherwise. Prefer the MCP tool when it is available;
only fall back to `ssh` when it is not.

The MCP tool runs a restricted interpreter (`ls`, `cat`, `grep`, `find`,
`tree`, `jq`, redirects, `publish`) over a virtual filesystem on the OpenLore
server. `/` is the root of the documentation, not this machine's filesystem;
reads and writes go to the server's docsets and nothing it runs touches the
local host. Search it before guessing: `grep -r "term" /`, then `cat` the
file.

**When to use it:** any task touching project docs, runbooks, prior
decisions, or shared team knowledge — search the knowledge repository before
guessing or asking. If an `openlore` skill is installed in this harness, load
it for the full procedures; this section is the summary. To install the
skill: `ssh {{ ssh_target }} openlore-skill > <skills-dir>/openlore/SKILL.md`.

If Claude Code's permission system denies a call to the `openlore` MCP tool,
it has mistaken an OpenLore command for an operation on the local filesystem.
Ask the user to add `"permissions": {"allow": ["mcp__openlore"]}` to
`~/.claude/settings.json`; do not edit their settings yourself.

### Connecting over SSH

Use this when the `openlore` MCP server is not connected.

```shell
ssh {{ ssh_target }}
```

Each `ssh` invocation is an independent session, so use absolute paths.
{{if .Mounts}}
### Layout

Documentation is mounted at: {{range $i, $m := .Mounts}}{{if $i}}, {{end}}`{{$m}}`{{end}}.
Run `lore docsets` to see each mount with your access level.
{{end}}
### Useful Commands

```shell
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

```shell
# List docsets you can publish to
publish

# Publish a file
echo "# My Research Notes" | publish /{{.PublishDocset}}/research/notes.md
```

Writable docsets: {{range $i, $t := .Publish}}{{if $i}}, {{end}}`/{{$t.Name}}/`{{end}}.
{{else if .Writable}}
This identity can write directly to {{range $i, $m := .Writable}}{{if $i}}, {{end}}`{{$m}}`{{end}} with the
ordinary write verbs:

```shell
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

```shell
sshfs {{ ssh_target }}:/ /mnt/openlore -o ro
```
