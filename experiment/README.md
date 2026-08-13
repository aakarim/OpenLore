# OpenLore recursive-search agent collaboration

This is a local version of the
[Gemma agent collaboration](https://huggingface.co/spaces/agent-collaborations/gemma-collab-lessons),
adapted to optimize OpenLore's intentionally naive `Ripgrep` function. A
seven-agent two-pizza team uses Qwen through `http://aiw1:30001/v1`. OpenLore
itself is the collaboration backend.

```text
seven Qwen/Pi agents ──SSH──> OpenLore channels + threads
         │                              │
         └──isolated git worktrees      ├──mention inboxes
                                        └──read-only verified results

browser ──HTTP──> dashboard + trusted verifier ──> separate persistence
```

## Channels and threads

The article suggests channels to limit context collapse and threads to keep
replies attached to one idea. The OpenLore workspace implements both as
Markdown:

```text
instructions/{contract,roles/*}.md                 # agent read-only
channels/<topic>/posts/<agent>/<timestamp>.md      # role-owned writes
threads/<agent>/<slug>/*.md                        # role-owned discussions
inboxes/<agent>/
trusted-results/<result-id>.md                     # service-generated read-only view
```

Channels are `coordination`, `benchmarking`, `profiling`, `traversal`,
`matching`, and `verification`. A channel post should link one thread. Agents
read their channel and inbox rather than ingesting every message; the
coordinator and integrator read across channels.

Agent collaboration reads and writes go through OpenLore over SSH. A post or
thread reply that mentions `@benchmark` atomically adds a source-linked notice
to that agent's inbox. Agents cannot change instructions, other agents' areas,
inboxes, or trusted results. The service-owned JSON records, audit stream,
private corpus, and benchmark logs live separately in ignored
`.experiment/persistence/`, which is not mounted as an agent-writable docset.

## Run

Prerequisites: Go, Node.js 22.19+, npm, Python 3, `ssh`, and `ssh-keygen`.

```bash
./scripts/fetch-rg-corpus.sh
OPENLORE_RG_CORPUS="$PWD/.benchdata/mdn-content/files/en-us" \
  ./scripts/benchmark-rg.sh baseline-mdn

./experiment/start-collab.sh
./experiment/start-dashboard.sh

# QWEN_MODEL is optional; the script selects the first /v1/models ID
# containing "qwen" when it is unset.
QWEN_BASE_URL=http://aiw1:30001/v1 ./experiment/run-team.sh
```

`run-team.sh` requires a clean git worktree because every agent starts from the
same committed baseline. It installs pinned Pi under ignored `.experiment/`,
creates isolated worktrees, runs the coordinator, runs five specialists in
bounded batches, and asks the integrator to validate and combine candidates.
Each process posts lifecycle events to its channel, writes streaming JSON events
to its run log, and defaults to a 15-minute limit so a stalled model request
cannot silently block the team. Override the limit with
`OPENLORE_AGENT_TIMEOUT_SECONDS`. A failed specialist does not prevent the
other independent specialists or integrator from reporting their results. The
script never pushes. The dashboard is a human control and observation surface;
it does not contain an agent loop. `run-team.sh` remains the explicit loop and
the verifier independently evaluates submitted commits in fresh temporary git
worktrees.

Inspect the collaboration:

```bash
AGENT_ID=coordinator ./experiment/lore.sh 'tree -L 4 /'
AGENT_ID=coordinator ./experiment/lore.sh 'tree /trusted-results'
open http://127.0.0.1:18765
```

Submit a candidate manually with:

```bash
./experiment/submit-candidate.sh traversal CANDIDATE_COMMIT BASE_COMMIT
```

The verifier checks ancestry and limits product diffs to `rg.go`, runs full
tests, race tests, vet, hidden correctness checks on a private generated corpus,
and benchmarks the pinned MDN, depth-64, and private corpora with controlled
`GOMAXPROCS=1`. It writes immutable structured records and an append-only audit
stream. The dashboard leaderboard derives only from those verified records.

Stop the backend with `./experiment/stop-team.sh`. Worktrees and logs are
retained for review; the run prints their paths.

## Acceptance rules

- `go test ./...` and `go test -race ./pkg/shell/cmds -run Rg` must pass.
- Output, ordering, errors, and exit codes are frozen by `rg_test.go`.
- Retain ten complete samples, compare per-case medians, and aggregate with a
  geometric mean; never compare best runs.
- Accept only a statistically supported improvement of at least 5% overall.
- Reject any primary workload regression above 5%.
- Reject corpus/query/path detection, sleeps, skipped reads, hardcoded output,
  removed correctness checks, or benchmark changes that make work disappear.
