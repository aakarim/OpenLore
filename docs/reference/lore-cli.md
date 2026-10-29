<!-- Hand-written: the lore client uses the standard flag package without a help generator, so this page is kept in step with cmd/lore/main.go by hand. -->

# lore CLI reference

`lore` is the client binary that syncs external sources to a running OpenLore server; the server itself is the `openlore` binary.

The `openlore` binary has its own [openlore CLI reference](commands.md). Both binaries ship in the same release archive and Homebrew cask. Run `lore` inside your knowledge repository: it walks up from the working directory to the folder that holds `openlore.yml`, or falls back to the repository of your last `lore login` when it reaches the root without finding one, and reads `home/<identity>/sources.yaml` for the identity you logged in with. Every command except `login` needs a completed `lore login`. Flags can follow positional arguments.

```text
Usage: lore <command> [flags]

Commands:
  login [server-url] [--token t]   Sign in and store the identity for this repository
  source add <path|git-url>        Add a committed folder or Git source
  source add claude [path]         Add Claude Code groups for a project directory
  source list                      Show sources, store paths, projections and last snapshot
  source rm <name>                 Remove a definition and its local state
  status [name...]                 Show what a sync would change
  sync [name...] [--dry-run]       Push sources to the server
  agent import [claude] [path]     Guided Claude Code source setup
  agent connect claude|codex       Register the server with a coding agent
```

Learn how the pieces fit in the [knowledge gateway](../concepts/knowledge-gateway.md).

---

## Commands

### `lore login`

**Usage:** `lore login [server-url] [--token <token>]`
**Default:** `server-url` is `external_url` from `openlore.yml`

Signs in with a browser passkey flow, or with a minted token when `--token` is given, and records the server, identity and credentials under `~/.config/lore/` keyed by the repository path. A successful login also makes this repository the default for `lore` commands run outside any knowledge repository.

```shell
lore login https://lore.example.com --token "$TOKEN"
```

### `lore source add`

**Usage:** `lore source add <path|git-url> [--name <name>] [--branch <branch>] [--include <glob>]... [--exclude <glob>]... [--dry-run]`
**Usage:** `lore source add claude [path] [--project] [--personal] [--name <name>] [--include <glob>]... [--exclude <glob>]... [--dry-run]`
**Default:** `--name` is the folder's base name or the repository name; `--branch` is the remote's default branch; `path` is the working directory

Appends a folder, Git or Claude Code source to `home/<identity>/sources.yaml` and prints the store path it will sync to; `--dry-run` prints without writing. For `claude`, `path` is the directory you run Claude Code in, and `--project` or `--personal` limits the picker to that scope and ticks its groups, except opt-in groups; with neither flag every group is offered and nothing is ticked.

```shell
lore source add https://github.com/acme/payments --branch main --include 'docs/**'
```

### `lore source list`

**Usage:** `lore source list`

Prints one line per source with its name, type, origin, store path, projecting docsets and the snapshot this identity last pushed.

```shell
lore source list
```

### `lore source rm`

**Usage:** `lore source rm <name>`

Removes the definition from `home/<identity>/sources.yaml`, forgets your identity's snapshot state for it and deletes the local clone or copy under `.local/sources/<store>`; snapshots on the server are kept.

```shell
lore source rm payments
```

### `lore status`

**Usage:** `lore status [name...]`

Reports the files a sync would add, change and remove per source and how many blobs it would upload, without uploading anything.

```shell
lore status notes
```

### `lore sync`

**Usage:** `lore sync [name...] [--dry-run]`

Refreshes each selected source, uploads blobs the server lacks and commits one snapshot per source, exiting non-zero if any source is rejected while the others still run.

```shell
lore sync
```

### `lore agent import`

**Usage:** `lore agent import [claude] [path] [--project] [--personal]`

Runs the guided Claude Code setup, identical to `lore source add claude` and accepting the same flags: an interactive group picker, a file preview and a confirmation, with Esc cancelling; with no profile it lists the available profiles.

```shell
lore agent import claude --project
```

### `lore agent connect`

**Usage:** `lore agent connect claude|codex`

Registers the server's MCP endpoint with Claude Code or Codex, writes the server's `agents` snippet into `CLAUDE.md` or `AGENTS.md` in the knowledge repository, then offers to run `lore agent import claude`.

```shell
lore agent connect claude
```

---

## Source types

### `folder`

**Store:** `folder/<identity>/<name>`
**Keys:** `name`, `type`, `path`, `include`, `exclude`

Syncs a directory on your machine; symbolic links are skipped.

```yaml
sources:
  - name: notes
    type: folder
    path: ~/notes
    exclude: ["drafts/**"]
```

### `git`

**Store:** `git/<host>/<org>/<repo>`
**Keys:** `name`, `type`, `url`, `branch`, `include`, `exclude`

Clones the remote into `.local/sources/git/<host>/<org>/<repo>/repo`, checks out the branch tip on every sync and syncs the working tree; the first identity to sync a repository owns its store.

```yaml
sources:
  - name: payments
    type: git
    url: https://github.com/acme/payments
    branch: main
    include: ["docs/**", "*.md"]
```

### `claude`

**Store:** `claude/<identity>/<project-key>/<group>` or `claude/<identity>/personal`
**Keys:** `name`, `type`, `group`, `project`, `include`, `exclude`

Syncs one group of Claude Code files, `instructions`, `memory`, `transcripts` or `personal`, resolving `project` on each machine to Claude Code's encoded directory name. `transcripts` is opt-in: the picker never ticks it and its `*.jsonl` contents are not filtered.

```yaml
sources:
  - name: payments-claude-memory
    type: claude
    group: memory
    project: ~/src/payments
```

---

## Filters

`include` and `exclude` are glob lists matched against source-relative paths. `*` matches within one path segment and `**` matches zero or more segments, so `*.md` matches only the root and `**/*.md` matches every directory. Exclusions win over inclusions.

The Claude profile always excludes `settings*.json`, agent memory directories, `.env*` files, key and certificate files, and names containing `credential`, `secret`, `token`, `password` or `api_key`. Only the `transcripts` group looks for `*.jsonl` session transcripts; Claude Code keeps them beside, not inside, the `memory/` directory the other groups read.

---

## Files

### `home/<identity>/sources.yaml`

**Location:** knowledge repository root, one folder per identity
**Committed:** yes

Lists the sources one identity syncs; the server validates every `home/*/sources.yaml` at startup and never fetches from them.

```yaml
version: "1"
sources: []
```

### `.local/`

**Location:** knowledge repository root
**Committed:** no

Holds `sources/<name>` clones and copies and `sync.json`, the per-identity record of acknowledged pushes and last snapshot IDs.

```text
.local/
  sources/payments/repo/
  sync.json
```

### `~/.config/lore/`

**Location:** `$XDG_CONFIG_HOME/lore/` when set
**Committed:** no

Stores the server URL, identity and OAuth credentials for each knowledge repository path on this machine, and in `default.json` the repository of the last successful login.

```text
~/.config/lore/<repository-key>.json
~/.config/lore/default.json
```

---

## Projections

A docset in `lore.json` projects a store when it has `"projection"` instead of `"path"`. Its `access` block decides who can read it, as for any docset, and `paths` can name one absolute mount path (default `/<docset>`). A projection mounted beneath an identity's home docset is readable by that identity with no `access` block.

Apply changes with `lore config reload` in an OpenLore session holding `lore:config:edit`. Projection additions, changes and removals update existing sessions without a server restart.

| Key | Type | Description |
|---|---|---|
| `store` | string | Store path from `lore source list` |
| `subpath` | string | Directory inside the store to serve as the docset root |
| `include` | string list | Globs a file must match to be served |
| `exclude` | string list | Globs that hide a file |

Projected docsets reject writes on every transport. The example mounts a personal store inside a home:

```json
{
  "alice-claude": {
    "paths": ["/user/alice/claude"],
    "projection": { "store": "claude/alice/personal" }
  }
}
```

Learn more about [docsets](../concepts/docsets.md).

---

## HTTP API

`lore` talks to these endpoints under `api.path` with a bearer token; `api.enabled` must be true.

| Method and path | Purpose |
|---|---|
| `GET /api/docstore/identity` | Return the identity the token resolves to |
| `POST /api/docstore/{store}/snapshots` | Prepare a snapshot from a manifest; returns its ID and missing hashes |
| `PUT /api/docstore/blobs/{sha256}` | Upload one blob requested by a pending snapshot |
| `POST /api/docstore/{store}/snapshots/{id}/commit` | Publish the pending snapshot |
| `GET /api/docstore/` | List stores the identity owns or can read through a projection |
| `GET /api/docstore/{store}` | Latest manifest |
| `GET /api/docstore/{store}/snapshots` | Retained snapshot history |
| `GET /api/docstore/{store}?file=<path>` | Read one file from the latest snapshot |

Prepare and commit are idempotent. A snapshot whose parent is no longer the latest is rejected. Limits are 8 MiB per file, 128 MiB per snapshot and 10,000 files.
