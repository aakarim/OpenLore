# Writing and publishing

Let agents write back into the knowledge base with atomic, attributed,
conflict-checked writes.

OpenLore is read-only by default. Set `readonly: false` in `openlore.yml` to
enable writes; [identity and docset policy](configuration-and-identity.md) still
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

## Validation

All write verbs converge on one write seam. Plugins can reject content before
commit, require knowledge-format conformance, enrich metadata, or react after a
successful commit without creating alternate mutation paths. [Folder
rules](folder-rules.md) cap file size and enforce structure through the same
seam.

## Next steps

- [Let an agent publish into an inbox](publish-to-inbox.md) for a worked example
  of a `publish` grant.
- Learn how [folder rules](folder-rules.md) reject oversized or malformed
  writes.
- Read [Write system internals](write-system.md) for the filesystem and commit
  model, and [Plugins and knowledge formats](plugins.md) for policy extensions.
