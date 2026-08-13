#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
runtime="$repo_root/.experiment/collab"
dashboard_runtime="$repo_root/.experiment/dashboard"

if [[ -f "$runtime/openlore.pid" ]]; then
  pid=$(cat "$runtime/openlore.pid")
  if kill -0 "$pid" 2>/dev/null; then
    kill "$pid"
    echo "Stopped OpenLore collaboration backend (PID $pid)."
  fi
  rm -f "$runtime/openlore.pid"
else
  echo "OpenLore collaboration backend is not running."
fi

if [[ -f "$dashboard_runtime/server.pid" ]]; then
  dashboard_pid=$(cat "$dashboard_runtime/server.pid")
  if kill -0 "$dashboard_pid" 2>/dev/null; then
    kill "$dashboard_pid"
    echo "Stopped dashboard/verifier service (PID $dashboard_pid)."
  fi
  rm -f "$dashboard_runtime/server.pid"
fi
