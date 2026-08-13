#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
agent=${1:?usage: submit-candidate.sh AGENT CANDIDATE_COMMIT [BASE_COMMIT]}
candidate=${2:?usage: submit-candidate.sh AGENT CANDIDATE_COMMIT [BASE_COMMIT]}
base=${3:-$(git -C "$repo_root" rev-parse HEAD)}
port=${OPENLORE_DASHBOARD_PORT:-18765}

curl --fail --silent --show-error \
  -H 'Content-Type: application/json' \
  -d "$(python3 -c 'import json,sys; print(json.dumps({"agent":sys.argv[1],"candidate_commit":sys.argv[2],"base_commit":sys.argv[3],"run_now":True}))' "$agent" "$candidate" "$base")" \
  "http://127.0.0.1:$port/api/submissions"
echo
