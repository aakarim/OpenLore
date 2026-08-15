# Writing and Publishing

OpenLore is read-only by default. Set `readonly: false` to enable its writable
substrate; identity and docset policy still determine which paths each session
can change. Embedded-docs binaries cannot be made writable.

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
echo "# API Notes" | publish backend api-notes.md
echo "# Research" | ssh -p 2222 server publish backend research/findings.md
ssh -p 2222 server publish  # list available inboxes
```

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
deletion; use `rw` for unrestricted writes within the docset.

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

## Human approval

Selected paths can route writes and deletes into pending changesets instead of
committing immediately:

```json
{
  "docsets": {
    "ops": {
      "paths": ["/ops"],
      "requires_approval": [
        { "path": "/ops/policy.md", "capability": "approve@oncall" }
      ]
    }
  }
}
```

Pending changes appear under `/requests`. An identity with the required
capability reviews and commits them with `approve`. A pending delete preserves
an exact subtree snapshot for review.

## Asynchronous jobs

The optional `spawn` command lets explicitly trusted identities run configured
external work and write its output back later. Grant the `spawn` capability on a
role and bound concurrency:

```yaml
readonly: false
max_jobs: 8
```

Jobs appear under `/jobs`. Their write-back goes through the same path scope,
compare-and-swap checks, validation, and approval policy as an interactive
write. A normal session without this explicit capability cannot execute host
processes.

## Validation and hooks

All write verbs converge on one write seam. Plugins can reject content before
commit, require knowledge-format conformance, enrich metadata, or react after a
successful commit without creating alternate mutation paths.

See [Plugins and knowledge formats](plugins.md) for policy extensions and
[Write system internals](write-system.md) for the filesystem and commit model.
