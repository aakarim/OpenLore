# Workload Identity Federation

> **Status: shipped.** The `jwt-bearer` grant is live: configure
> `oidc_issuers` (below) and OpenLore verifies external IdP assertions against
> each issuer's JWKS and exchanges them for OpenLore tokens. Bearer tokens
> issued without WIF carry the `full` scope and keep working unchanged — WIF is
> purely additive.

Workload Identity Federation lets CI runners, agents, and services authenticate
to OpenLore's `/mcp` and `/api` endpoints **without any long-lived secret**.
Instead of minting and distributing an OpenLore token (or an SSH key) to every
workload, the workload presents a short-lived JWT that its platform already
issues — GitHub Actions OIDC, Kubernetes/SPIFFE, Okta/Entra/Keycloak, etc. —
and OpenLore exchanges it for a short-lived OpenLore token.

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

The result is the same `Identity` model used everywhere else in OpenLore: the
federated caller lands in a **lore** (docset access), with **capabilities**
(write/publish/approve) and a **home** — and is filtered to that lore's
filesystem just like an SSH session.

## Why WIF

- **No long-lived keys** handed to agents or CI runners.
- **Short-lived logins**, revoked passively by expiry (minutes/hours).
- **Identity comes from your IdP**, mapped to an OpenLore lore + capabilities.
- Works over HTTP/MCP with a trivial `Authorization: Bearer <jwt>` exchange. For
  the SSH transport analogue (short-lived SSH certificates via Teleport/OIDC),
  see the Teleport integration design.

## 1. Trust an external IdP

Register the IdP whose tokens you want to accept. OpenLore fetches its public
keys (JWKS) to verify assertion signatures.

```yaml
# openlore.yml
auth:
  tokens:
    issuer: https://openlore.example       # your OpenLore instance (iss + JWKS base)
    audience: https://openlore.example     # one audience per instance
    access_ttl: 1h
    refresh_ttl: 720h

  oidc_issuers:
    - issuer_url: https://token.actions.githubusercontent.com   # GitHub Actions OIDC
      jwks: { mode: discovery }            # fetch keys via OIDC discovery (default)
    - issuer_url: https://spire.example    # issuer without a discovery document
      jwks:                                # fetch a JWKS document directly, e.g. a
        mode: url                          # SPIRE trust-bundle endpoint
        url: https://spire.example/keys
```

`audience` is the value your workloads must request in their platform JWT (see
the platform examples below). OpenLore rejects assertions whose `aud` does not
match.

## 2. Map IdP claims → an OpenLore identity

WIF rules live alongside the exact-`sub` rules used for human/passkey logins. A
rule matches on the assertion's claims and resolves to a named **identity** that
must already exist in `lore.json` (see `cat /auth.md`). Rules narrow — never
widen — the identity's authority via `scope`.

```yaml
  rules:
    # human / passkey / SSH identity (exact sub) — for contrast
    - match: { sub: "alice" }

    # WIF: any GitHub Actions run in org/repo, requesting our audience
    - match:
        sub_prefix: "repo:my-org/my-repo:"
        aud: "https://openlore.example"
      identity: ci-indexer      # must exist in lore.json identities[]
      scope: "read"             # narrow below the identity's full authority
      ttl: 15m                  # cap the OpenLore token lifetime for this rule
```

Match keys:

- **`sub_prefix`** — prefix match on the assertion `sub` (e.g. GitHub encodes
  `repo:org/repo:ref:refs/heads/main`; a prefix pins the repo without pinning
  every branch).
- **`sub`** — exact subject match.
- **`aud`** — required audience; pin it to your instance to prevent token reuse
  across services.
- Additional claim matches (e.g. `environment`, `ref`) can be required so, say,
  only the `production` environment maps to a write-capable identity.

## 3. Scopes: narrow, never widen

A rule's `scope` **narrows** the identity's authority; it can never grant more
than the identity already has.

- **`full`** — full identity authority (the default an SSH key or passkey login
  resolves to). Reserved; a WIF rule normally uses a *narrowing* scope instead.
- A narrowing scope (e.g. `read`) intersects with the identity's authority:
  effective authority = `identity_authority ∩ scope`.
- **Missing / empty / unrecognized scope → denied** (fail-closed) — never full.

This is what lets one `lore.json` identity back several WIF rules at different
privilege levels (read-only for PR builds, publish for `main`, etc.).

## 4. Platform examples

### GitHub Actions

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

### Kubernetes / SPIFFE

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

Add a matching rule for the SA subject
(`system:serviceaccount:<ns>:<name>` for SA tokens, or the SPIFFE ID) and set
its `issuer_url`/`jwks` to your cluster or SPIRE bundle.

## 5. Use the OpenLore token

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
  -d '{"command": "publish knowledge report.md < out.md"}'
```

Every call runs with the resolved identity's lore, capabilities, and home —
identical to what that identity gets over SSH. A public/anonymous caller (no
token) still works wherever anonymous SSH does, landing in the read-only
`default` lore.

## Relationship to the rest of auth

- **Human logins** (passkey → bearer token) use the *same* token endpoint with
  the `authorization_code` grant. WIF adds the `jwt-bearer` grant on top; one
  issuer, one identity resolver, one token format.
- **SSH access** is federated separately via short-lived SSH certificates
  (Teleport / native OIDC-over-SSH). WIF here covers the HTTP/MCP transports.
- See `cat /auth.md` for the `lore.json` identity/docset/lore model these rules
  resolve into, and `cat /mcp.md` for the MCP and JSON API endpoints.
