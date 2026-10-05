# Configure the server

OpenLore separates server configuration (`openlore.yml`) from identity, role,
and docset policy (`lore.json`). This page sets up `openlore.yml`; the policy
side is explained in [Auth](auth.md).

## Create `openlore.yml`

Create `openlore.yml` in the directory you run `openlore` from, or pass
`--config`. Every key is listed in the [openlore.yml reference](openlore-yml.md).

An explicitly loaded config file takes precedence over an embedded
`openlore.yml` and replaces it rather than merging with it. If no file is
loaded, OpenLore uses the embedded config when present, then built-in defaults;
command-line flags always win.

```yaml
version: "1"

# Enables verbose logs. Unknown shell commands and parser failures are recorded
# as structured debug events to support command/syntax gap analysis.
debug: false

port: 2222
metrics_port: 3000
http_port: 8080
host_key_path: .ssh/openlore_ed25519
default_cwd: /docs

mcp:
  enabled: true
  path: /mcp

motd: |
  Welcome to Acme Corp docs.
  Type 'tree -L 1 /' to get started.

files:
  allowed:
    - "*.md"
    - "*.txt"
    - "*.yml"
    - "*.json"
  ignore:
    - ".git"
    - "node_modules"
    - ".env"

# Folder rules (see docs/folder-rules.md). `growth` is the default multiplier
# for `max: initial` size rules and must be at least 1. `rules.tokenizer` is
# reserved and rejected at boot until a real tokenizer ships; size/tokens uses
# the built-in estimator.
rules:
  growth: 1.25

# skills_dir: ./skills
# auth_file: ./lore.json
# tls_cert: ./cert.pem
# tls_key: ./key.pem
```

## Analytics

Analytics uses SQLite by default for both aggregation materialisations and the
per-file current-facts cache:

```yaml
analytics:
  enabled: true
  dir: analytics
  aggregations:
    store: sqlite # use file for the legacy materialisation store (no facts cache)
  pipeline:
    enabled: true
```

`analytics.enabled: false` disables the complete analytics application,
including durable event logging. To retain events, metrics, and stored views
while pausing analytics processing, leave analytics enabled and set
`analytics.pipeline.enabled: false`. Re-enabling the pipeline catches the
durable event index up from its checkpoint.

One bounded processor handles both content-fact reconciliation and requested
dashboard views. It executes one expensive unit at a time; dashboard demand is
promoted ahead of routine warming without canceling in-flight work. SQLite
stores file facts, ownership-aware directory totals, durable events, completed
dashboard views, and checkpoints. OpenLore detects external edits by comparing file size and
modification time. An edit that changes neither is picked up at the next full
reconciliation.

## Debug logging

Debug logging can also be enabled with `openlore --debug`. Unknown-command
events include only the command name, not its arguments. Parser-failure events
include a syntax sample capped at 512 bytes and the parser error.

## Next steps

- [Auth](auth.md) covers `lore.json`: authentication posture, roles, docsets,
  identities and credentials.
- [openlore.yml reference](openlore-yml.md) documents every key, including
  `tokens`, `passkeys`, `inbox` and `analytics`.
- [Dashboard and metrics](dashboard.md) explains what the analytics pipeline
  feeds.
