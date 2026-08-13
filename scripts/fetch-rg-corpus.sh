#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
data_root="$repo_root/.benchdata"
mdn_root="$data_root/mdn-content"
deep_root="$data_root/deep-markdown"
mdn_commit=c808a24d4e4f7bda00e7117f315965ed39b780e5

mkdir -p "$data_root"

if [[ ! -d "$mdn_root/.git" ]]; then
  git init -q "$mdn_root"
  git -C "$mdn_root" remote add origin https://github.com/mdn/content.git
  git -C "$mdn_root" sparse-checkout init --no-cone
  printf '/files/en-us/\n/LICENSE.md\n' >"$mdn_root/.git/info/sparse-checkout"
fi

if [[ "$(git -C "$mdn_root" rev-parse HEAD 2>/dev/null || true)" != "$mdn_commit" ]]; then
  git -C "$mdn_root" fetch --depth=1 origin "$mdn_commit"
  git -C "$mdn_root" checkout -q --detach FETCH_HEAD
fi

actual=$(git -C "$mdn_root" rev-parse HEAD)
[[ "$actual" == "$mdn_commit" ]] || {
  echo "MDN commit mismatch: got $actual, want $mdn_commit" >&2
  exit 1
}

rm -rf "$deep_root"
DEEP_ROOT="$deep_root" python3 <<'PY'
import os
from pathlib import Path

root = Path(os.environ["DEEP_ROOT"])
path = root
payload = "OpenLore recursive search benchmark. javascript markdown content.\n" + ("x" * 1984) + "\n"
for depth in range(64):
    path = path / f"d{depth:02d}"
    path.mkdir(parents=True)
    for leaf in range(64):
        marker = "deepest-sentinel\n" if depth == 63 and leaf == 63 else ""
        (path / f"document-{leaf:02d}.md").write_text(
            f"# Depth {depth}, document {leaf}\n{marker}{payload}", encoding="utf-8"
        )
PY

MDN_ROOT="$mdn_root/files/en-us" DEEP_ROOT="$deep_root" python3 <<'PY'
import os
from pathlib import Path

for label, value in (("MDN", os.environ["MDN_ROOT"]), ("deep", os.environ["DEEP_ROOT"])):
    root = Path(value)
    files = list(root.rglob("*.md"))
    size = sum(p.stat().st_size for p in files)
    depth = max((len(p.relative_to(root).parts) - 1 for p in files), default=0)
    print(f"{label}: path={root} markdown_files={len(files)} bytes={size} max_depth={depth}")

mdn_files = list(Path(os.environ["MDN_ROOT"]).rglob("*.md"))
if len(mdn_files) < 14_000:
    raise SystemExit(f"MDN corpus unexpectedly small: {len(mdn_files)} Markdown files")
PY

cat <<EOF

Benchmark MDN:
  OPENLORE_RG_CORPUS="$mdn_root/files/en-us" ./scripts/benchmark-rg.sh baseline-mdn

Benchmark deep synthetic corpus:
  OPENLORE_RG_CORPUS="$deep_root" ./scripts/benchmark-rg.sh baseline-deep
EOF
