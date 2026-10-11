#!/usr/bin/env bash
set -euo pipefail

# Keep the executable out of go.mod: Litestream runs as a separate process.
version=0.5.17
if [[ -z "${LITESTREAM_BIN:-}" ]]; then
  case "$(uname -s)/$(uname -m)" in
    Linux/x86_64) platform=linux-x86_64 ;;
    Linux/aarch64|Linux/arm64) platform=linux-arm64 ;;
    Darwin/arm64) platform=darwin-arm64 ;;
    Darwin/x86_64) platform=darwin-x86_64 ;;
    *) echo 'Set LITESTREAM_BIN to a Litestream 0.5.17 executable on this platform.' >&2; exit 1 ;;
  esac
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  archive="litestream-${version}-${platform}.tar.gz"
  base="https://github.com/benbjohnson/litestream/releases/download/v${version}"
  curl -fsSL --retry 3 "$base/$archive" -o "$tmp/$archive"
  curl -fsSL --retry 3 "$base/checksums.txt" -o "$tmp/checksums.txt"
  expected=$(awk -v name="$archive" '$2 == name { print $1 }' "$tmp/checksums.txt")
  actual=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
  [[ -n "$expected" && "$actual" == "$expected" ]] || { echo 'Litestream checksum mismatch' >&2; exit 1; }
  tar -xzf "$tmp/$archive" -C "$tmp" litestream
  export LITESTREAM_BIN="$tmp/litestream"
fi
[[ "$("$LITESTREAM_BIN" version)" == "$version" ]] || { echo "Expected Litestream $version" >&2; exit 1; }
go test -race -tags=integration ./pkg/openlore -run '^TestLitestreamSQLite$' -count=1 -timeout=15m
