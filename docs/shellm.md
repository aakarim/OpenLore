# headlong / shellm Integration

[shellm](https://github.com/laude-institute/headlong) is headlong's recursive
language model engine: an agent that thinks by writing bash and running it.
Its only tool protocol is the shell, which makes OpenLore's SSH interface
directly usable from generated code — no adapter needed.

## Teaching shellm about a server

Every OpenLore server ships instruction commands per agent type (see the
`teach` skill's "Onboarding Agents" index). For headlong there are two, and
each prints a ready headlong-format `SKILL.md`:

```bash
mkdir -p .skills/openlore .skills/openlore-housekeeping
ssh <server> agents-shellm > .skills/openlore/SKILL.md
ssh <server> agents-shellm-housekeeping > .skills/openlore-housekeeping/SKILL.md
skills show openlore   # verify
```

`openlore` covers querying and publishing. `openlore-housekeeping` is a
maintenance skill: it audits the knowledge base for stale docs, broken links,
stuck inbox items, unsynced trajectories, and missing skill files, then
publishes a report for human review.

Point the skill at your server with an environment variable:

```bash
export OPENLORE_SSH="-p 2222 docs.internal"
```

## Docker sandboxing caveats

shellm runs generated code in a Docker container by default (image
`ubuntu:latest`, default bridge network, no extra mounts). Three consequences
for OpenLore access:

- **`localhost` is the container, not the host.** A remote OpenLore server is
  reachable as-is from the bridge network. For a server running on the Docker
  host, use `host.docker.internal` (Docker Desktop provides it; on native
  Linux the container must be created with `--add-host`), or run
  `shellm --env local` to skip the sandbox.
- **No SSH client.** The default image installs only `jq curl python3 tmux
  sudo`; generated code must `apt-get install -y openssh-client` first, or
  the sandbox image must include it.
- **No SSH keys.** The container's `HOME` is the workdir, so `~/.ssh` is
  empty. Keyless servers (`allow_keyless: true`) work immediately. For a
  keyed identity, pass a directory-valued variable
  (`shellm --var KEY_DIR=/path/to/keys`), which shellm bind-mounts at the same
  path, and use `ssh -i "$KEY_DIR/id_ed25519"`.

`shellm --var OPENLORE_SSH ...` forwards the host's target setting into the
sandbox without putting it on a command line.

## Sharing trajectories through OpenLore

Every shellm run records an append-only trajectory: a directory holding
`trajectory.jsonl`, oversized output blobs under `blobs/`, and nested
directories for recursive child runs. The outer shellm process writes these on
the **host** (default `~/.headlong/trajectories/`), even when generated code
runs in Docker. Publishing them into OpenLore gives teammates and other agents
a governed, greppable view of what an agent did.

### Server configuration

Give trajectories their own docset backed by the writable directory, writable
only by the syncing identity:

```json
{
  "roles": { "syncer": { "comment": "trajectory sync" } },
  "docsets": {
    "trajectories": {
      "paths": [{ "published/trajectories": "/trajectories" }],
      "access": { "allow": { "guest": "ro", "syncer": "rw" } }
    }
  },
  "identities": [
    { "name": "traj-agent", "public_key": "ssh-ed25519 ...", "roles": ["syncer"] }
  ]
}
```

Start the server with the writable substrate enabled (`--readonly=false` or
`readonly: false`). The `published/trajectories` directory must exist on disk.

Trajectory files need matching `files.allowed` patterns. `*.jsonl` is allowed
by default; to also share spilled output blobs, extend the list in
`openlore.yml` (setting `files.allowed` replaces the default list):

```yaml
files:
  allowed:
    - "*.md"
    - "*.json"
    - "*.jsonl"
    - "*.stdout"
    - "*.stderr"
```

### Syncing

Sync whole trajectory directories after a run completes, uploading `blobs/`
and child directories before the `trajectory.jsonl` that references them (the
`SKILL.md` served by the `shellm` command carries the same snippet):

```bash
sync_traj() {
  local dir="$1" dest="/trajectories/$(basename "$1")"
  (cd "$dir" && find . -type f | sort | while read -r f; do
    local rel="${f#./}" sub
    sub=$(dirname "$rel")
    [ "$sub" != "." ] && ssh -n $OPENLORE_SSH "mkdir -p $dest/$sub"
    cat "$f" | ssh $OPENLORE_SSH "tee $dest/$rel >/dev/null"
  done)
}
```

Re-running the sync overwrites files atomically; incremental syncs of a
growing `trajectory.jsonl` can instead append only new lines with `tee -a`,
which is always a conflict-safe CAS append. Do **not** point
`SHELLM_TRAJ_DIR` at an SSHFS mount of OpenLore: shellm's trajectory writer
relies on `mkdir`-based locking and atomic renames that are not guaranteed
over SFTP/FUSE (and OpenLore's SFTP interface is read-only).

### Reading

Trajectories are ordinary docset files, so any authorized session can inspect
them:

```bash
# What runs are shared?
ssh $OPENLORE_SSH "ls /trajectories"

# Final answers across all runs
ssh $OPENLORE_SSH "grep -l '\"type\":\"final\"' -r /trajectories"

# One run's step types and final result
ssh $OPENLORE_SSH "cat /trajectories/<run>/trajectory.jsonl | jq -r .type"
ssh $OPENLORE_SSH "cat /trajectories/<run>/trajectory.jsonl | jq -r 'select(.type == \"final\") | .content'"
```

## What OpenLore adds over headlong's plain filesystem

headlong's [agent-native docs
proposal](https://github.com/laude-institute/headlong/blob/main/design/agent-native-docs.md)
keeps `AGENTS.md`, `skills/`, and docs as plain files in one repo. OpenLore
serves the same conventions through the same shell interface, and adds the
properties plain files cannot provide:

- **Multiplayer.** One server holds the shared knowledge; every identity gets
  its own view. Roles grant `ro`, `publish`, or `rw` per docset, and each
  agent can have a private `/home`. Agents on different machines share one
  memory without sharing a git checkout.
- **Governable.** Writes are atomic and conflict-aware. `publish` routes new
  content into an inbox that a human accepts or rejects, so agent-written
  docs and housekeeping reports never land unreviewed. The Agent Skills
  plugin validates `SKILL.md` files on the write path, so a shared skills
  collection cannot drift into an invalid state — `skills install` from
  GitHub has no equivalent check.
- **Inspectable.** `lore meta` exposes frontmatter as NDJSON for `jq`.
  Synced trajectories make agent runs greppable by anyone with read access.
  Humans browse the same content in the web view; Prometheus metrics cover
  usage.

The docs proposal's `docs/human/` vs `docs/agent/` split maps directly onto
docsets, each with its own access rules, without moving any files.
