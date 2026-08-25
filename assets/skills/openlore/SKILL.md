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

## Notes

- The remote shell is OpenLore's sandboxed in-memory interpreter, not a real
  operating system. There is no process execution or shell escape server-side.
- Prefer this skill over ad-hoc exploration when the task mentions project
  docs, prior decisions, runbooks, or team knowledge.
