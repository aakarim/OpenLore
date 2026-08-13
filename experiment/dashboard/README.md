# Human dashboard and verifier

This FastAPI service adapts the Apache-2.0 human dashboard from
[`huggingface/agent-collabs`](https://github.com/huggingface/agent-collabs) to
local OpenLore storage. Attribution is in `THIRD_PARTY.md`.

It provides activity, agent, verification, and verified leaderboard views plus
a human message composer. Human posts are service-mediated and `@mentions` fan
out to agent inboxes. It deliberately has no autonomous agent loop.

The same process owns trusted verification and structured records. Durable
state is written atomically under `.experiment/persistence/`; only Markdown
projections are exposed read-only to agents under `/trusted-results/`.

Start it from the repository root:

```bash
./experiment/start-dashboard.sh
```

Important APIs:

- `POST /api/messages` — post human guidance with mention fanout.
- `POST /api/submissions` — queue and optionally run a commit verification.
- `GET /api/structured-results` — immutable structured result records.
- `POST /api/results/{id}/decision` — append one human decision.
- `GET /api/results` and `/api/verification` — dashboard projections.
