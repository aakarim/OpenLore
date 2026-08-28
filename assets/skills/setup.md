# Set Up an OpenLore Project

You are installing OpenLore and teaching it at the same time — never do
something the person cannot follow. Open with “👋 Let's set up your lore
server.” Immediately after the opening, set the mental model in one or two
sentences: setup does not put the shared server on this machine — it creates a
small Git project (the server's blueprint) here, verifies it with a throwaway
local run, and the later `deploy` step shares the server with the team on real
infrastructure. Then describe the whole journey in a few plain-language
lines — interview, pick a release, tracked config, identity and permissions,
throwaway local verification, checks, review — and get a go-ahead before
starting.
Before each stage, announce in one sentence what you are about to do and why,
and wait for consent before creating files, starting servers, or changing
anything; answering questions never needs permission. Interview one question at
a time and wait; explain why it matters, recommend a default, and define
unfamiliar terms just in time. If there is no safe default, say why a choice is
required. Use restrained emojis. Run checks quietly and mention only failures
or decisions. After each stage give a short ✅ plain-language summary, never
“acceptance evidence”. Use an official release; do not clone, fork, vendor, or
add OpenLore as a submodule. After fixing a failure, summarize its user
impact—not a technical postmortem—unless asked.

## Outcome

Produce one Git repository named from the user's team with this shape:

```text
<team-slug>-lore/
├── Containerfile
├── openlore.yml
├── .gitignore
├── deploy/
│   └── README.md
└── .local/
    ├── compose.yml
    ├── lore.json
    ├── known_hosts
    ├── ssh_config
    ├── filesystem/
    │   ├── user/onboarding/README.md
    │   └── channel/general/INDEX.md
    └── runtime.env
```

`openlore.yml` is the project marker and the Git/IaC authority for server
configuration. `.local/` is gitignored bootstrap state. It is never part of the
image and is never committed.

## 1. Interview and inspect

Ask in order, one per turn, with its reason and default:
1. Team display name (required to identify its owner; no safe default).
2. “Who is this for?” Ask for a short description of the organization, person,
   and team where applicable — or their website address, and you will make a
   best effort to pull the details from it. If they give a website, fetch it,
   draft the description yourself, and confirm the draft before using it.
   Explain that this seeds useful shared context instead of an empty server; a
   sentence or two is enough, with no factual default.
3. Derive a lowercase slug using letters, numbers, and single hyphens, then
   suffix `-lore` (`Acme Research` → `acme-research-lore`); allow correction.
4. Creation location (default: a new child of the current directory). Explain
   that this folder holds the server's project files — its blueprint — not the
   running shared server, which `deploy` later puts on shared infrastructure.
5. SSH public key and matching private-key path. Explain that this key identifies
   their first account; the private key stays put for their SSH client. Default
   to one key for OpenLore and infrastructure, but offer separate keys.

Silently inspect the destination, the Go toolchain, Compose tooling, and ports.
For a non-empty destination show conflicts and require approval; never
overwrite `openlore.yml`. For the local verification runtime, prefer an
installed Go toolchain that satisfies the selected release's `go.mod`; fall
back to Compose (Docker, then Podman) only when there is none. Default to ports
2222/8080; choose free ones when occupied and report them in the stage summary.

## 2. Select the OpenLore release and SSH key

Find the latest stable semantic tag that exists on `ghcr.io/aakarim/openlore`;
do not infer it from GitHub releases. Ignore prereleases unless requested. If
none exists, explain and ask whether to use `latest` temporarily or stop. Confirm
the version, then create this `Containerfile`:

```dockerfile
FROM ghcr.io/aakarim/openlore:1.2.3
```

Otherwise never use `latest`, ranges, branches, or digest-only references. The
Containerfile selects OpenLore and permits later extension. Do not bake config or
bootstrap state into it.

Validate the selected public/private key pair by authentication, not by printing
private material. Never copy, print, generate, replace, or commit a private key.

## 3. Create tracked configuration

Create root `openlore.yml` with config schema `version: "1"` and at least:

```yaml
version: "1"

port: 2222
http_port: 8080
metrics_port: 0
default_cwd: /

host_key_path: /var/lib/openlore/ssh/openlore_ed25519
auth_file: /var/lib/openlore/config/lore.json
data_dir: /var/lib/openlore/data
writable_dir: /var/lib/openlore/published
readonly: false

mcp:
  enabled: true
  path: /mcp

api:
  enabled: true
  path: /api

passkeys:
  enabled: true
  rp_id: localhost
  rp_name: "<team display name> Lore"
  rp_origins: ["http://localhost:<selected HTTP port>"]
  lore_path: /lore
  passkeys_file: /var/lib/openlore/data/passkeys.json
  session_ttl: 24h
```

These standard Unix paths are identical locally and remotely. Dynamic policy
does not belong in this file.

Create `.gitignore` with `/.local/`. Create `deploy/README.md` explaining that
provider deployment skills place committed scripts and non-secret resource
metadata below `deploy/<provider>/`; credentials and runtime state never go
there.

## 4. Create local bootstrap state (teach the permission model here)

This stage is the heart of the teaching. Before writing anything, explain the
permission model in a few short sentences and confirm the person is happy with
what you are about to grant:

- An **identity** is the account OpenLore recognizes when their SSH key
  connects. You are creating one named `onboarding`, tied to their public key.
- A **docset** is a permission-controlled knowledge folder. They get a private
  home (`/user/onboarding`, writable only by them) and a shared `general`
  docset (`/channel/general`) for the team.
- A **role** is a named permission bundle granted to identities; docsets allow
  roles `ro` or `rw`. Their `agent` role gets `rw` on `general`.
- The **administrator** role carries the `lore:config:edit` capability — it can
  change this policy from an SSH session. Say so and get explicit agreement
  before granting it.

Then create `.local/lore.json` exactly in this schema — field names and nesting
matter; do not invent variants such as `grants` or `permissions`:

```json
{
  "allow_keyless": false,
  "unknown_identity": "deny",
  "roles": {
    "administrator": {
      "allow": { "capabilities": ["lore:config:edit"] }
    },
    "agent": {}
  },
  "docsets": {
    "onboarding-home": {
      "paths": ["/user/onboarding"]
    },
    "general": {
      "paths": ["/channel/general"],
      "access": { "allow": { "agent": "rw" } }
    }
  },
  "identities": [
    {
      "name": "onboarding",
      "public_key": "<selected public key>",
      "roles": ["administrator", "agent"],
      "home": "onboarding-home"
    }
  ]
}
```

Validate the complete JSON. A docset without an `access.allow` entry is
reachable only as an identity's `home`, which is implicitly read/write for its
owner. If the running server disagrees with this schema, trust
`docs/configuration-and-identity.md` on the server (`cat
/docs/configuration-and-identity.md` over SSH) over this template and report
the difference.

Create both paths below `.local/filesystem/`. Put a short `README.md` in the
private home. Put a concise summary of the confirmed “Who is this for?” answer
at the very top of `/channel/general/INDEX.md`, followed by useful organization,
person, and team details without inventing facts. Do not overwrite user content.
This is the initial SSH-visible filesystem. Do not put generated host private keys,
signing keys, audit logs, tokens, or runtime databases in bootstrap state; each
deployed server creates its own operational state.

## 5. Create disposable local runtime state

Put `compose.yml` and `runtime.env` inside `.local/` regardless of which local
runtime will run the server; they document deployment parity and serve hosts
without Go. Use this minimal structure,
replacing the project name with the team slug; its mounts keep config, content,
data, and host keys outside the image and persistent. Inherit the image command,
which already loads the mounted config; do not override `command` or `entrypoint`:

```yaml
name: team-lore
services:
  openlore:
    build:
      context: ..
      dockerfile: Containerfile
    restart: unless-stopped
    ports:
      - "${OPENLORE_SSH_PORT}:2222"
      - "${OPENLORE_HTTP_PORT}:8080"
    volumes:
      - ../openlore.yml:/var/lib/openlore/config/openlore.yml:ro
      - ./lore.json:/var/lib/openlore/config/lore.json
      - ./filesystem:/var/lib/openlore/published
      - openlore-data:/var/lib/openlore/data
      - openlore-ssh:/var/lib/openlore/ssh
volumes:
  openlore-data:
  openlore-ssh:
```

Write `OPENLORE_SSH_PORT` and `OPENLORE_HTTP_PORT` to `runtime.env`. When
Compose is the local runtime, always invoke it with
`docker compose --env-file .local/runtime.env -f .local/compose.yml` (or the
Podman equivalent) so interpolation is deterministic.

Run the local server directly with Go when the inspected toolchain satisfies
the selected release's `go.mod`. This avoids image pulls and emulation on hosts
whose architecture does not match the published images, and it changes only how
the local server runs — the tracked Containerfile remains the deployment
architecture:

1. Clone OpenLore into a separate temporary directory and check out the
   selected release.
2. Remove that clone's embedded `assets/config/openlore.yml` before building,
   because a binary cannot load embedded and external configuration together.
3. Build `./cmd/openlore` into an ignored local path.
4. Create an ignored `.local/openlore.local.yml` derived from root
   `openlore.yml`, replacing `/var/lib/openlore` paths with absolute paths below
   this project's `.local/`. Do not modify the tracked production config.
5. Run the binary with `--config .local/openlore.local.yml`.

Do not commit the clone, binary, or local config. Use Compose only when no
suitable Go toolchain exists. Run the same acceptance suite regardless of which
local runtime was used.

## 6. Require local acceptance

Build and start the local server. Capture its host key into
`.local/known_hosts`, replacing only a stale entry for this loopback host and
port. Create `.local/ssh_config` with the selected private-key path, `User
onboarding`, the chosen port, `UserKnownHostsFile`, and strict host-key checking.
Never disable host-key checking or mutate global `~/.ssh/known_hosts`.

Quietly run all checks; setup is not successful until they pass:

1. HTTP readiness succeeds.
2. `/mcp` completes an MCP initialization exchange.
3. `passkey` is available; the mounted config is active: keyless login fails and
   `ssh -F .local/ssh_config lore-local` authenticates `onboarding`.
4. The SSH session starts at `/`.
5. The home README and shared INDEX are visible; a unique disposable document can be
   written to `/channel/general`, read back, and removed.
6. After restarting the local server (container or Go process), authentication,
   filesystem contents, and the SSH host key persist.

Diagnose failures at their owning layer and rerun the failed check. After any
restart, wait for HTTP readiness before rerunning checks; a write attempted
while the server is still starting can fail spuriously. Never weaken
authentication or hardcode around a failed check.

## 7. Finish reviewably

Initialize Git if needed. Show all tracked files and prove `.local/` is ignored.
Finish with a friendly ✅ summary: what was created, which local runtime is
serving (Go binary or Compose) and how to restart it, selected release version,
local URLs, identity/home, ports, and key used. Do not narrate successful
internal checks. Give these commands for a new terminal:

```bash
ssh -F .local/ssh_config lore-local
ssh -F .local/ssh_config lore-local 'cat /channel/general/INDEX.md'
```

Offer an initial commit only after explicit approval; never push automatically.

Then ask whether to continue in this session (“run onboarding”) or later with
`ssh openlore.sh onboarding | <agent-cli>` to add people/folders; deployment is
similarly “run deploy” or `ssh openlore.sh deploy | <agent-cli>`, and its
benefit is to share the server with the team. Explain that
setup is complete and onboarding is optional customization. One project is one
authoritative server; do not create staging environments.
