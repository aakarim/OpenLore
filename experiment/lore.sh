#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
runtime=${TEAM_RUN_DIR:-"$repo_root/.experiment/collab"}
agent=${AGENT_ID:-coordinator}
port=${OPENLORE_TEAM_SSH_PORT:-24222}
key="$runtime/keys/$agent"

[[ -f "$key" ]] || { echo "No OpenLore identity key for $agent; run start-collab.sh" >&2; exit 2; }
[[ $# -gt 0 ]] || { echo "usage: AGENT_ID=name $0 'openlore shell command'" >&2; exit 2; }

exec ssh -T \
  -i "$key" \
  -p "$port" \
  -o IdentitiesOnly=yes \
  -o StrictHostKeyChecking=no \
  -o UserKnownHostsFile="$runtime/known_hosts" \
  "$agent@127.0.0.1" "$1"
