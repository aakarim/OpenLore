# Docsets

A docset is a named part of the served tree together with the policy that
governs it. Every question OpenLore asks about a file — who may read it, who may
change it, which rules check it, where contributions land — is answered by the
docset that owns the file.

Docsets are declared in `lore.json`, the policy file named by `auth_file` in
`openlore.yml`:

```json
{
  "docsets": {
    "handbook": {
      "paths": ["/handbook"],
      "access": { "allow": { "guest": "ro", "backend": "ro", "research": "ro" } }
    },
    "backend": {
      "paths": ["/teams/backend"],
      "aliases": ["/backend"],
      "inbox": "inbox",
      "access": { "allow": { "backend": "rw", "research": "publish" } },
      "rules": {
        "doc-size": { "match": ["**/*.md"], "use": "size/kilobytes", "with": { "max": 60 } }
      }
    }
  },
  "roles": { "backend": {}, "research": {} }
}
```

An agent with the `backend` role sees `/handbook` and `/teams/backend` and can
change files in the second. A research agent sees the same two docsets but can
only add files under `/teams/backend/inbox`. A keyless caller sees `/handbook`
and nothing else.

## Why docsets

A knowledge repository usually starts with one audience and grows several:
public guides, one team's runbooks, another team's design notes, an agent's own
scratch space. Without docsets you would run a server per audience, or copy
files into per-agent repositories and let them drift. Docsets let one server and
one tree serve all of them.

- **Each identity sees only what it was granted.** A docset an identity has no
  grant on is not listed, not searchable, and not reachable by path. Agents
  cannot `grep` their way into another team's notes, and you do not have to
  trust them not to.
- **The same answer on every transport.** SSH, SFTP, MCP over HTTP, the JSON
  API and the dashboard all resolve access through docsets, so one policy
  covers every way into the server. See [Transports](transports.md).
- **Per-area behaviour lives in one place.** Access, [folder rules](folder-rules.md),
  the [inbox](writing.md#publish-to-an-inbox), write conflict policy, a
  per-area read-only lock and write size limits are all set on the docset that
  needs them, not server-wide.
- **Layout is independent of storage.** Path mappings and aliases present
  folders where agents expect them without moving anything on disk.
- **Agents can see their own scope.** `lore docsets` lists the docsets a
  session can access, with its grants and attributes such as `home`, `inbox`
  and `alias`.

## How a docset is defined

| Key | What it does | Covered in |
|---|---|---|
| `paths` | The display paths the docset owns. An entry is either a path or a `{ "source": "display" }` mapping that shows a source folder at a different path. | [Docset paths](auth.md#docset-paths) |
| `aliases` | Extra display roots for the first path. | [Path aliases](auth.md#path-aliases) |
| `access` | `allow` maps role names to a grant (`ro`, `publish`, `rw`, or a plugin grant); `deny` lists roles that get nothing. | [Roles, docsets, and identities](auth.md#roles-docsets-and-identities) |
| `inbox` | The folder that a `publish` grant may write into. | [Writing and publishing](writing.md#publish-to-an-inbox) |
| `rules` | Folder rules for files in this docset. | [Folder rules](folder-rules.md) |
| `config.edit` | Roles that may change `.lore/config.yaml` files in the docset. | [Folder rules](folder-rules.md#permissions) |
| `readonly`, `write_conflict_policy`, `max_write_size` | Per-docset write behaviour. | [Writing and publishing](writing.md) |

An identity can also name one docset as its `home`. That docset becomes `$HOME`
and the identity gets `rw` on it. See
[Identity home directories](auth.md#identity-home-directories).

## How docsets are designed

### Subtrees of one filesystem

Docsets are not separate stores or mounts. They are display-path subtrees of a
single virtual filesystem, so scoping is decided by path. When a session starts,
OpenLore builds a view of that filesystem containing the roots of the docsets
the identity can read, the parent directories that lead to them, and system
mounts such as `/jobs`. Everything else is absent from that session's view.

When `lore.json` is loaded, content outside every docset is visible to no one.
Without `lore.json`, OpenLore treats the whole tree as a single docset called
`public` rooted at `/`, and every session gets `rw` on it, limited by the global
`readonly` setting. The same code path serves both cases.

### The most specific docset governs

A path belongs to the docset whose root is its longest matching prefix. Nested
docsets carve their subtree out of the parent; they do not inherit from it.

```diagram
/teams                    docset: teams     (staff: ro)
├── onboarding.md         governed by teams
└── backend               docset: backend   (backend: rw)
    ├── runbook.md        governed by backend
    └── inbox/            governed by backend
```

A `staff` identity can read `/teams/onboarding.md` but not
`/teams/backend/runbook.md`, because `backend` grants `staff` nothing. The same
boundary applies to folder rules: a nested docset that does not declare a rule
is exempt from its parent's. A recursive delete in the parent cannot cross into
the nested docset.

### Grants are named, and narrowing always wins

Within a docset, each of an identity's roles contributes its grant
independently. A role listed in `access.deny` removes the docset entirely for
that identity, even if another role grants access. A delegated OAuth client can
be capped below its principal with `deny_docsets`.

Grants are named types. Core registers `ro` and `rw`; plugins register others,
such as the inbox plugin's `publish`. If `lore.json` names a grant that no
plugin provides, the server refuses to start rather than silently denying
access. The `guest` role can only hold grants that never write.

A write must pass every layer, and each layer can only narrow the one before it:

```diagram
global readonly ──▶ token scope ──▶ docset readonly ──▶ grant on that path
```

### Canonical paths and fail-closed configuration

Aliases are rewritten to the docset's first path before any check runs, so
policy, the write log, events, inboxes and `$HOME` always see the canonical
path, whichever spelling the caller used.

OpenLore rejects configurations where the governing docset would be ambiguous.
Two docsets cannot share a display root, and an alias cannot overlap another
docset's root, alias or mount, except when it sits inside a broader docset.
The server refuses to start rather than guess.

Writes land strictly below a docset root. The root itself must already exist,
and the shell cannot create a folder outside every docset. Adding a docset is an
operator change to `lore.json`, not something an agent can do by writing files.

## Next steps

- [Auth](auth.md) explains how callers resolve to identities and roles, and
  covers paths, aliases and homes in detail.
- [Writing and publishing](writing.md) covers grants that write, inboxes and
  conflict handling.
- [Folder rules](folder-rules.md) shows how docset rules combine with
  `.lore/config.yaml`.
