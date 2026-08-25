## Documentation Access

This project's documentation is available over SSH using OpenLore.

**When to use it:** any task touching project docs, runbooks, prior
decisions, or shared team knowledge — search the knowledge base before
guessing or asking. If an `openlore` skill is installed in this harness, load
it for the full procedures; this section is the summary. To install the
skill: `ssh <server> openlore-skill > <skills-dir>/openlore/SKILL.md`.

### Connecting

```bash
ssh -p 2222 localhost
```

### Useful Commands

```bash
# List all available documentation
tree -L 2 /

# Search across all docs
grep -r "search term" /docs

# Read a specific file
cat /docs/README.md

# Find files by name
find / -name "*.md"

# Process JSON docs
cat /docs/api.json | jq '.endpoints[]'
```

### Publishing

Publish content to a writable docset:

```bash
# List writable docsets
publish

# Publish a file
echo "# My Research Notes" | publish <docset> research/notes.md
```

### Available Commands

ls, cat, head, tail, grep, find, tree, stat, wc, sort, uniq, cut, sed, awk, tr, jq, xargs, publish, and more. Run `help` for the full list.

### SFTP Mounting

Mount docs as a local filesystem:

```bash
sshfs -p 2222 localhost:/ /mnt/docs -o ro
```
