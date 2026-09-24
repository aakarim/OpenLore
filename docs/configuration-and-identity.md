# Configuration and Identity

OpenLore separates server configuration (`openlore.yml`) from identity, role,
and docset policy (`lore.json`).

## Server configuration

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

Debug logging can also be enabled with `openlore --debug`. Unknown-command
events include only the command name, not its arguments. Parser-failure events
include a syntax sample capped at 512 bytes and the parser error.

## Authentication posture

Keyless SSH is enabled by default. Set `allow_keyless: false` to require a
recognised key or another configured authentication method.

Unknown SSH keys are controlled in `lore.json`:

- `"unknown_identity": "allow"` resolves them to the built-in `guest` role.
- `"unknown_identity": "deny"` rejects them.

Keyless and unknown allowed callers use `guest`, which can receive only
read-only grants.

MCP-over-HTTP and the JSON API can inherit this posture or jointly require
OAuth. The existing `mcp.require_auth` setting governs both HTTP transports:

```yaml
mcp:
  enabled: true
  path: /mcp
  require_auth: true
```

If the resolved posture requires a token (`allow_keyless: false` inherited, or
`require_auth: true`) but no `tokens` block is configured, `/mcp` and `/api`
fail closed with 401 and the server logs a warning at startup. Configure
`tokens`, or set `require_auth: false` to serve anonymous HTTP callers.

## Roles, docsets, and identities

```json
{
  "allow_keyless": true,
  "unknown_identity": "allow",
  "default_cwd": "/docs",
  "roles": {
    "backend": {
      "allow": { "capabilities": ["spawn"] }
    }
  },
  "rules": {
    "doc-size": { "match": ["**/*.md"], "use": "size/kilobytes", "with": { "max": 60 }, "default": true }
  },
  "docsets": {
    "public": {
      "paths": ["/docs/public"],
      "access": { "allow": { "guest": "ro", "backend": "ro" } }
    },
    "backend": {
      "paths": ["/docs/api", { "internal/specs": "/docs/specs" }],
      "aliases": ["/api"],
      "access": { "allow": { "backend": "rw" } },
      "rules": {
        "format": { "match": ["**/*.md"], "use": "okf" }
      },
      "config": { "edit": ["backend"] }
    },
    "backend-home": {
      "paths": ["/home/backend"]
    }
  },
  "identities": [
    {
      "name": "backend-agent",
      "public_key": "ssh-ed25519 AAAA...",
      "roles": ["backend"],
      "home": "backend-home"
    }
  ]
}
```

Docsets grant exact role names:

- `ro` reads the docset.
- `publish` reads the docset and writes only inside its configured inbox.
- `rw` reads and writes throughout the docset.
- Plugins may contribute additional grant types.

Multiple roles contribute grants independently. Any matching docset deny wins.
Capability allows form a union across roles, while any capability deny wins.

Folder rules use three keys, all documented in [Folder rules](folder-rules.md):

- Top-level `rules` declares rules that apply to every docset. Each rule has
  `match`, optional `exclude`, `use` (a member such as `size/kilobytes` or
  `okf`), `with` (the member's parameters), `enforce` (default `true`) and
  `default` (`true` lets a folder's `.lore/config.yaml` replace the rule under
  the same name).
- `docsets.<name>.rules` declares rules for that docset's paths, with the same
  shape. The older `docsets.<name>.okf` block is still accepted and is
  equivalent to declaring the `okf`, `okf/bundle`, `link/resolves` and
  `link/alias` rules.
- `docsets.<name>.config.edit` lists the roles allowed to create, edit or
  delete `.lore/config.yaml` files under the docset and to run
  `lore size baseline reset`. The role also needs a write grant on the path. A
  docset without `config.edit` has no one who can change its folder rules.

## HTTP inbox credentials

Inbox upload credentials are not OAuth tokens. OAuth authenticates the token
management endpoints (`POST/GET /inbox/tokens`, `DELETE /inbox/tokens/{id}`),
while a generated `olin_...` bearer or HMAC authenticates
`POST /inbox/{docset}`. The CLI equivalents are:

```bash
openlore inbox token create --identity alice --ttl 24h --config openlore.yml
openlore inbox token list --config openlore.yml
openlore inbox token revoke TOKEN_ID --config openlore.yml
```

Creation requires `auth_file`. At upload time the token must still exist, be
unexpired, and be bound to an exact, currently existing identity; aliases,
unknown-identity fallback, and deleted identities are rejected. That identity's
live docset grants are evaluated for every upload.

`inbox.max_upload_size` is the bounded in-memory raw HTTP body/multipart cap,
and `inbox.allowed_types` independently controls extension/MIME pairs. This
policy does not widen ordinary shell, MCP, or DirFS content/size policy.
Multipart parsing retains aggregate file-part copies no larger than the raw
body and releases the raw body before commit.

HMAC signs the exact raw body as `HMAC-SHA256(secret, "timestamp." + body)`.
Replay state is process-local, bounded to 1,000 signatures per token with a
separate 10,000-signature global guard. One full token cannot block another,
but multi-instance deployments need sticky routing or shared replay protection.

Multipart files form one ordered batch. Commits are in request order with no
rollback: if a later leaf fails, the HTTP 500 JSON includes `committed_paths`
for the durable prefix, and post-commit audit receives that exact prefix and
the actor.

## Docset paths

Each docset exposes one or more virtual paths. A path may directly mount the
corresponding source path or map a source path to a different display path:

```json
"paths": [
  "/docs/api",
  { "internal/specs": "/docs/specs" }
]
```

Authorisation is evaluated against the owning docset. Nested docsets create
independent policy boundaries rather than inheriting their parent's grants.

## Path aliases

Aliases expose alternate virtual roots for a docset's first canonical path:

```json
{
  "docsets": {
    "jared": {
      "paths": ["/agent/jared"],
      "aliases": ["/jared"]
    }
  }
}
```

`/agent/jared/notes.md` and `/jared/notes.md` address the same file. Navigation
preserves the spelling used by the caller, but authorisation, the write log,
plugin middleware, events, inboxes, and `$HOME` use the canonical path.

Aliases must be absolute and normalised. They cannot overlap another alias,
mount, or canonical path at or beneath the alias.

## Identity home directories

An identity can name one unique docset as its home:

```json
{
  "name": "backend-agent",
  "public_key": "ssh-ed25519 AAAA...",
  "roles": ["backend"],
  "home": "backend-home"
}
```

The home docset's display path becomes `$HOME`, enabling `~`, `~/path`, and `cd`
with no arguments. It does not change the initial directory, which remains
`default_cwd`.

```bash
ssh -p 2222 server 'echo $HOME'
ssh -p 2222 server 'cat ~/notes.md'
ssh -p 2222 server 'cd && pwd'
```

The owner receives implicit `rw` on its home. Nested docsets remain separate
boundaries and do not inherit that access.

## Manage identities and roles

Add an identity from the CLI:

```bash
openlore identity add \
  --name my-agent \
  --key "ssh-ed25519 AAAA..." \
  --role backend \
  --home backend-home \
  --auth ./lore.json
```

`--key` is optional, allowing passkey- or token-only identities.

Manage policy with:

- `openlore role add|remove`
- `openlore role grant|revoke`
- `openlore role deny|undeny`
- `openlore role capability allow|deny|remove`
- `openlore identity role add|remove --identity NAME --role ROLE`

## SSH certificates

Use `--ca-keys` to trust CA-signed user certificates and `--host-cert` to serve a
CA-signed host certificate. This is the strongest option for environments that
operate an SSH certificate authority.

## Verify the SSH host key over HTTPS

SSH otherwise relies on trust on first use. OpenLore displays its public host
key on the web front page and serves it from `GET /host-key`. Put the HTTP server
behind TLS, then install the key before connecting:

```bash
curl -s https://docs.example.com/host-key | \
  awk '{print "[docs.example.com]:2222 " $0}' >> ~/.ssh/known_hosts

ssh -p 2222 docs.example.com
```

See `examples/` for Caddy reverse-proxy configurations.
