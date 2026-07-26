# Configuration and Identity

OpenLore separates server configuration (`openlore.yml`) from identity, role,
and docset policy (`lore.json`).

## Server configuration

Create `openlore.yml` in the project root or pass `--config`:

```yaml
version: "1"

port: 2222
metrics_port: 3000
http_port: 8080
host_key: .ssh/openlore_ed25519
allow_keyless: true
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

# skills_dir: ./skills
# auth_file: ./lore.json
# tls_cert: ./cert.pem
# tls_key: ./key.pem
```

## Authentication posture

Keyless SSH is enabled by default. Set `allow_keyless: false` to require a
recognized key or another configured authentication method.

Unknown SSH keys are controlled in `lore.json`:

- `"unknown_identity": "allow"` resolves them to the built-in `guest` role.
- `"unknown_identity": "deny"` rejects them.

Keyless and unknown allowed callers use `guest`, which can receive only
read-only grants.

MCP-over-HTTP can inherit this posture or independently require OAuth:

```yaml
mcp:
  enabled: true
  path: /mcp
  require_auth: true
```

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
  "docsets": {
    "public": {
      "paths": ["/docs/public"],
      "access": { "allow": { "guest": "ro", "backend": "ro" } }
    },
    "backend": {
      "paths": ["/docs/api", { "internal/specs": "/docs/specs" }],
      "aliases": ["/api"],
      "access": { "allow": { "backend": "rw" } }
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

The `admin` capability grants access to administrative commands and exposes the
active configuration files at `/opt/openlore/lore.json` and
`/opt/openlore/openlore.yml`. These mounted files are always read-only; use the
corresponding OpenLore administration commands to modify configuration. The
`/opt/openlore` directory is hidden from identities without `admin`, and the
host configuration filenames are excluded from the ordinary lore tree.

## Docset paths

Each docset exposes one or more virtual paths. A path may directly mount the
corresponding source path or map a source path to a different display path:

```json
"paths": [
  "/docs/api",
  { "internal/specs": "/docs/specs" }
]
```

Authorization is evaluated against the owning docset. Nested docsets create
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
preserves the spelling used by the caller, but authorization, approvals,
changesets, hooks, events, inboxes, and `$HOME` use the canonical path.

Aliases must be absolute and normalized. They cannot overlap another alias,
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
