#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
runtime="$repo_root/.experiment/collab"
base_url=${QWEN_BASE_URL:-http://aiw1:30001/v1}
api_key=${OPENAI_API_KEY-}
if [[ -z "$api_key" ]]; then
  api_key=local
fi
pi_version=0.84.1
tooling="$repo_root/.experiment/tooling"
run_id=$(date -u +%Y%m%dT%H%M%SZ)
run_root="${TMPDIR:-/tmp}/openlore-rg-team-$run_id"
board_lore="$repo_root/experiment/lore.sh"
agent_timeout=${OPENLORE_AGENT_TIMEOUT_SECONDS:-900}
agents=(coordinator benchmark profiler traversal scanner regex integrator)

[[ "$agent_timeout" =~ ^[1-9][0-9]*$ ]] || {
  echo "OPENLORE_AGENT_TIMEOUT_SECONDS must be a positive integer." >&2
  exit 2
}

[[ -f "$runtime/openlore.pid" ]] && kill -0 "$(cat "$runtime/openlore.pid")" 2>/dev/null || {
  echo "Start the collaboration backend first: ./experiment/start-collab.sh" >&2
  exit 2
}
curl --fail --silent "http://127.0.0.1:${OPENLORE_DASHBOARD_PORT:-18765}/api/health" >/dev/null || {
  echo "Start the dashboard/verifier first: ./experiment/start-dashboard.sh" >&2
  exit 2
}
[[ -z "$(git -C "$repo_root" status --porcelain)" ]] || {
  echo "The experiment requires a clean, committed baseline." >&2
  git -C "$repo_root" status --short >&2
  exit 2
}
[[ -d "$repo_root/.benchdata/mdn-content/files/en-us" && -d "$repo_root/.benchdata/deep-markdown" ]] || {
  echo "Prepare both benchmark corpora first: ./scripts/fetch-rg-corpus.sh" >&2
  exit 2
}

models_json=$(curl --fail --silent --show-error --connect-timeout 5 --max-time 30 \
  -H "Authorization: Bearer $api_key" "$base_url/models") || {
  echo "Qwen endpoint is unreachable at $base_url; no agents were launched." >&2
  exit 3
}
model=${QWEN_MODEL:-$(printf '%s' "$models_json" | python3 -c '
import json,sys
ids=[m["id"] for m in json.load(sys.stdin).get("data",[]) if "qwen" in m.get("id","").lower()]
print(ids[0] if ids else "")
')}
[[ -n "$model" ]] || { echo "No Qwen model found; set QWEN_MODEL explicitly." >&2; exit 3; }

mkdir -p "$tooling" "$run_root"/{worktrees,logs,sessions,pi-home}
if [[ ! -x "$tooling/node_modules/.bin/pi" ]]; then
  npm install --prefix "$tooling" --save-exact "@earendil-works/pi-coding-agent@$pi_version"
fi
pi="$tooling/node_modules/.bin/pi"

BASE_URL="$base_url" MODEL="$model" PI_HOME="$run_root/pi-home" python3 <<'PY'
import json
import os
from pathlib import Path

config = {
    "providers": {
        "aiw1": {
            "baseUrl": os.environ["BASE_URL"],
            "api": "openai-completions",
            "apiKey": "$OPENAI_API_KEY",
            "compat": {"supportsDeveloperRole": False, "supportsReasoningEffort": False},
            "models": [{
                "id": os.environ["MODEL"],
                "name": "Qwen on aiw1",
                "reasoning": False,
                "input": ["text"],
                "contextWindow": 32768,
                "maxTokens": 8192,
                "cost": {"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
            }],
        }
    }
}
(Path(os.environ["PI_HOME"]) / "models.json").write_text(json.dumps(config, indent=2) + "\n")
PY

base_commit=$(git -C "$repo_root" rev-parse HEAD)
for agent in "${agents[@]}"; do
  branch="experiment/$run_id/$agent"
  git -C "$repo_root" worktree add -q -b "$branch" "$run_root/worktrees/$agent" "$base_commit"
done

common_prompt=$(cat <<'EOF'
You are one member of a seven-agent two-pizza team optimizing OpenLore's naive
Ripgrep function. Your identity is in AGENT_ID and your checkout is an isolated
git worktree. Never push, fetch, pull, reset, clean, edit another worktree, or
access the OpenLore backing directory. Coordinate only through the OpenLore
backend with "$TEAM_LORE '<command>'". Start with
"$TEAM_LORE 'cat /instructions/README.md'", then read
/instructions/contract.md and /instructions/roles/$AGENT_ID.md. Those files
and /trusted-results are read-only. Write concise posts only under
/channels/<topic>/posts/$AGENT_ID/ and discussions only under
/threads/$AGENT_ID/<slug>/. @mentions fan out source-linked notifications to
private inboxes. Use unique UTC filenames and never overwrite another file.
Publish negative results. Optimize real work only: no sleeps,
hardcoding, corpus/query/path recognition, skipped reads, or weakened tests.
Run focused correctness tests and statistically meaningful benchmarks. Commit
each coherent code candidate locally and post its hash and evidence through
OpenLore. The corpora are in OPENLORE_RG_CORPUS and OPENLORE_RG_DEEP_CORPUS.
After reading your instructions, immediately post a short plan in your assigned
channel before doing longer analysis or code changes so the team can see and
respond to your direction.
EOF
)

agent_channel() {
  case "$1" in
    coordinator) echo coordination ;;
    benchmark) echo benchmarking ;;
    profiler) echo profiling ;;
    traversal) echo traversal ;;
    scanner | regex) echo matching ;;
    integrator) echo verification ;;
  esac
}

post_lifecycle() {
  local agent=$1
  local state=$2
  local detail=$3
  local channel stamp content
  channel=$(agent_channel "$agent")
  stamp=$(date -u +%Y%m%dT%H%M%SZ)
  detail=${detail//\'/’}
  content="---
type: lifecycle
agent: $agent
state: $state
timestamp: $stamp
run: $run_id
---
$detail"
  AGENT_ID="$agent" TEAM_RUN_DIR="$runtime" "$board_lore" \
    "echo '$content' > /channels/$channel/posts/$agent/$run_id-$state.md" >/dev/null
}

run_with_timeout() {
  local timeout=$1
  shift
  python3 -c '
import os
import signal
import subprocess
import sys

timeout = int(sys.argv[1])
process = subprocess.Popen(sys.argv[2:], start_new_session=True)
try:
    status = process.wait(timeout=timeout)
except subprocess.TimeoutExpired:
    os.killpg(process.pid, signal.SIGTERM)
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        process.wait()
    status = 124
sys.exit(status)
' "$timeout" "$@"
}

run_agent() {
  local agent=$1
  local role_prompt=$2
  local tools=${3:-read,bash,edit,write,grep,find,ls}
  (
    cd "$run_root/worktrees/$agent"
    post_lifecycle "$agent" started \
      "Agent process started on branch \`experiment/$run_id/$agent\` with a ${agent_timeout}s limit."
    set +e
    AGENT_ID="$agent" TEAM_LORE="$board_lore" TEAM_RUN_DIR="$runtime" \
      OPENLORE_RG_CORPUS="$repo_root/.benchdata/mdn-content/files/en-us" \
      OPENLORE_RG_DEEP_CORPUS="$repo_root/.benchdata/deep-markdown" \
      OPENAI_API_KEY="$api_key" PI_CODING_AGENT_DIR="$run_root/pi-home" \
      run_with_timeout "$agent_timeout" \
      "$pi" --provider aiw1 --model "$model" --thinking off --print --mode json \
        --tools "$tools" --no-session "$common_prompt

$role_prompt" >"$run_root/logs/$agent.log" 2>&1
    status=$?
    set -e

    head=$(git rev-parse --short HEAD)
    changes=$(git status --short | wc -l | tr -d ' ')
    if [[ "$status" -eq 124 ]]; then
      post_lifecycle "$agent" timed-out \
        "Agent process timed out after ${agent_timeout}s. Its draft worktree is preserved with $changes uncommitted path(s); last commit: \`$head\`. See the run log before reusing any draft."
    elif [[ "$status" -ne 0 ]]; then
      post_lifecycle "$agent" failed \
        "Agent process exited with status $status. Its draft worktree is preserved with $changes uncommitted path(s); last commit: \`$head\`."
    else
      post_lifecycle "$agent" finished \
        "Agent process exited successfully. Worktree has $changes uncommitted path(s); last commit: \`$head\`. Consult this agent’s evidence posts before integration."
    fi
    return "$status"
  )
}

wait_batch() {
  local status=0
  local pid
  for pid in "$@"; do
    wait "$pid" || status=1
  done
  return "$status"
}

run_agent coordinator "You are the coordinator. Read all channel READMEs and inspect the benchmark and rg contract. Post a diverse decomposition in /channels/coordination and create one thread per independent direction. Do not modify or commit product code. Explicitly counter early convergence and duplicated approaches." "read,bash,grep,find,ls" || true

run_agent benchmark "You own benchmarking. Audit corpus fidelity, stability, and anti-cheat coverage. You may improve tests or harness code, but do not optimize Ripgrep. Commit useful changes and post evidence in the benchmarking channel." &
pid1=$!
run_agent profiler "You own profiling. Establish CPU and allocation profiles of the committed naive baseline on both corpora, identify measured bottlenecks, and post evidence in the profiling channel. Make only profiling-oriented changes if essential." &
pid2=$!
run_agent traversal "You own traversal. Implement one measured path/traversal optimization while preserving deterministic ordering and VFS semantics. Coordinate in the traversal channel." &
pid3=$!
wait_batch "$pid1" "$pid2" "$pid3" || true

run_agent scanner "You own line scanning and allocations. Implement one measured strategy preserving CRLF, final-newline, empty-file, long-line, and atomic-error behavior. Coordinate in the matching channel." &
pid4=$!
run_agent regex "You own matching strategy. Explore literal/regex fast paths that remain correct for all Go RE2 patterns and case-insensitive mode. Coordinate in the matching channel and avoid duplicating scanner work." &
pid5=$!
wait_batch "$pid4" "$pid5" || true

run_agent integrator "You are the integrator, not the verifier. Read every channel, relevant thread, and specialist branch. Cherry-pick compatible candidates, run focused checks, and commit the integrated candidate. Submit its hash to the trusted verifier with: curl -sS -H 'Content-Type: application/json' -d '{\"agent\":\"integrator\",\"candidate_commit\":\"<hash>\",\"base_commit\":\"$base_commit\",\"run_now\":true}' http://127.0.0.1:18765/api/submissions. You cannot write /trusted-results. Post the result ID in /channels/verification/posts/integrator/." || true

cat <<EOF
Team run complete.
Model: $model
Base commit: $base_commit
Worktrees and logs: $run_root
Integrator branch: experiment/$run_id/integrator
Dashboard and trusted leaderboard: http://127.0.0.1:18765
EOF
