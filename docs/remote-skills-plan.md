# Plan: Remote Skills

Status: **accepted design; ready for implementation**

A skill folder can be linked to a public GitHub repository through a `remote`
key in its SKILL.md YAML frontmatter. Linked skills are imported with
`skills import`, kept current on read (branch refs) or held stable (tag/SHA
refs), and protected from local edits except deliberate link management.

## Goals

- Declare a skill's upstream in the SKILL.md frontmatter itself: repository,
  optional subdirectory, and a ref (branch, tag, or commit SHA).
- Track mutable refs: a branch-linked skill serves whatever is on the branch
  head, synced during the read that notices staleness.
- Pin immutable refs: a tag- or SHA-linked skill never changes until the user
  edits the ref or runs `skills update`.
- Keep local content byte-identical to upstream except for the grafted
  `remote` block in SKILL.md.
- Reject local edits to linked skills with a clear error, while allowing a
  surgical frontmatter edit that removes or changes the `remote` block.
- Report transient remote failures to the reading agent by injecting a
  `remote-status` key into the *served* frontmatter, never the stored file.
- No new Go dependencies: git smart-HTTP for ref resolution, codeload tarballs
  for content, both over `net/http`.

## Non-goals for v1

- Private repositories or any authentication.
- Hosts other than GitHub. Ref resolution uses the generic git smart-HTTP
  protocol, so other hosts later only need a content-fetch strategy.
- Merging local edits with upstream changes. Linked skills are read-only.
- Following moved tags. Tags are treated as immutable; re-tagging upstream is
  not detected.
- Background or asynchronous sync. All sync work happens inline in the read
  that triggers it, or in an explicit command.
- Detecting out-of-band unlinking. If the backing file is edited outside
  OpenLore (direct filesystem access, `git pull`) and the `remote` key is
  lost, the skill silently becomes local. Version control is the backstop.
- Importing a whole multi-skill collection in one command.
- Signature or provenance verification of upstream content.

## User model

### Frontmatter schema

```yaml
---
name: pdf
description: Process PDF files.
remote:
  repo: anthropics/skills        # owner/repo on github.com; public only
  path: document-skills/pdf      # optional subdirectory; default repo root
  ref: main                      # branch (tracks) | tag or SHA (pins)
  commit: 4f2a9c…                # sync-maintained: last applied commit SHA
  kind: tracking                 # sync-maintained: tracking | pinned
---
```

- `repo` (required): `owner/repo`. GitHub is implied in v1.
- `path` (optional): directory within the repo containing the skill.
- `ref` (required): a branch name, tag name, or 40-hex commit SHA. Whether it
  is mutable is determined at sync time from the remote's advertised refs,
  GitHub-Actions style: `@main` follows, `@v1.2.0` pins.
- `commit` (sync-maintained): the commit SHA whose content is currently
  stored. Written by import and every sync. Human-readable provenance.
- `kind` (sync-maintained): whether the resolved ref is `tracking` or
  `pinned`. It is stored so tag-linked skills remain zero-network after a
  process restart. A surgical ref edit clears both `commit` and `kind`.

`agentskills.Validate` adds `remote` to the frontmatter allowlist and
validates its sub-keys; unknown sub-keys are rejected. `remote-status` is
rejected in stored content so nobody can fake injected status on disk.

### Import

```
skills import anthropics/skills/document-skills/pdf@v1.2.0
skills import https://github.com/anthropics/skills/tree/main/document-skills/pdf
skills import owner/repo [parent-dir]
```

Both spellings normalize to the same `{repo, path, ref}`. The URL form maps
`/tree/{ref}/{path}` directly.

- Omitted ref → the repo's default branch (from the smart-HTTP HEAD symref),
  i.e. **tracking by default**. Import output prints
  `tracking branch 'main' — pin with @<tag> for reproducibility`.
- Omitted path → the fetched tree is scanned for directories containing
  SKILL.md: exactly one → imported; several → rejected with the candidate
  list; none → error. No guessing.
- Destination is `<cwd>/<skill-name>` (name from the upstream SKILL.md; the
  format already requires name == directory name). An optional trailing
  argument overrides the parent directory.
- Import refuses an existing destination, and requires the destination to be
  inside a skills-enabled collection in a docset with an `rw` grant — the same
  checks `skills enable` performs.
- Import fetches the tarball at the resolved SHA, validates the incoming
  SKILL.md, grafts the `remote` block (including `commit`), and applies all
  files as one change set through the write queue.

### Read behavior

For a SKILL.md read inside an enabled collection whose stored frontmatter has
a `remote` block, a before-read hook runs:

1. **TTL gate.** An in-memory per-skill timer (default 60s) short-circuits
   repeat checks. Nothing about check times is persisted.
2. **Pinned refs** (`ref` is a tag or SHA and `commit` is present): no network
   at all. The read serves stored content.
3. **Branch refs**: resolve the branch head with one smart-HTTP `info/refs`
   request (default timeout 3s). Head == `commit` → serve stored. Head moved →
   fetch the codeload tarball at the new SHA, validate, graft the `remote`
   block with the updated `commit`, and apply files and deletions as a single
   plugin-originated change set through the write queue. The read then
   proceeds and serves the fresh stored file. The read **blocks** on this.
4. **Missing `commit`** (fresh ref edit): treat as stale regardless of ref
   type; resolve and sync as above.

Failure modes never fail the read. The stored version is served and a
transform layer injects one key into the served frontmatter:

```yaml
remote-status: "remote unreachable; serving stored version (commit 4f2a9c) — run 'skills update' to retry"
remote-status: "upstream at 9be01d is not a valid skill; update refused, serving stored version (commit 4f2a9c)"
```

Healthy reads serve stored bytes verbatim — no injection.

### Write protection

Any write to any file under a linked skill is rejected with:

```
remote is set; change it upstream or run 'skills remove-remote'
```

**The one exception — surgical frontmatter edits.** A write to SKILL.md is
admitted iff the incoming content is identical to the stored content except
for the `remote` block:

- `remote` removed → the skill is unlinked and becomes a normal local skill.
- `repo`, `path`, or `ref` changed → re-link. The middleware clears `commit`
  so the next read (or `skills update`) syncs against the new target.

Everything else — body edits, other frontmatter keys, sibling files — is
rejected. Injected `remote-status` keys are stripped from incoming content
before the comparison, so an agent that read an error-annotated file can
still perform a valid surgical edit.

Deleting the whole skill folder remains allowed (existing behavior).

Sync writes originate from the plugin itself and carry a plugin-originated
marker on the change set; the write middleware admits them.

### Commands

- `skills import <spec> [parent-dir]` — as above. Emits JSON records like the
  existing subcommands.
- `skills update [folder]` — force a sync now, any ref type. Defaults to cwd.
  Re-resolves the ref, fetches if the content differs, reports old → new SHA.
- `skills remove-remote [folder]` — sugar for the surgical unlink write.
- `skills status` — extended to show, per linked skill: repo, ref, ref type
  (tracking/pinned), stored commit, and the last check outcome if cached.

### Configuration

Under the existing plugin block in `openlore.yml`:

```yaml
plugins:
  skills:
    enabled: true
    remote_check_ttl: 60s    # min interval between branch-head checks per skill
    remote_timeout: 3s       # network timeout for ref check and tarball fetch
    remote_max_bytes: 10MB   # cap on extracted skill content per sync
```

## Fetching mechanics

- **Ref resolution:**
  `GET https://github.com/{owner}/{repo}/info/refs?service=git-upload-pack` —
  the endpoint `git ls-remote` uses. One response advertises the HEAD symref
  (default branch), all branch heads, and all tags (peeled `^{}` entries give
  the commit for annotated tags). Pkt-line parsing is ~50 lines. Not subject
  to the GitHub REST API rate limit.
- **Content:** `GET https://codeload.github.com/{owner}/{repo}/tar.gz/{sha}`,
  fetched by the **resolved SHA**, never the ref — resolution and fetch cannot
  race. Only the `path` subtree is extracted.
- **Extraction safety:** symlinks and hardlinks are skipped; entries with
  path traversal are rejected; `.lore/` entries in the upstream tree are
  ignored (reserved); the size cap and a file-count cap abort oversized
  skills; the incoming SKILL.md must pass `agentskills.Validate` (with the
  grafted `remote` block) **before** any file is applied. An invalid upstream
  refuses the update and surfaces via `remote-status`.

## Architecture notes

- **Read pipeline placement.** The sync trigger uses the existing before-read
  middleware contract (`ReadMiddlewareProvider`), which can do work and abort
  but not rewrite bytes. Status injection requires a new, minimal
  content-transform read contract (`Transform(path, content) → content`)
  contributed by plugins. The transform runs **outside** the read-tracking
  layer: CAS hashes recorded at read time must reflect stored bytes, not
  injected bytes, or every subsequent CAS write would mismatch.
- **Frontmatter grafting.** Sync takes the upstream SKILL.md, parses its
  frontmatter, inserts the local `remote` block (with the new `commit`), and
  re-serializes. Local files therefore differ from upstream by exactly that
  block; any diff logic normalizes it away.
- **State inventory.** Durable state lives in exactly one place: the `remote`
  block in the stored SKILL.md. In-memory state is the per-skill check TTL
  and the last failure message for `skills status`. Nothing is added to
  xattrs, `.lore`, or any index.

## Implementation map

- `pkg/agentskills/validate.go` — `remote` allowlist entry + sub-key
  validation; reject `remote-status` in stored content.
- `pkg/agentskills` (new file) — frontmatter graft/strip/compare helpers used
  by sync, the surgical-write check, and injection.
- new package `pkg/openlore/skillsremote` (or `internal/gitremote`) —
  smart-HTTP `info/refs` pkt-line parsing, codeload tarball fetch, safe
  subtree extraction, spec parsing (`owner/repo[/path][@ref]` and GitHub tree
  URLs).
- `pkg/openlore/agent_skills_plugin.go` — before-read sync trigger with TTL;
  content-transform provider for `remote-status`; write-middleware extension
  implementing the surgical-edit rule and the plugin-originated bypass.
- `pkg/openlore/middleware.go` / `server.go` — the new content-transform read
  contract, wired outside read tracking.
- `pkg/shell/cmds/skills.go` — `import`, `update`, `remove-remote`
  subcommands; `status` extension.
- `internal/config` — the three `remote_*` settings.

## Test plan

- **Unit:** remote schema validation; graft/strip round-trips; surgical
  comparison edge cases (key removed → unlink, ref changed → commit cleared,
  body change → rejected, injected key stripped before compare); spec parsing
  for both spellings.
- **Fake GitHub** (`httptest` server serving `info/refs` and tarballs):
  import happy path; branch tracking detects a moved head and applies through
  the write queue; pinned ref performs zero requests; unreachable remote →
  stored content served with injected `remote-status`; invalid upstream →
  update refused with injected status; TTL suppresses repeat checks; timeout
  falls back cleanly.
- **Extraction safety:** traversal entries, symlinks, `.lore` entries,
  oversized archives.
- **CAS:** a read that received injected content can still complete a valid
  surgical CAS write.

## Decisions log

| Decision | Choice |
| --- | --- |
| Source of truth for the link | `remote` block in SKILL.md frontmatter |
| Sync state | `remote.commit`, sync-maintained in the same block |
| Branch refs | auto-apply on read (blocking), TTL-gated |
| Tag/SHA refs | pinned; no network on read; explicit `skills update` |
| Default ref on import | default branch — tracking by default |
| Fetch mechanism | git smart-HTTP `info/refs` + codeload tarball by SHA |
| Write protection | block all, except surgical `remote`-block-only SKILL.md edits |
| Unlink | remove the `remote` key (surgically) or `skills remove-remote` |
| Failure reporting | `remote-status` injected into served frontmatter only |
| Out-of-band unlink detection | none in v1; version control is the backstop |
