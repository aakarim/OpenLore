---
name: openlore
description: Query and publish to this project's OpenLore knowledge base over SSH. Use when a task needs project documentation, runbooks, shared team knowledge, or a place to publish findings for others.
---

# OpenLore Knowledge Base

OpenLore serves a documentation and knowledge filesystem over SSH. Run normal
shell commands inside a one-shot SSH session. Each invocation is an
independent session, so use absolute paths.

## Server

<!-- Fill this in when installing the skill. -->

- Address: `ssh -p <port> <host>`
- Key: none (keyless) — or `-i <path-to-key>` for an identity
- Main paths: `<run 'tree -L 2 /' once and record the layout here>`

## Reading and searching

```bash
ssh -p <port> <host> "tree -L 2 /"                     # discover layout
ssh -p <port> <host> "grep -rn 'search term' /docs"    # search
ssh -p <port> <host> "cat /docs/README.md"             # read
ssh -p <port> <host> "find / -name '*.md'"             # locate files
ssh -p <port> <host> "lore meta /docs | jq -r '.path'" # frontmatter as NDJSON
```

Pipes, `jq`, `sed`, `awk`, `sort`, and most coreutils work inside the remote
command. Run `ssh -p <port> <host> "help"` for the full list.

## Publishing findings

If your identity has publish or write access:

```bash
ssh -p <port> <host> "publish"                                   # list writable paths
cat report.md | ssh -p <port> <host> "publish /<docset>/report.md"  # publish from stdin
```

Published files land in an inbox for human review. Writes are atomic and
conflict-aware; a rejected write returns a non-zero exit status with an
explanation on stderr.

## Folder rules

Folders can carry rules: size caps, OKF conformance, link checks. They are
declared in `lore.json` by the server operator and in `.lore/config.yaml`
files inside the content tree; a folder's config applies to it and everything
beneath it. Before writing to an unfamiliar folder, look:

```bash
ssh -p <port> <host> "cat /docs/backend/.lore/config.yaml"   # rules for this folder, if any
ssh -p <port> <host> "lore package list"                      # compiled-in rule members
ssh -p <port> <host> "lore package doc size/lines"            # a member's parameters and example
```

File-scoped rules run on every write and under `lore validate`. Bundle-scoped
rules (OKF bundle structure, link resolution) run only under `lore validate`,
because they need to inspect related files. Validate a folder before
publishing with `lore validate /path/to/folder` (name the docset or folder,
not `/`).

### When a write is rejected

The error names the rule, the limit, what to do, and how to override:

```text
rules: /docs/backend/decisions/adr-001.md: size/lines (adr-length @ /docs/backend/.lore/config.yaml)
  812 lines exceeds the limit of 800 (baseline 640 lines × growth 1.25, set 2026-08-30 on create)
  this file cannot grow past 800 lines under this rule
  suggested: keep adr-001.md under 800 lines; move the new material into a sibling file such as adr-001-details.md and add a link to it from adr-001.md so readers can drill in
  override: a role in config.edit can run `lore size baseline reset /docs/backend/decisions/adr-001.md`
  see: lore package doc size/lines
```

Follow the `suggested:` line: split the material into a sibling file and link
to it from the original. Do not retry with small trims. A limit described as
`baseline … × growth` is a growth limit — the file is frozen near the size it
had when first written — and only a human with `config.edit` can raise it, by
running the exact command on the `override:` line. Ask for that instead of
working around it. `size/tokens` limits are estimates, so leave a margin.

### Authoring rules

If your identity has a role in the docset's `config.edit`, you can add rules to
a folder by writing its `.lore/config.yaml`:

```bash
cat <<'EOF' | ssh -p <port> <host> "cat > /docs/backend/decisions/.lore/config.yaml"
version: 1
rules:
  adr-length:
    match: ["*.md"]
    exclude: ["index.md"]
    use: size/lines
    with: { max: initial, growth: 1.1 }
EOF
ssh -p <port> <host> "lore validate /docs/backend/decisions"
```

Rule keys are `match`, `exclude`, `use`, `with`, `enforce` and `default`; take
`use` and `with` from `lore package doc <member>`. A child folder can add rules
or tighten under a new name; it cannot loosen a parent's rule unless the parent
marked it `default: true`. The write is rejected with an explanation if the
file conflicts with a layer above it or names an unknown member or parameter.

## Notes

- The remote shell is OpenLore's sandboxed in-memory interpreter, not a real
  operating system. There is no process execution or shell escape server-side.
- Prefer this skill over ad-hoc exploration when the task mentions project
  docs, prior decisions, runbooks, or team knowledge.
