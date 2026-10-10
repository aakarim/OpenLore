# Auth

Every transport ends in the same question: which `lore.json` identity is this
caller, and what may it do? This page explains the credentials OpenLore accepts,
how each one resolves to an identity, the roles and docsets that identity is
granted, and how workload identity federation lets CI jobs and services
authenticate without a long-lived secret.

Policy lives in `lore.json`, named by `auth_file` in `openlore.yml`. Server
settings such as ports, served files and analytics are covered in
[Configure the server](configure-the-server.md).

## One identity model, several credentials

A credential proves who is calling. It never creates authority of its own: the
identity it resolves to already exists in `lore.json`, and the caller gets that
identity's grants on every transport. The reserved `guest` identity covers
callers who present no recognised credential and can receive only read-only
grants.

| Transport | Credential | Resolves to |
|---|---|---|
| SSH | None (keyless) | `guest`, while `allow_keyless` is true |
| SSH | Public key listed on an identity | That identity |
| SSH | Certificate signed by a CA in `--ca-keys` | The identity whose key the certificate wraps, or the identity named by a certificate principal |
| SSH | Unrecognised key | `guest` when `unknown_identity` is `allow`; rejected when it is `deny` |
| MCP over HTTP, JSON API | OpenLore bearer token from `/oauth/token` | The identity in the token's `sub`, narrowed by its scope |
| MCP over HTTP, JSON API | No token | The anonymous posture inherited from SSH, unless `mcp.require_auth` is true |
| Dashboard | Passkey session or bearer token | Always required for data endpoints, even on anonymous instances |
| Inbox upload | `olin_...` bearer or HMAC credential | The exact identity the credential was created for |

The token-bearing rows share one issuer. The `tokens` block in `openlore.yml`
turns on an ES256 signer, and every OpenLore token, however it was obtained,
carries the same claims and is checked by the same resolver. That is why
settings such as `tokens.access_ttl` apply to human and machine logins alike.

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

## SSH

The SSH server resolves a connection in order: a public key that matches an
identity's `public_key`; a CA-signed certificate whose principal names an
identity; otherwise `guest` or a rejection, depending on `unknown_identity`.

### SSH certificates

Use `--ca-keys` to trust CA-signed user certificates and `--host-cert` to serve a
CA-signed host certificate. This is the strongest option for environments that
operate an SSH certificate authority.

### Verify the SSH host key over HTTPS

SSH otherwise relies on trust on first use. OpenLore displays its public host
key on the web front page and serves it from `GET /host-key`. Put the HTTP server
behind TLS, then install the key before connecting:

```bash
curl -s https://docs.example.com/host-key | \
  awk '{print "[docs.example.com]:2222 " $0}' >> ~/.ssh/known_hosts

ssh -p 2222 docs.example.com
```

See `examples/` for Caddy reverse-proxy configurations.

## Passkeys and OAuth clients

People sign in to the dashboard and to OAuth-based agents with a passkey.
`passkey register --identity NAME --name LABEL` prints a short-lived
registration link; the browser then binds a WebAuthn credential to that
identity. Agents such as Claude Code, Codex, Cursor and OpenCode use OAuth 2.1
with PKCE: the person approves the client in the browser, and the
`authorization_code` grant returns an OpenLore access token plus a rotating
refresh token.

An OAuth client acts through a durable delegated identity such as
`claude-code@claude.ai`. The human stays the principal in `sub`; the client is
recorded in `act.sub`, and write history shows the pair as `principal/actor`.
Delegates can be capped below the principal's authority with `deny_docsets`
and `deny_capabilities`. Client identity, `private_key_jwt`, mTLS corroboration
and signing-key rotation are in [OAuth clients](authenticated-oauth-clients.md).

## Roles, docsets, and identities

[Docsets](docsets.md) explains what a docset is, why access is organised around
them, and how the most specific docset governs a path. This section covers the
`lore.json` keys.

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

### Docset paths

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

### Path aliases

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

### Identity home directories

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

The request format is in the [HTTP inbox API](inbox.md).

## Workload Identity Federation

Workload Identity Federation (WIF) lets CI runners, agents, and services
authenticate to OpenLore's `/mcp` and `/api` endpoints **without any long-lived
secret**. Instead of minting and distributing an OpenLore token (or an SSH key)
to every workload, the workload presents a short-lived JWT that its platform
already issues — GitHub Actions OIDC, Kubernetes/SPIFFE, Okta/Entra/Keycloak,
etc. — and OpenLore exchanges it for a short-lived OpenLore token.

```
  ┌── workload (CI / agent / pod) ──┐
  │ already has a platform OIDC JWT  │
  │  (GitHub Actions, K8s, Okta…)    │
  └───────────────┬─────────────────┘
                  │ POST /oauth/token
                  │   grant_type=jwt-bearer
                  │   assertion=<platform JWT>
                  ▼
        ┌───────────────────────────────┐
        │  OpenLore token endpoint        │
        │  1. verify JWT vs IdP JWKS      │
        │  2. match claims → a rule       │
        │  3. rule → identity + scope     │
        │  4. issue OpenLore access token │
        └───────────────┬────────────────┘
                        │ Authorization: Bearer <openlore token>
                        ▼
                 /mcp  and  /api
        (scoped to the resolved identity's lore,
         exactly like an SSH session)
```

The result is the same identity model used everywhere else in OpenLore: the
federated caller lands in a **lore** (docset access), with **capabilities**
(write/publish/approve) and a **home** — and is filtered to that lore's
filesystem just like an SSH session. Bearer tokens issued without WIF carry the
`full` scope and keep working unchanged; WIF is purely additive.

### Why WIF

- **No long-lived keys** handed to agents or CI runners.
- **Short-lived logins**, revoked passively by expiry (minutes/hours).
- **Identity comes from your IdP**, mapped to an OpenLore lore + capabilities.
- Works over HTTP/MCP with a trivial `Authorization: Bearer <jwt>` exchange. For
  the SSH transport analogue (short-lived SSH certificates via Teleport/OIDC),
  see the Teleport integration design.

### 1. Trust an external IdP

Register the IdP whose tokens you want to accept. OpenLore fetches its public
keys (JWKS) to verify assertion signatures.

```yaml
# openlore.yml
tokens:
  issuer: https://openlore.example       # your OpenLore instance (iss + JWKS base)
  audience: https://openlore.example     # one audience per instance
  access_ttl: 1h
  refresh_ttl: 720h

oidc_issuers:
  - issuer_url: https://token.actions.githubusercontent.com   # GitHub Actions OIDC
    jwks: { mode: discovery }            # fetch keys from OIDC discovery

  # Direct JWKS mode skips discovery, for example for a SPIRE trust bundle.
  - issuer_url: https://spire.example
    jwks:
      mode: url
      url: https://spire.example/keys
```

`audience` is the value your workloads must request in their platform JWT (see
the platform examples below). OpenLore rejects assertions whose `aud` does not
match. `jwks.mode` defaults to `discovery`. `url` mode requires `jwks.url`, and
`jwks.url` is rejected in discovery mode. Direct mode changes only where keys
come from: issuer, audience, expiry, signature, and asymmetric-algorithm checks
remain identical.

### 2. Map IdP claims → an OpenLore identity

WIF rules live in `lore.json` on the **identity** they resolve to. The identity
must already exist; federation never creates one. All predicates in a match are
ANDed, and exact `sub` matches take precedence over broader matches. Rules
narrow — never widen — the identity's authority via `scope`.

```json
{
  "name": "ci-indexer",
  "roles": ["reader"],
  "match": [{
    "sub_prefix": "repo:my-org/my-repo:",
    "aud": "https://openlore.example",
    "claims": {
      "iss": "https://token.actions.githubusercontent.com",
      "environment": "production"
    },
    "scope": "read",
    "ttl": "15m"
  }]
}
```

Match keys:

- **`sub_prefix`** — prefix match on the assertion `sub` (e.g. GitHub encodes
  `repo:org/repo:ref:refs/heads/main`; a prefix pins the repo without pinning
  every branch).
- **`sub`** — exact subject match.
- **`aud`** — required audience; pin it to your instance to prevent token reuse
  across services.
- **`claims`** — exact string values for additional claims such as `iss`,
  `environment`, or `ref`. Pin `iss` when trusted issuers could have overlapping
  subjects.
- **`scope`** — required `read` or `full` authority ceiling.
- **`ttl`** — optional Go duration cap for the exchanged token.

A match with none of `sub`, `sub_prefix`, `aud` or `claims` is ignored, so an
empty rule can never capture every assertion.

### 3. Scopes: narrow, never widen

A rule's `scope` **narrows** the identity's authority; it can never grant more
than the identity already has.

- **`full`** — full identity authority (the default an SSH key or passkey login
  resolves to). A WIF rule normally uses a *narrowing* scope instead.
- A narrowing scope (e.g. `read`) intersects with the identity's authority:
  effective authority = `identity_authority ∩ scope`.
- **Missing / empty / unrecognised scope → denied** (fail-closed) — never full.

This is what lets one `lore.json` identity back several WIF rules at different
privilege levels (read-only for PR builds, publish for `main`, etc.).

### 4. Platform examples

#### GitHub Actions

Request an OIDC token for your audience, then exchange it:

```yaml
# .github/workflows/index.yml
permissions:
  id-token: write            # allow the job to mint an OIDC token
jobs:
  index:
    runs-on: ubuntu-latest
    steps:
      - id: tok
        run: |
          JWT=$(curl -sS \
            "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=https://openlore.example" \
            -H "Authorization: Bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" \
            | jq -r .value)
          OL=$(curl -sS -X POST https://openlore.example/oauth/token \
            -d grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer \
            -d "assertion=$JWT" | jq -r .access_token)
          echo "OL_TOKEN=$OL" >> "$GITHUB_ENV"
      - run: |
          curl -sS -X POST https://openlore.example/api/shell \
            -H "Authorization: Bearer $OL_TOKEN" \
            -H 'Content-Type: application/json' \
            -d '{"command": "ls /knowledge"}'
```

#### Kubernetes / SPIFFE

Project a service-account token with your audience and exchange it the same way:

```yaml
# pod spec — projected SA token scoped to OpenLore's audience
volumes:
  - name: openlore-token
    projected:
      sources:
        - serviceAccountToken:
            audience: https://openlore.example
            expirationSeconds: 3600
            path: token
```

```bash
# in-container: exchange the projected token
JWT=$(cat /var/run/secrets/openlore/token)
OL=$(curl -sS -X POST https://openlore.example/oauth/token \
  -d grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer \
  -d "assertion=$JWT" | jq -r .access_token)
```

Add a match to the target identity for the SA subject
(`system:serviceaccount:<ns>:<name>` for SA tokens, or a `spiffe://…` ID). Use
discovery with a SPIRE OIDC Discovery Provider, or `jwks.mode: url` with a bare
JWKS/trust-bundle endpoint. WIF accepts bearer JWT-SVIDs; X.509-SVID/mTLS is not
part of this flow.

### 5. Use the OpenLore token

The exchanged token is an ordinary OpenLore bearer token. Present it to **both**
endpoints:

```bash
# MCP-over-HTTP
curl -X POST https://openlore.example/mcp \
  -H "Authorization: Bearer $OL_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'

# Plain JSON API
curl -X POST https://openlore.example/api/shell \
  -H "Authorization: Bearer $OL_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"command": "publish /knowledge/report.md < out.md"}'
```

Every call runs with the resolved identity's lore, capabilities, and home —
identical to what that identity gets over SSH. When `mcp.require_auth` is false
or omitted and anonymous SSH is allowed, a public/anonymous caller (no token)
still works on both HTTP transports, landing in the read-only `default` lore.
When `mcp.require_auth` is true, both `/mcp` and `/api` require a token.

### Relationship to the rest of auth

- **Human logins** (passkey → bearer token) use the *same* token endpoint with
  the `authorization_code` grant. WIF adds the `jwt-bearer` grant on top; one
  issuer, one identity resolver, one token format.
- **SSH access** is federated separately via short-lived SSH certificates
  (Teleport / native OIDC-over-SSH). WIF here covers the HTTP/MCP transports.

## Next steps

- [Configure the server](configure-the-server.md) sets up the `openlore.yml`
  side: ports, served files and analytics.
- [OAuth clients](authenticated-oauth-clients.md) covers client identity,
  `private_key_jwt`, mTLS corroboration and signing-key rotation.
- [openlore.yml reference](openlore-yml.md#tokens) documents the `tokens`,
  `oidc_issuers`, `passkeys`, `auth` and `mcp` keys.
- [Transports](transports.md) describes the MCP and JSON API endpoints the
  exchanged token is used with.
