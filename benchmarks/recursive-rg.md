# Recursive `rg` optimization benchmark

This experiment starts with the intentionally naive `Ripgrep` function in
`pkg/shell/cmds/rg.go`. It collects and sorts every path, reads complete files,
splits each file into lines, and searches sequentially. Optimize the real work;
artificial delays and benchmark-specific shortcuts are forbidden.

## Corpora

Run:

```bash
./scripts/fetch-rg-corpus.sh
```

The primary corpus is the English Markdown from
[MDN content](https://github.com/mdn/content), pinned to commit
`c808a24d4e4f7bda00e7117f315965ed39b780e5`. It contains more than 14,000
Markdown files and roughly 59 MB of content. The checkout retains MDN's
`LICENSE.md`; corpus files are downloaded locally and are not committed.

Ripgrep's own `tests/data` is deliberately not used: it has only a handful of
flat functional fixtures. The setup also generates 4,096 Markdown files spread
over a 64-level tree to isolate recursive traversal costs.

## Measurement

```bash
OPENLORE_RG_CORPUS="$PWD/.benchdata/mdn-content/files/en-us" \
  ./scripts/benchmark-rg.sh baseline-mdn

# After changing the implementation:
OPENLORE_RG_CORPUS="$PWD/.benchdata/mdn-content/files/en-us" \
  ./scripts/benchmark-rg.sh candidate-mdn
```

The benchmark fixes `GOMAXPROCS=1`, uses warm filesystem caches, runs each case
ten times, and records `ns/op`, throughput, allocations, Go version, host, and
commit. Compare complete samples with `benchstat`; do not compare best runs.

```bash
go run golang.org/x/perf/cmd/benchstat@latest \
  bench-results/baseline-mdn.txt bench-results/candidate-mdn.txt
```

A candidate is valid only if `go test ./...` and `go test -race
./pkg/shell/cmds -run Rg` pass and command output remains byte-for-byte stable.
Prefer improvements whose confidence interval excludes zero and reject a
headline win that regresses any primary workload by more than 5%.
