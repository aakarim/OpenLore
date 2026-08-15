# Available Commands

## Filesystem
- `ls` — List directory contents
- `cat` — Display file contents
- `head` / `tail` — First/last N lines
- `tree` — Directory tree visualization
- `find` — Find files by name or type
- `stat` — File metadata
- `wc` — Count lines, words, bytes
- `du` — File space usage
- `diff` — Compare two files

## Search
- `grep` — Search for patterns (supports -r, -i, -n, -v, -c, -l, -o)

## Text Processing
- `sort`, `uniq`, `cut`, `sed`, `awk`, `tr`
- `rev`, `tac`, `nl`, `fold`, `paste`, `column`
- `join`, `comm`, `expand`, `unexpand`

## Data
- `jq` — JSON processor

## Utilities
- `xargs`, `seq`, `printf`, `date`
- `basename`, `dirname`, `tee`
- `base64`, `md5sum`, `sha256sum`

## Shell Features
- Pipes: `grep pattern file | sort | head -5`
- Logical operators: `test -f x && echo yes || echo no`
- Loops: `for x in a b c; do echo $x; done`
- Variables: `FOO=bar; echo $FOO`
- Command substitution: `echo $(wc -l file.md)`
## Skills collections

With `plugins.skills.enabled: true`, directories can be managed as recursive
Agent Skills collections:

```text
skills status [folder]
skills enable [folder]
skills disable [folder]
skills validate [scope]
skills import <repo-spec> [parent-dir]
skills update [skill-folder]
skills remove-remote [skill-folder]
skills enable --recreate-xattrs [folder]
skills disable --recreate-xattrs [folder]
```

Folders and scopes default to the current directory. Management commands emit
NDJSON; bare `skills` prints import and tracking usage plus the installed
instruction commands.
Enabling validates every descendant `SKILL.md` before atomically queuing the
zero-length `user.lore.plugins.openlore.skills.v1` directory marker. Collection
inheritance stops at nested docset boundaries. Enabling and disabling require
the named `rw` grant; status and validation require read access.

`.lore` is reserved and hidden through OpenLore. It contains portable folder
metadata and travels with host-level copies, Git, Syncthing, archives, and
backups; do not edit it directly. Deleting metadata outside OpenLore cannot be
distinguished from metadata that never existed, so retain repository or backup
history. `--recreate-xattrs` is an explicit corruption-recovery operation: it
preserves the unreadable bytes as `self.conflicted.<sha256>`, then recreates only
the requested Skills marker state. It cannot recover unrelated attributes.

OpenLore's xattr errors follow a normalized Linux-shaped contract, but the
metadata is synthetic canonical CBOR rather than native host xattrs. Backend
support is detected by attempting the operation, never from the kernel version.

Imports accept `owner/repo` shorthand (GitHub) or an HTTPS repository URL on
GitHub, GitLab, Bitbucket, Codeberg, or self-hosted GitLab/Gitea/Forgejo.
Only public repositories are supported; fetches are restricted to HTTPS hosts
resolving to public IP addresses. Run `skills` for import syntax, automatic
branch tracking, pinning, status checks, and unlinking.
