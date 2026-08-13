#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
runtime="$repo_root/.experiment/dashboard"
venv="$runtime/venv"
port=${OPENLORE_DASHBOARD_PORT:-18765}

mkdir -p "$runtime" "$repo_root/.experiment/persistence"
if [[ ! -x "$venv/bin/uvicorn" ]]; then
  python3 -m venv "$venv"
  "$venv/bin/pip" install --quiet --requirement "$repo_root/experiment/dashboard/requirements.txt"
fi
if [[ -f "$runtime/server.pid" ]] && kill -0 "$(cat "$runtime/server.pid")" 2>/dev/null; then
  echo "Dashboard already running at http://127.0.0.1:$port"
  exit 0
fi
(
  cd "$repo_root/experiment/dashboard"
  exec env \
    OPENLORE_EXPERIMENT_REPO="$repo_root" \
    OPENLORE_COLLAB_WORKSPACE="$repo_root/.experiment/collab/workspace" \
    OPENLORE_PERSISTENCE_DIR="$repo_root/.experiment/persistence" \
    "$venv/bin/uvicorn" app:app --host 127.0.0.1 --port "$port"
) >"$runtime/server.log" 2>&1 &
echo $! >"$runtime/server.pid"

for _ in $(seq 1 60); do
  if curl --fail --silent "http://127.0.0.1:$port/api/health" >/dev/null; then
    echo "Dashboard and verifier ready at http://127.0.0.1:$port"
    exit 0
  fi
  sleep 0.25
done
echo "Dashboard failed to start; see $runtime/server.log" >&2
exit 1
