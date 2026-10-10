# Writing and publishing

Let agents write back into the knowledge repository with atomic, attributed,
conflict-checked writes.

OpenLore is read-only by default. Set `readonly: false` in `openlore.yml` to
enable writes; [identity and docset policy](auth.md) still
determine which paths each session can change. Embedded-docs binaries cannot be
made writable.

## Write operations

```bash
echo "# Notes" > /mydocset/notes.md
echo "- point" >> /mydocset/notes.md
cat input.md | tee /mydocset/copy.md
cat change.diff | patch /mydocset/x.md
sed -i 's/old/new/g' /mydocset/x.md
mkdir -p /mydocset/a/b/c
mv /mydocset/draft.md /mydocset/final.md
rm /mydocset/old.md
rm -r /mydocset/section
```

Every operation is a whole-object atomic swap. Directory moves are not
supported because the filesystem has no atomic tree-move operation; create the
destination and move files explicitly.

## Publish to an inbox

`publish` lets a contributor read a whole docset while creating or editing only
inside its inbox:

```bash
echo "# API Notes" | publish /backend/api-notes.md
```

The path is the docset name followed by the file name; the server routes it
into the docset's inbox. Run `publish` with no arguments to list the docsets you
can publish to.

Configure an inbox and grant `publish`:

```json
{
  "docsets": {
    "backend": {
      "paths": ["/docs/backend"],
      "inbox": "inbox",
      "access": { "allow": { "contributor": "publish" } }
    }
  },
  "roles": {
    "contributor": {}
  },
  "identities": [
    { "name": "research-agent", "roles": ["contributor"] }
  ]
}
```

The write lands under `/docs/backend/inbox`. A `publish` grant never permits
deletion; use `rw` for unrestricted writes within the docset. For a worked
example, see [Let an agent publish into an inbox](publish-to-inbox.md).

## Conflict handling

Overwrites use compare-and-swap by default. If a file changed since the session
read it, OpenLore rejects the stale write rather than silently clobbering newer
content. Append and patch operations always use compare-and-swap.

```yaml
readonly: false
write_conflict_policy: hash  # hash (default) or last_write_wins
```

Override the policy for a specific docset in `lore.json`:

```json
{
  "docsets": {
    "ops": {
      "paths": ["/ops"],
      "write_conflict_policy": "hash"
    }
  }
}
```

## Deferred writes

A write plugin can hold a write instead of committing or rejecting it. The
command then reports the write as pending rather than done:

```text
tee: /ops/policy.md change pending as <ref>
```

OpenLore core does not ship a review queue; the plugin that deferred the write
owns the reference and decides when, or whether, the write is committed. See
[Write system internals](write-system.md#7-deferred-writes) for the seam.

## Asynchronous jobs

The optional `spawn` command lets explicitly trusted identities run configured
external work and write its output back later. Grant the `spawn` capability on a
role and bound concurrency:

```yaml
readonly: false
max_jobs: 8
```

Jobs appear under `/jobs`. Their write-back goes through the same path scope,
compare-and-swap checks, and validation as an interactive write. A normal session without this explicit capability cannot execute host
processes.

## Saved is not validated

A write that exits 0 has passed every check that looks at **one file in
isolation**. It has not passed the checks that need to see **related files**.
Those run only when you ask for them with `lore validate`. This is why a
document with a broken link saves successfully and then fails validation: the
link target is another file, and the write path never looks at other files.

### What runs on every write

Each check below runs before the bytes reach disk. A failure exits non-zero,
prints the reason on stderr, and nothing is written.

| Check | What it decides | Rejection looks like |
|---|---|---|
| Capability and scope | Is the verb allowed for this identity, and is the path inside a docset it may write? | `redirect: /docs/x.md: read-only filesystem` |
| Compare-and-swap | Has the file changed since the session last read it? | `redirect: /docs/x.md: file changed concurrently — re-read and retry` |
| Substrate limits | Byte cap, denied filenames, ignored paths | `… write rejected: 9000000 bytes exceeds limit of 8388608`, `… access denied: /docs/.env` |
| Folder-config permission | Writing `.lore/config.yaml` needs a role in `config.edit`; the config itself must decode and unify with the layers above it | `.lore/config.yaml: rules.doc-size: conflicts with …` |
| **File-scope folder rules** | `okf`, `size/kilobytes`, `size/lines`, `size/tokens`, evaluated against the proposed content of each file in the operation | `okf: /docs/bad.md: missing YAML frontmatter block …` |
| Plugin and `shellexec` `pre_commit` middleware | Anything a plugin or configured command rejects or defers | plugin-specific, or `… change pending as <ref>` |

An `enforce: false` rule logs a warning on the server and lets the write
through; it never appears in the command's output.

### What runs only under `lore validate`

| Check | Why it cannot run on write |
|---|---|
| `link/resolves` (`openlore/broken-link`, `openlore/link-outside-bundle`) | The target is a different file, which may be written next |
| `okf/bundle` (root `index.md`, `log.md`, `okf_version`) | Adding a concept legitimately breaks the index until the index is updated in a later write |
| `link/alias` (`openlore/alias-referrer`, `openlore/alias-target`, warnings) | Portability across servers, not a property of one file |
| `.lore/config.yaml` diagnostics for every config in the folder | The configs were valid when written; validate re-checks them together |

`lore validate <dir>` also re-runs the file-scope rules over every file under
`<dir>`, so it catches files that were written before a rule was added or that
arrived outside OpenLore (a git checkout, `sshfs`). It writes nothing and never
records a size baseline. Exit status is 1 if any `error` finding was reported
and 0 if there were only warnings or none. It does not fetch URLs; external
links are never checked.

### Write → validate → finish

Run the loop below from one session. It uses a docset with an `okf` block (or
`link/resolves` under `rules`), which is what makes link checking part of
`lore validate`.

```bash
# 1. Write. Exit 0 means the file is saved and passed every file-scope check.
printf -- '---\ntype: Note\n---\n# Retry policy\n\nSee [the queue design](queue-design.md).\n' \
  > /docs/retry-policy.md

# 2. Validate the docset root, not / and not just the file's folder.
lore validate /docs
```

```text
retry-policy.md:6:6: error [openlore/broken-link] local link "queue-design.md" does not resolve
see: lore package doc link/resolves
1 error, 0 warnings
```

```bash
# 3. Fix the finding: write the missing target (or correct the link), then re-validate.
printf -- '---\ntype: Note\n---\n# Queue design\n' > /docs/queue-design.md
lore validate /docs
```

```text
0 errors, 0 warnings
```

```bash
# 4. Finish only when validate exits 0.
lore validate /docs && echo done
```

Two details about step 2:

- **Name the docset root.** `lore validate` checks links against the folder it
  was given. Validating a subfolder reports a link to a sibling folder as
  `openlore/link-outside-bundle`, even though the target exists; validating
  `/` is refused when docsets below it carry bundle rules
  (`lore validate: / is above docsets docs with bundle rules; run lore validate per docset`).
- **Validate once per batch, not per file.** Bundle findings depend on the
  whole set of files, so write everything the change needs, then validate.
  A broken-link error in the middle of a multi-file write is expected until
  the last file lands.

In a CI or scripted flow, the write commands and `lore validate` can run over
separate SSH connections; the exit status of `lore validate` is the gate.

## Next steps

- [Let an agent publish into an inbox](publish-to-inbox.md) for a worked example
  of a `publish` grant.
- Learn how [folder rules](folder-rules.md) reject oversized or malformed
  writes.
- Read [Write system internals](write-system.md) for the filesystem and commit
  model, and [Plugins and knowledge formats](plugins.md) for policy extensions.
