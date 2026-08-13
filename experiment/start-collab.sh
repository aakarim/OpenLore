#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
runtime="$repo_root/.experiment/collab"
workspace="$runtime/workspace"
keys="$runtime/keys"
ssh_port=${OPENLORE_TEAM_SSH_PORT:-24222}
http_port=${OPENLORE_TEAM_HTTP_PORT:-18088}
agents=(coordinator benchmark profiler traversal scanner regex integrator)

mkdir -p "$runtime" "$workspace" "$keys"

for agent in "${agents[@]}"; do
  if [[ ! -f "$keys/$agent" ]]; then
    ssh-keygen -q -t ed25519 -N '' -C "openlore-rg-$agent" -f "$keys/$agent"
  fi
done

channels=(coordination benchmarking profiling traversal matching verification)
for channel in "${channels[@]}"; do
  mkdir -p "$workspace/channels/$channel/posts"
  if [[ ! -f "$workspace/channels/$channel/README.md" ]]; then
    cat >"$workspace/channels/$channel/README.md" <<EOF
# $channel channel

Post concise findings under \`posts/\`. Create a focused discussion under
\`/threads/<agent>/<slug>/\` and link it from the post. Do not duplicate the full board.
EOF
  fi
done

for agent in "${agents[@]}"; do
  mkdir -p "$workspace/inboxes/$agent"
  mkdir -p "$workspace/threads/$agent" "$workspace/submissions/$agent"
  for channel in "${channels[@]}"; do
    mkdir -p "$workspace/channels/$channel/posts/$agent"
  done
done
mkdir -p "$workspace/instructions/roles" "$workspace/trusted-results" "$workspace/channels/coordination/posts/human"

cat >"$workspace/README.md" <<'EOF'
# Recursive `rg` optimization collaboration

This root is read-only. The frozen contract and role instructions are under
`/instructions/`. Trusted leaderboard entries under `/trusted-results/` are
generated only from verifier-owned structured records.

## Protocol

1. Post only under `/channels/<topic>/posts/<your-agent>/`.
2. Put discussions under `/threads/<your-agent>/<slug>/`; mention another
   agent to fan a source-linked notification into their private inbox.
3. Work only in your assigned git worktree and branch; never push.
4. Post commit hashes, exact benchmark commands, all samples, and failures.
5. Publish negative results; they prevent repeated dead ends.
6. Submit candidates to the verifier API; no agent can write trusted results.

See `/instructions/contract.md` for the frozen evaluation contract.
EOF

cat >"$workspace/instructions/contract.md" <<'EOF'
# Frozen evaluation contract

- Preserve `rg [-inl] PATTERN [PATH ...]`, deterministic path/line ordering,
  CRLF handling, long lines, atomic errors, and exit codes 0/1/2.
- Run `go test ./...` and `go test -race ./pkg/shell/cmds -run Rg`.
- Benchmark both MDN and the 64-level generated tree with GOMAXPROCS=1.
- Use all ten samples; the verifier compares per-case medians and the geometric
  mean across cases while retaining every raw sample.
- No artificial delays, hardcoding, corpus recognition, skipped correctness,
  hidden host-process execution, or benchmark edits that remove work.
- Overall speedup must be at least 5%; no primary case may regress over 5%.
EOF

cat >"$workspace/instructions/README.md" <<'EOF'
# Agent instructions (read-only)

The contract is immutable during a run. Role files constrain ownership but do
not grant trust: only the external verifier writes structured results.
EOF

for agent in "${agents[@]}"; do
  cat >"$workspace/instructions/roles/$agent.md" <<EOF
# $agent role

Your identity is \`$agent\`. Write messages only in your role-specific channel
post directories and discussions only under \`/threads/$agent/\`.
EOF
done

KEYS="$keys" RUNTIME="$runtime" python3 <<'PY'
import json
import os
from pathlib import Path

keys = Path(os.environ["KEYS"])
agents = ["coordinator", "benchmark", "profiler", "traversal", "scanner", "regex", "integrator"]
channels = ["coordination", "benchmarking", "profiling", "traversal", "matching", "verification"]
policy = {
    "allow_keyless": False,
    "unknown_identity": "deny",
    "default_cwd": "/",
    "roles": {"team": {}, **{agent: {} for agent in agents}},
    "docsets": {
        "collaboration-read": {"paths": ["/"], "access": {"allow": {"team": "ro"}}},
        "instructions": {"paths": ["/instructions"], "readonly": True, "access": {"allow": {"team": "ro"}}},
        "trusted-results": {"paths": ["/trusted-results"], "readonly": True, "access": {"allow": {"team": "ro"}}},
        **{
            f"inbox-{agent}": {
                "paths": [f"/inboxes/{agent}"],
                "access": {"allow": {agent: "ro"}},
            }
            for agent in agents
        },
        **{
            f"channel-{channel}-{agent}": {
                "paths": [f"/channels/{channel}/posts/{agent}"],
                "access": {"allow": {agent: "rw"}},
            }
            for channel in channels for agent in agents
        },
        **{
            f"threads-{agent}": {
                "paths": [f"/threads/{agent}"],
                "access": {"allow": {agent: "rw", "team": "ro"}},
            }
            for agent in agents
        },
        **{
            f"submissions-{agent}": {
                "paths": [f"/submissions/{agent}"],
                "access": {"allow": {agent: "rw"}},
            }
            for agent in agents
        },
    },
    "identities": [
        {
            "name": agent,
            # OpenLore compares the canonical authorized-key form, which does
            # not include ssh-keygen's trailing comment.
            "public_key": " ".join((keys / f"{agent}.pub").read_text().split()[:2]),
            "roles": ["team", agent],
        }
        for agent in agents
    ],
}
(Path(os.environ["RUNTIME"]) / "lore.json").write_text(json.dumps(policy, indent=2) + "\n")
PY

go build -o "$runtime/openlore" ./cmd/openlore

if [[ -f "$runtime/openlore.pid" ]] && kill -0 "$(cat "$runtime/openlore.pid")" 2>/dev/null; then
  echo "OpenLore collaboration backend already running (PID $(cat "$runtime/openlore.pid"))."
else
  # The stock binary embeds its deployment config, so use CLI overrides rather
  # than loading a second config file (the two sources intentionally conflict).
  # Run from the ignored runtime directory so embedded relative data paths do
  # not create control-plane files in the source checkout.
  (
    cd "$runtime"
    exec "$runtime/openlore" \
      --config "$runtime/no-external-config.yml" \
      --port "$ssh_port" \
      --http-port "$http_port" \
      --metrics-port 0 \
      --host-key "$runtime/host_ed25519" \
      --auth "$runtime/lore.json" \
      --allowed '*.md' \
      --readonly=false \
      "$workspace"
  ) >"$runtime/server.log" 2>&1 &
  echo $! >"$runtime/openlore.pid"
fi

for _ in $(seq 1 30); do
  if AGENT_ID=coordinator TEAM_RUN_DIR="$runtime" "$repo_root/experiment/lore.sh" 'cat /README.md' >/dev/null 2>&1; then
    echo "OpenLore collaboration backend ready on SSH port $ssh_port."
    echo "Workspace: $workspace (agents access it only through OpenLore)"
    exit 0
  fi
  sleep 0.25
done

echo "OpenLore failed to become ready; see $runtime/server.log" >&2
exit 1
