#!/usr/bin/env bash
set -euo pipefail

readonly ghostty_revision=4540d499ae463ad7b90f28f6f852f64f844c160f
readonly cache_root="${XDG_CACHE_HOME:-${HOME}/.cache}/openlore-libghostty"
readonly ghostty_dir="${GHOSTTY_DIR:-${cache_root}/ghostty-${ghostty_revision}}"
readonly transcripts="$(mktemp -d)"
trap 'rm -rf "$transcripts"' EXIT

if ! command -v zig >/dev/null 2>&1 || [[ "$(zig version)" != "0.16.0" ]]; then
    echo "test-libghostty: Zig 0.16.0 is required" >&2
    exit 1
fi

if [[ ! -d "${ghostty_dir}/.git" ]] ||
    [[ "$(git -C "$ghostty_dir" rev-parse HEAD 2>/dev/null || true)" != "$ghostty_revision" ]]; then
    rm -rf "$ghostty_dir"
    mkdir -p "$(dirname "$ghostty_dir")"
    git init -q "$ghostty_dir"
    git -C "$ghostty_dir" remote add origin https://github.com/ghostty-org/ghostty.git
    git -C "$ghostty_dir" fetch -q --depth 1 origin "$ghostty_revision"
    git -C "$ghostty_dir" checkout -q FETCH_HEAD
fi

zig build --build-file "${ghostty_dir}/build.zig" \
    --cache-dir "${ghostty_dir}/.zig-cache" \
    --global-cache-dir "${cache_root}/zig" \
    --prefix "${ghostty_dir}/zig-out" \
    -Demit-lib-vt -Doptimize=ReleaseFast -Dsimd=false

OPENLORE_LIBGHOSTTY_TRANSCRIPTS="$transcripts" \
    go test ./pkg/shell -run '^TestLibghosttyTranscript$' -count=1

cc -std=c11 -DGHOSTTY_STATIC \
    -I"${ghostty_dir}/zig-out/include" \
    internal/libghosttytest/harness.c \
    "${ghostty_dir}/zig-out/lib/libghostty-vt.a" \
    -o "${transcripts}/harness"

"${transcripts}/harness" \
    "${transcripts}/terminal-80.bin" \
    "${transcripts}/terminal-20.bin"
