---
name: openlore
description: Query and publish to an OpenLore knowledge base over SSH using ordinary shell commands. Use when a task needs project documentation, runbooks, shared team knowledge, or a place to publish findings.
metadata:
  shelllm:
    requires:
      bins: ["ssh"]
---

# OpenLore

OpenLore serves a documentation and knowledge filesystem over SSH. There is no
API to learn: run the same shell commands you already use, inside a one-shot
SSH session.

## Target server

Set `OPENLORE_SSH` to the SSH arguments for your OpenLore server, for example:

```bash
export OPENLORE_SSH="-p 2222 localhost"
```

Then run commands non-interactively:

```bash
ssh $OPENLORE_SSH "tree -L 2 /"
```

Each `ssh` invocation is an independent session; state such as `cd` or shell
variables does not persist between invocations, so use absolute paths.

## Reading and searching

```bash
# Discover what is available
ssh $OPENLORE_SSH "tree -L 2 /"

# Search across all docs
ssh $OPENLORE_SSH "grep -rn 'search term' /docs"

# Read a specific file
ssh $OPENLORE_SSH "cat /docs/README.md"

# Find files by name
ssh $OPENLORE_SSH "find / -name '*.md'"

# Query structured frontmatter as NDJSON
ssh $OPENLORE_SSH "lore meta /docs | jq -r '.path'"
```

Pipes, `jq`, `sed`, `awk`, `sort`, `xargs`, and most other coreutils work
inside the remote command. Run `ssh $OPENLORE_SSH "help"` for the full list.

## Publishing findings

If your identity has publish or write access, store results back into the
knowledge base:

```bash
# List docsets you can publish to
ssh $OPENLORE_SSH "publish"

# Publish a report from stdin
cat report.md | ssh $OPENLORE_SSH "publish <docset> reports/report.md"
```

Writes are atomic and conflict-aware; a rejected write returns a non-zero exit
status with an explanation on stderr.

## Sharing run trajectories

If the server has a writable `trajectories` docset, publish completed shellm
trajectory directories so teammates and other agents can read them. Sync the
whole directory — `trajectory.jsonl`, `blobs/`, and nested child-run
directories — uploading blobs before the JSONL that references them:

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
sync_traj ~/.headlong/trajectories/<run-id>-<slug>
```

Run this on the host after a run completes (the outer shellm process writes
trajectories on the host, even when generated code runs in Docker). For a
still-growing `trajectory.jsonl`, append new lines with `tee -a`, which is
always a conflict-safe atomic append. Never point `SHELLM_TRAJ_DIR` at a
network mount of OpenLore; sync copies instead.

Read shared trajectories from any session:

```bash
ssh $OPENLORE_SSH "cat /trajectories/<run>/trajectory.jsonl | jq -r 'select(.type == \"final\") | .content'"
```

## Notes

- The remote shell is OpenLore's sandboxed in-memory interpreter, not a real
  operating system: there is no process execution, network access, or shell
  escape on the server side.
- Docker sandbox networking: shellm's container uses Docker's default bridge
  network, so `localhost` refers to the container, not the host. A remote
  OpenLore server (e.g. `docs.internal`) is reachable as-is; for a server on
  the Docker host use `host.docker.internal` where available, or run
  `shellm --env local`.
- The default sandbox image has no `ssh` client (`apt-get install -y
  openssh-client` first) and no SSH keys; container `HOME` is the workdir.
  Keyless servers work immediately. For a keyed identity, mount the key
  directory with `--var KEY_DIR=/path/to/keys` (directory-valued vars are
  mounted) and pass `ssh -i "$KEY_DIR/id_ed25519"`.
- Forward the target through the sandbox with
  `shellm --var OPENLORE_SSH ...` if it is set on the host.

## Installing this skill

From a machine with access to an OpenLore server:

```bash
mkdir -p .skills/openlore .skills/openlore-housekeeping
ssh $OPENLORE_SSH agents-shellm > .skills/openlore/SKILL.md
ssh $OPENLORE_SSH agents-shellm-housekeeping > .skills/openlore-housekeeping/SKILL.md
```

The companion `openlore-housekeeping` skill audits the knowledge base and
publishes maintenance reports.
