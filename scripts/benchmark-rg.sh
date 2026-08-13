#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
corpus=${OPENLORE_RG_CORPUS:-}
label=${1:-candidate}

if [[ -z "$corpus" || ! -d "$corpus" ]]; then
  echo "OPENLORE_RG_CORPUS must name a prepared corpus directory" >&2
  echo "Run ./scripts/fetch-rg-corpus.sh first." >&2
  exit 2
fi

mkdir -p "$repo_root/bench-results"
output="$repo_root/bench-results/$label.txt"

{
  echo "go: $(go version)"
  echo "commit: $(git -C "$repo_root" rev-parse HEAD)"
  echo "corpus: $(cd "$corpus" && pwd)"
  echo "os: $(uname -a)"
  echo
  OPENLORE_RG_CORPUS="$corpus" GOMAXPROCS=1 \
    go test ./pkg/shell/cmds -run '^$' -bench '^BenchmarkRipgrepCorpus/' \
      -benchmem -benchtime=3x -count=10
} | tee "$output"

echo "Wrote $output"
