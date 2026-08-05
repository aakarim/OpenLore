# Plan: Folder Xattrs and the Skills Plugin

Status: **accepted design; ready for implementation**

OpenLore will add Linux-shaped extended attributes to directories in its
virtual filesystem. The attributes are synthetic: OpenLore stores them in a
portable `.lore` control directory rather than setting native attributes on the
backing filesystem.

The first consumer is the Skills plugin. Operators enable the plugin globally
in `openlore.yml`; users with `rw` access mark collection folders through a
`skills` convenience command. Users do not need to understand xattr storage.

## Goals

- Store arbitrary opaque values under `user.lore.*` on directories.
- Preserve the useful Linux xattr limits, flags, empty-value semantics, and
  errors.
- Put all metadata writes through OpenLore's existing serialized write queue.
- Make attributed folders self-contained and portable across Git, Syncthing,
  archives, backups, and host-level recursive copy/move.
- Port Skills collection designation from `lore.json` to one versioned xattr.
- Keep Skills discovery dynamic, authorization-scoped, and free of a durable or
  in-memory collection index.
- Leave a deliberate extension point for file xattrs and implement the plugin
  schema migration contract needed to evolve semantic marker versions safely.

## Non-goals for v1

- Native host-filesystem xattrs.
- Xattrs on regular files, symlinks, devices, sockets, or other non-directory
  objects.
- Portable emulation of `security.*`, `trusted.*`, or `system.*`.
- ACLs, SELinux labels, capabilities, or Linux privilege emulation.
- Generic shell, MCP, SFTP, or FUSE xattr commands. The Go VFS contract and the
  Skills convenience commands are the v1 surfaces.
- A collection cache, filesystem watcher, polling loop, or persistent index.
- Automatic merge of externally conflicted CBOR.
- Detecting deletion of metadata after restart. Missing metadata means no
  attributes; Git, sync history, or backups provide recovery.
- Subtree exclusions inside a recursive Skills collection.
- Interpreting the removed `lore.json` `agent_skills` field. It is ignored
  without warning; there are no external users to migrate yet.
- Kernel-version compatibility checks. Distribution kernels backport features,
  so `uname` is never sufficient evidence of a backend capability.

## User model

### Enable the plugin

The plugin is disabled by default and enabled globally:

```yaml
# openlore.yml
plugins:
  skills:
    enabled: true
```

Disabling the plugin is non-destructive. Stored markers and `.lore` content
remain untouched. Skills interpretation and management subcommands become
unavailable until the plugin is enabled again; ordinary content access is not
affected.

### Manage collections

The existing bare `skills` command continues listing installed instruction
commands. Add management subcommands:

```bash
skills                              # existing installed-skill listing
skills status [folder]
skills enable [folder]
skills disable [folder]
skills validate [scope]
```

`folder` and `scope` default to the current directory. `status` and `validate`
require read access. `enable` and `disable` require `rw` access to the target.
Anyone with `rw` may manage the OpenLore Skills marker; it is not restricted to
an operator role.

All management output is NDJSON by default. Commands put no prose on stdout.
Operational failures may use stderr, but validation and result records retain
stable JSON fields and rule identifiers.

### Collection marker

The exact v1 marker is a present zero-length value:

```text
user.lore.plugins.openlore.skills.v1 = <zero bytes>
```

Plugin-owned attributes follow:

```text
user.lore.plugins.<publisher>.<plugin>.v<semantic-schema>
```

`openlore` is the first-party publisher. Publisher and plugin segments use
lowercase ASCII letters, digits, and hyphens. The `vN` suffix versions that
plugin's semantic key/value contract, not the xattr API or sidecar envelope.
Unknown attributes remain opaque to core OpenLore.

## Xattr VFS contract

Keep `vfs.FileSystem` and `vfs.WritableFS` source-compatible. Add optional
capabilities:

```go
type XattrReader interface {
    GetXattr(path, name string) ([]byte, error)
    ListXattrs(path string) ([]string, error)
}

type XattrWriter interface {
    SetXattr(path, name string, value []byte, flags XattrFlags) error
    RemoveXattr(path, name string) error
}
```

The Go API allocates complete values and lists. It does not copy syscall buffer
probes into every VFS wrapper. A future syscall-shaped adapter can add
`(required int, err error)` probes and `ERANGE` for undersized caller buffers.

Only directories support the capability in v1. An existing regular file or
symlink returns `ENOTSUP`. Path lookup and storage traversal must not follow
symlinks. Only complete names under `user.lore.*` are accepted; all other
namespaces return `ENOTSUP`. Values under the accepted namespace remain opaque
to core OpenLore.

### Flags

- No flags: create or replace.
- `XATTR_CREATE`: create only; return `EEXIST` when present.
- `XATTR_REPLACE`: replace only; return `ENODATA` when absent.
- Both flags or unknown bits: return `EINVAL`.

Flags are evaluated at queued commit time, not when the operation is proposed.
That preserves their meaning across approvals and concurrent writes.

### Limits

- Fully qualified attribute name: at most 255 bytes, counted as bytes rather
  than Unicode code points.
- Individual value: at most 65,536 bytes.
- Encoded name list: at most 65,536 bytes, calculated as
  `sum(len(name) + 1)` for Linux-compatible NUL termination.
- Complete canonical CBOR envelope: at most 8 MiB per directory.

Values may contain arbitrary bytes, including NUL and invalid UTF-8. A present
zero-length value is distinct from an absent attribute. V1 names are UTF-8 text;
names containing invalid UTF-8 or NUL are invalid. Names are returned in UTF-8
byte order for deterministic OpenLore output; no claim is made that Linux
orders `listxattr` results.

A set that would make the name list exceed 64 KiB is rejected with `ERANGE` so
OpenLore never persists an unlistable attribute set. An update that exceeds the
8 MiB envelope cap returns `ENOSPC`.

### Errors

Errors are matchable with `errors.Is` and hide CBOR implementation details:

| Condition | Error |
|---|---|
| absent named attribute on get/remove | `ENODATA` |
| create-only attribute already exists | `EEXIST` |
| replace-only attribute is absent | `ENODATA` |
| unsupported namespace, backend, or object type | `ENOTSUP` |
| name/value/list limit exceeded | `ERANGE` |
| envelope cap, disk, or quota capacity exhausted | `ENOSPC` |
| write scope denied or substrate read-only | `EPERM` |
| target path absent | `ENOENT` |
| malformed CBOR, duplicate keys, or checksum failure | corruption error matching `EIO` |
| invalid flags, invalid UTF-8, or NUL in name | `EINVAL` |

This is an OpenLore normalization, not a promise that every Linux filesystem
uses the same errno in every case. Linux may use `EROFS`, `EACCES`, `EDQUOT`, or
`E2BIG` in related situations.

## Portable storage

### Layout

Every attributed directory stores its own metadata:

```text
<directory>/
└── .lore/
    └── xattrs/
        └── self
```

Examples:

```text
/.lore/xattrs/self
/skills/.lore/xattrs/self
/teams/backend/.lore/xattrs/self
```

`.lore` is always reserved by OpenLore. The entire subtree is omitted from
public VFS reads, directory listings, walks, search, web views, MCP, SFTP, and
metadata scans. Direct public `Stat`, read, write, rename, or removal of a
`.lore` path is denied. Internal xattr storage uses a private backing-store seam
rather than the public VFS.

Host-level tools still see `.lore`. It is durable, portable project data and is
expected to travel with the folder through recursive copy/move, Git, Syncthing,
archives, and backups. OpenLore does not add it to ignore files. Future
machine-local state must use another location or an explicitly nonportable
subtree.

The `xattrs` path is a directory now so future file metadata can coexist without
changing the folder format, for example under `.lore/xattrs/files/`. V1 reads
only the canonical `self` file and ignores every sibling file or directory.

### Why folder-local `.lore`

This layout was chosen over parent sidecars, a project-root mirror, adjacent
files, and a central database:

**Benefits**

- A folder is self-contained; host-level moves, copies, archives, and sync carry
  its metadata naturally.
- There is no central path mapping or project-wide metadata database.
- Loss or corruption has folder-level rather than project-level blast radius.
- `.lore` is one stable control namespace that can hold future portable
  OpenLore metadata.

**Costs**

- Every attributed folder gains an internal dot directory.
- A legitimate user child named `.lore` is impossible.
- Host-level deletion remains possible and cannot be distinguished from “never
  had xattrs” after restart.
- Binary CBOR needs OpenLore tooling rather than hand editing.

**Rejected storage alternatives**

| Layout | Ergonomic benefit | Cost that ruled it out for v1 |
|---|---|---|
| parent sidecar | keeps the attributed folder itself clean and has an exact parent-visible mapping | ordinary host moves/copies can separate a folder from its metadata |
| project-root `.lore` mirror | one `.git`-like control location and less per-folder machinery | path mapping, cross-docset authorization, merge behavior, and project-wide deletion blast radius are substantially more complex |
| adjacent sidecar file | mapping is obvious to host users and tooling | visible clutter, filename collisions, and the same move/separation problem |
| central database | protects metadata from accidental direct deletion and simplifies indexed lookup | poor Git/sync/archive portability, centralized recovery, and unnecessary operational state |

No sidecar owned by the same host user can prevent deletion by that user outside
OpenLore. The selected layout optimizes for metadata travelling with its folder,
while OpenLore's public interfaces hide and protect the reserved subtree.

Syncthing's `.stfolder` is useful precedent for self-contained root metadata and
for stopping dangerous behavior when independently known state disappears. It
is not an inventory solution: Syncthing can detect its missing marker because
it retains independent configured-folder state. OpenLore deliberately does not
add a second catalog in v1.

### Envelope

`.lore/xattrs/self` is canonical CBOR containing:

```text
format:     "openlore.xattrs"
version:    1
attributes: map<text fully-qualified-name, byte-string opaque-value>
checksum:   SHA-256(canonical CBOR of {format, version, attributes})
```

The storage version is independent of semantic plugin versions in attribute
names. Canonical encoding makes unchanged logical maps byte-stable. The
checksum detects accidental valid-looking corruption; it is not authentication
and cannot recover deleted data. Compute the checksum over the exact canonical
CBOR encoding of the checksum-free map `{format, version, attributes}`, then
store the resulting 32-byte digest in the final envelope. Field names, map-key
ordering, and integer representations are part of the format and need canonical
test vectors.

Missing `self` means no attributes. A present empty map is valid. Once created,
`self`, `xattrs`, and `.lore` are retained even when the map becomes empty.
`skills disable` and `RemoveXattr` never prune this structure. This avoids
brittle ownership and cleanup inference. Deleting the owning folder naturally
deletes its `.lore` subtree.

### Atomic writes

Under the backing store's mutation lock:

1. Read and validate the current envelope, or start with an empty map if `self`
   is absent.
2. Apply flag and namespace semantics in memory.
3. Validate name, value, list, and envelope limits.
4. Canonically encode and checksum the complete new map.
5. Write a temporary file in `.lore/xattrs/`.
6. `fsync` the file, rename it over `self`, then `fsync` the directory. When
   `.lore` or `xattrs` is newly created, also sync each parent directory needed
   to make the new entries durable.

Failures before rename preserve the previous envelope. After rename, an
operation may have committed even if a later directory sync reports an error;
readers must observe either the complete old envelope or the complete new
envelope, never a partial envelope.

Do not implement sidecar access as path-string checks followed by ordinary file
operations. Resolve every component descriptor-relative, confined beneath the
backing root, and without following symlinks. On Linux, use `openat2` with
`RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS` where available or an equivalent
component-by-component `openat(O_NOFOLLOW)` design, with creation and rename
relative to the verified directory descriptor. Other platforms and backends
must provide an equivalent race-safe invariant or return `ENOTSUP`. A
check-then-use path implementation is not conforming because host tools do not
participate in OpenLore's mutation lock.

### Conflicts and explicit recreation

Sibling files under `.lore/xattrs/` are ignored, including files created by
Git or Syncthing conflict handling. Only a malformed, duplicate-key,
unsupported, or checksum-failed canonical `self` is a conflict.

Normal mutations fail closed on conflicted canonical metadata. Skills offers an
explicit recovery path:

```bash
skills enable --recreate-xattrs <folder>
skills disable --recreate-xattrs <folder>
```

The queued recovery changeset:

1. Copies the exact unreadable bytes, create-only, to
   `self.conflicted.<sha256>`.
2. `fsync`s the preserved copy.
3. Atomically replaces `self` with a fresh envelope containing only the Skills
   marker (`enable`) or an empty map (`disable`).
4. Reports that unrelated attributes could not be recovered automatically.

The original bytes remain available for forensic/manual recovery. Recreation
never happens without the explicit flag. Preservation is retryable: if the
deterministic backup already exists, verify that its exact bytes and SHA-256
match the current conflicted `self` and continue. Abort if the existing backup
differs or cannot be verified. This allows repair to resume after interruption
between preserving the forensic copy and replacing `self`.

## Write queue and policy integration

Add immutable `set_xattr` and `remove_xattr` actions to `vfs.ChangeSet`, carrying
the canonical target, fully qualified name, exact opaque bytes, and flags. Add
internal payloads for preserve-and-recreate and atomic `migrate_xattrs`.

`migrate_xattrs` carries an expected source marker or envelope digest plus an
ordered batch of plugin-owned set/remove edits. It is one queued read-modify-
write commit, not independently visible xattr actions. A precondition mismatch
causes rediscovery and a bounded retry. The batch may mutate only the migration
provider's namespace on one physical directory. Readers therefore never
observe old and new semantic markers as separately committed migration steps.

All session-facing xattr mutations pass through:

- canonical path resolution;
- write-scope authorization;
- write middleware and path-based approval rules;
- the serialized write log;
- pre-commit revalidation;
- atomic backing persistence;
- audit and post-commit middleware.

Content-specific validators such as OKF deliberately ignore xattr actions.
Skills middleware understands its own marker actions. General audit and
post-commit events include target, action, attribute name, value size, and value
hash—not raw opaque bytes. A held changeset necessarily retains the exact bytes
required to commit, so approval storage must apply its existing confidentiality
boundary.

Plugin schema migrations are the one approval exception: enabling/installing
the plugin version is operator authorization for its migration. They still use
the serialized queue, readonly lock, audit, and post-commit path, but bypass
per-path human approval.

### Wrapper behavior

Every server-session wrapper must forward or deliberately reject xattrs:

- `DirFS`: reference sidecar implementation.
- `MergeFS`: route to the physical root/mount; incapable backends return
  `ENOTSUP`.
- Overlay: read the visible directory's metadata and never mutate a lower
  read-only object accidentally.
- Alias: canonicalize so every logical view shares the physical folder's
  `.lore/xattrs/self`.
- Read scope: hide names and values whenever the directory is hidden. Xattr
  reads require full read authority for the physical
  directory, equivalent to scoped `within`; navigation-only ancestor visibility
  is insufficient.
- Write scope: require an explicit `CanManageSkills(path)`-style capability for
  Skills marker changes. It requires the effective named `rw` grant plus token,
  global, and docset locks; generic `CanWrite(action, path)` is insufficient.
- Restricted grant implementations such as publish/inbox access fail closed
  for new xattr mutation actions unless they explicitly opt in.
- Read middleware/tracking: preserve policy without treating xattr reads as
  content-hash baselines.
- Embedded/plain `fs.FS`: may expose prebuilt readable metadata if the adapter
  supports the private layout; otherwise return `ENOTSUP`. They never claim
  writable support.

Attributes belong to the physical folder. Aliases, nested logical views, and
multiple mounts of the same physical directory see one map. Authorization is
still evaluated through the caller's current logical view before access.

### Folder deletion

Direct `.lore` deletion is forbidden, but deleting its owning folder removes the
internal subtree. `RemoveAll` snapshots include a hash of xattr state without
exposing `.lore` paths or opaque bytes in user-facing manifests. If xattrs
change while deletion is queued or awaiting approval, deletion fails stale and
must be retried.

## Skills plugin

### Configuration and old interface

Add typed `plugins.skills.enabled` configuration to `openlore.yml`; default is
false. The plugin is registered only when enabled. The removed per-docset
`agent_skills` key in `lore.json` is ignored without warnings or migration.

The current bare `skills` command remains available independently because it
lists installed instruction commands. When the plugin is disabled,
`status|enable|disable|validate` return a machine-readable `ENOTSUP` result.

### Dynamic collection discovery

There is no collection index. `.lore/xattrs/self` is the source of truth:

- Status reads the target and its ancestors dynamically. Ancestor probing stops
  at the target's most-specific governing docset root and requires full read
  authority on each inspected directory; an outer marker is never inherited
  across that boundary.
- Write validation walks the target's ancestors, bounded by path depth.
- Collection validation walks readable directories dynamically for markers.
- Metadata discovery first finds readable marked scopes, then scans only those
  scopes for skills.
- Host/Git/Syncthing changes are visible on the next operation without polling
  or restart.

Plugin loading performs a scan only for schema migration; it does not retain a
collection cache.

### Where collections may live

- A docset root may be marked.
- Any descendant folder within a docset may be marked.
- Global virtual `/` may never be marked.
- Crossing into a nested docset stops the parent collection. The nested docset
  needs its own marker because it is a separate authorization boundary.
- Symlink targets are rejected and recursive traversal never follows symlinks.

Semantic eligibility is independent of any one identity's writeability. A
read-only or embedded directory can carry a prebuilt marker. The management
command simply requires the calling identity to have `rw` for a change.

### Recursive skill model

A marked directory defines a recursive collection. Any descendant directory
containing an exactly named `SKILL.md` is a skill. Intermediate organizational
directories need neither a marker nor `SKILL.md`.

Consequences:

- Missing `SKILL.md` cannot be inferred as an error; an unmarked directory may
  be intentional organization.
- Creating or updating `SKILL.md` inside an enabled scope must validate before
  commit.
- Deleting `SKILL.md` is allowed and removes that directory from discovery.
- Deleting a skill directory is allowed.
- Other files and organizational directories are unconstrained.
- Moving a `SKILL.md` to another directory validates its `name` against the new
  containing directory.
- Existing `metadata.agent_skill: disable` frontmatter remains supported and
  excludes that valid, parseable file from discovery.

Nested markers are allowed even when an ancestor already enables the subtree.
They are useful future configuration boundaries and are not automatically
deduplicated or rejected. The nearest marker owns each skill for reporting, but
recursive parent enable/validate operations still include nested collections.
Each physical `SKILL.md` is validated once per command and errors propagate into
the parent result.

There are no negative/exclusion markers in v1. Removing a nested direct marker
may leave the folder effectively enabled through its ancestor; the command must
say so explicitly. Removing an ancestor marker preserves all nested markers and
reports the collections that remain enabled.

### Validation invariants

Use one validator for `skills enable`, `skills validate`, write admission, and
pre-commit validation. It collects all findings rather than stopping at the
first error. Stable rule IDs and line/column locations are provided where
known.

`skills enable <folder>`:

- Rejects global `/`, out-of-docset paths, symlinks, non-directories, and
  callers without `rw`.
- Recursively validates every `SKILL.md` below the target, including nested
  directly marked collections, but not nested docsets.
- Attributes each skill to its nearest marker and emits each finding once.
- Repeats validation immediately before queued commit.
- Sets the zero-length marker only when the entire recursive scope is valid.
- Has no `--force`; `--recreate-xattrs` repairs storage, not validation errors.
- Is idempotent when the direct marker already exists.

`skills disable <folder>`:

- Removes only the direct Skills marker.
- Never modifies content, other attributes, or `.lore` structure.
- Is idempotent when no direct marker exists.
- Reports inherited effective enablement and surviving nested direct markers.

`skills validate [scope]`:

- Requires read access, never writes, and may begin at global `/`.
- Recognizes the nearest inherited marker above the requested scope.
- Recursively discovers marked collections below the scope that the identity
  can read.
- Emits nothing for ordinary unmarked directories.
- Validates each physical skill once and reports nested collection summaries.
- Emits one `agent-skills/no-collections` warning only when neither an inherited
  nor descendant readable collection exists.
- Returns success for `no_collections`; validation errors return nonzero.

### NDJSON schema

Finding:

```json
{"type":"finding","collection":"/skills/team","path":"deploy/SKILL.md","line":1,"column":1,"severity":"error","rule":"agent-skills/name","message":"frontmatter name does not match directory"}
```

Collection summary:

```json
{"type":"collection","path":"/skills/team","status":"invalid","errors":1}
```

Final result:

```json
{"type":"result","path":"/skills","operation":"enable","status":"rejected","collections":2,"errors":1,"warnings":0}
```

Allowed result statuses include:

- `enabled`, `already_enabled`, `disabled`, `already_disabled`;
- `marker_removed` with `effective_status: "enabled"` and inherited `source`;
- `valid`, `invalid`, `no_collections`;
- `pending` with approval `ref`;
- `degraded`, `conflict`, `unsupported`, and `rejected`.

No validation prose is mixed into stdout.
Warnings without errors return success. Validation errors, rejected mutations,
conflicts, degradation, and unsupported operations return nonzero. Every
failure still emits a final machine-readable result so an agent can determine
what to fix.

### `lore meta` and docset reporting

Keep the current filter aliases (`agent_skills`, `agent_skill`, `skills`, and
`skill`) for query compatibility unless command namespace collision requires a
separate parser fix. Derive roots dynamically from readable xattrs, return
canonical absolute paths, and never expose inaccessible collection names.

`lore docsets` derives any `agent-skills` display attribute from physical
markers rather than `lore.json`.

## Plugin semantic migrations

Plugins that introduce a new semantic xattr version provide a migration method.
For example, Skills v2 knows how to transform:

```text
user.lore.plugins.openlore.skills.v1
```

to its v2 key/value contract. Core xattrs remain versionless and opaque.

### Migration execution

- On plugin load, scan mounted directory xattrs for supported older marker
  versions.
- Compute migration in plugin code.
- Submit it as a server-owned `plugin-migration` changeset through the write
  queue using the atomic `migrate_xattrs` action and a source precondition.
- Bypass per-path human approval, but retain serialization, readonly handling,
  audit, and post-commit behavior.
- Do not require a user identity's `rw`; enabling the installed plugin version
  authorizes its schema migration.
- If a supported old marker appears later through Git/Syncthing, a dynamic read
  enqueues the migration lazily and deduplicates concurrent attempts.
- Never automatically rewrite unknown newer versions or conflicted storage.

### Degraded collections

A migration failure degrades only the affected collection, never the whole
server:

- Ordinary content remains readable/writable under normal docset policy.
- The collection is excluded from Skills discovery and Skills-specific write
  validation.
- Skills status/validation reports the reason.
- Skills management rejects changes until migration succeeds or xattrs are
  explicitly recreated.
- Automatic migration retries on the next plugin load or encounter.

An old plugin encountering an unknown newer Skills marker also degrades that
collection rather than silently treating it as disabled. A plugin version may
temporarily understand older schemas solely to compute migration; mixed
versions are not treated as simultaneously active configuration.

## Implementation plan

### Phase 1: VFS contract and sidecar

1. Add optional reader/writer interfaces, flags, limits, normalized errors, and
   conformance tests under `pkg/vfs`.
2. Add xattr, atomic migration, and repair actions to `ChangeSet` and
   `CommitChangeSet`.
3. Implement canonical CBOR envelope encoding/checksum validation in a focused
   internal storage component used by `DirFS`.
4. Implement directory-only `DirFS` reads and queued mutations at
   `.lore/xattrs/self`, including race-safe descriptor-relative root confinement
   and the 8 MiB cap.
5. Reserve and hide `.lore` throughout the public VFS without hiding ordinary
   parent content.
6. Integrate xattr hashes into tree snapshots and owning-folder deletion.

### Phase 2: Composition and policy

1. Route capabilities through `MergeFS`, overlays, aliases, scoped reads,
   scoped writes, read chains, read tracking, and middleware.
2. Add write-log admission/replay, approval, redacted post-commit, and readonly
   drain tests.
3. Verify physical aliases share metadata while logical authorization still
   gates each access.
4. Verify unsupported backends return `ENOTSUP`, not empty attributes.

### Phase 3: Skills interface

1. Add `plugins.skills.enabled` to `openlore.yml`; delete docset
   `agent_skills` interpretation without warning.
2. Extend the existing `skills` command with status/enable/disable/validate and
   default NDJSON output.
3. Refactor Agent Skills validation from immediate-child assumptions to the
   recursive, nested-docset-aware model.
4. Make collection status/discovery dynamic from xattrs; remove boot-time root
   snapshots and `anyDocsetHasAgentSkills`.
5. Apply the same validator at command preflight, write admission, and
   serialized pre-commit.
6. Update `lore meta` filters and docset reporting to derive Skills state from
   physical markers.
7. Add explicit preserve-and-recreate conflict recovery.

### Phase 4: Plugin migrations

1. Add the plugin xattr migration provider contract.
2. Add startup scanning and lazy runtime migration with per-folder in-flight
   deduplication, but no collection index.
3. Add server-owned queue submissions that bypass approval and remain audited.
4. Add degraded-collection state derived dynamically from migration failures.

### Phase 5: Documentation and rollout

1. Update `openlore.yml.example`, plugin docs, command reference, embedded
   Skills docs, and configuration examples.
2. Document `.lore` as reserved, hidden through OpenLore, and portable through
   host tools.
3. Document backups and the inability to distinguish deleted metadata from
   never-created metadata after restart.
4. Document Linux differences and operation-based backend capability checks.

## Verification matrix

### Xattr contract

- Names of 254, 255, and 256 bytes; multibyte names counted by bytes.
- Values of 0, 1, 65,535, 65,536, and 65,537 bytes.
- NUL and invalid UTF-8 values round-trip exactly.
- Invalid UTF-8 and NUL in names return `EINVAL`.
- Missing versus present-empty remains distinguishable.
- Default/create/replace/invalid flag combinations.
- Name lists at 65,535, 65,536, and 65,537 encoded bytes.
- Aggregate envelopes immediately below, at, and above 8 MiB.
- Every required errno matches with `errors.Is`.
- Regular files and symlinks return `ENOTSUP`; traversal never follows links.

### Storage and durability

- Canonical CBOR is byte-stable regardless of Go map iteration order.
- Canonical envelope and checksum test vectors are byte-exact across independent
  encoders.
- Wrong format/version, duplicate keys, malformed CBOR, and checksum mismatch
  fail closed.
- Missing `self` yields empty list/`ENODATA`, while an empty envelope persists.
- Temp write, file sync, rename, and directory sync fault injection leaves old
  or new valid state.
- Capacity failures before rename leave the old file intact and return
  `ENOSPC`; post-rename sync failures still leave an old-or-new valid envelope.
- Concurrent set/remove operations are serializable and lose no unrelated
  attributes.
- Other `.lore/xattrs` siblings are ignored.
- Symlink-swap and rename-race tests cannot escape the backing root or redirect
  sidecar reads/writes.
- Explicit recreation preserves exact conflicted bytes and never runs without
  its flag.
- Recreation resumes when its matching deterministic forensic backup already
  exists and rejects a mismatched backup.
- Empty maps never prune `.lore`, `xattrs`, or `self`.

### Hiding, composition, and deletion

- `.lore` never appears through shell, SFTP, MCP, web, walk, search, metadata,
  snapshots, or aliases.
- Direct `.lore` access is denied; owning-folder deletion succeeds and removes
  it.
- Xattr drift makes held folder deletion stale without exposing opaque bytes.
- Mounts, overlays, aliases, nested docsets, read scope, and write scope preserve
  expected behavior.
- Denied reads do not leak attribute names or collection paths.
- Navigation-only access does not expose ancestor xattrs, and inherited-marker
  probing never crosses the most-specific governing docset root.
- Xattr writes are queued, approval-aware, replayable, and post-commit once.
- Restricted non-`rw` grants fail closed for xattr mutation actions.

### Skills commands and validation

- Plugin disabled/enabled and non-destructive re-enable behavior.
- Bare `skills` remains backward-compatible.
- Every management command emits valid NDJSON only.
- `rw` may enable/disable; read-only identities may status/validate but cannot
  mutate.
- Global `/` is rejected; docset roots and descendants are accepted.
- Recursive organizational folders, nested markers, and nested docset
  boundaries.
- Symlinked directories are rejected/skipped.
- All invalid `SKILL.md` findings are collected with stable IDs.
- Enable validates recursively through nested markers and repeats pre-commit.
- Each skill is validated once and attributed to its nearest marker.
- Delete `SKILL.md` demotes the skill; create/update remains strict.
- Disable preserves nested markers and reports inherited effective status.
- Validate recognizes inherited scopes and readable descendant collections.
- No-collection warning is emitted only when none are found and exits zero.
- Alias output is canonical and inaccessible folders never appear.

### Plugin migrations

- Startup migration, runtime lazy migration, and in-flight deduplication.
- Old-to-new markers change in one conditional atomic envelope commit; stale
  source preconditions rediscover and retry without losing unrelated xattrs.
- Migrations use queue/audit, bypass approval, and respect physical readonly.
- Failure degrades one collection while ordinary content and other collections
  remain available.
- Unknown newer versions degrade rather than silently disable.
- Read-only, corrupt, unsupported, and transient failure reasons are stable and
  machine-readable.

## References

- Linux xattr overview and limits: https://man7.org/linux/man-pages/man7/xattr.7.html
- Linux set semantics and errors: https://man7.org/linux/man-pages/man2/setxattr.2.html
- Linux list encoding and limits: https://man7.org/linux/man-pages/man2/listxattr.2.html
- ext4 xattr layout: https://docs.kernel.org/filesystems/ext4/attributes.html
- Syncthing folder marker rationale: https://forum.syncthing.net/t/why-syncthing-need-a-stfolder-folder/15043
- Syncthing metadata consolidation discussion: https://forum.syncthing.net/t/is-it-possible-to-delete-stfolder-and-stignore-file/17332
- Syncthing version-store behavior: https://docs.syncthing.net/users/versioning.html
