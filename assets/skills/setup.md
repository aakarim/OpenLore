# Set Up an OpenLore Project

Create a new, locally verified OpenLore project that deploys a thin derivative
of an official OpenLore release. Do not clone, fork, vendor, or add OpenLore as
a submodule.

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
    ├── filesystem/
    │   ├── user/onboarding/
    │   └── channel/general/
    └── runtime.env
```

`openlore.yml` is the project marker and the Git/IaC authority for server
configuration. `.local/` is gitignored bootstrap state. It is never part of the
image and is never committed.

## 1. Inspect before changing anything

1. Ask for the team display name, not a project name.
2. Derive a lowercase slug using letters, numbers, and single hyphens, then
   suffix it with `-lore` (for example, `Acme Research` becomes
   `acme-research-lore`). Show the result and allow correction.
3. Ask where to create it; default to a new child of the current directory.
4. If the destination exists and is not empty, show what conflicts and require
   explicit approval before touching it. Never overwrite an existing
   `openlore.yml`.
5. Detect Docker Compose or Podman Compose and available ports. Prefer local
   ports 2222 and 8080, but choose free ports when occupied.

## 2. Select the OpenLore release and SSH key

Find the latest stable semantic tag that actually exists on the public
`ghcr.io/aakarim/openlore` image. Do not assume a GitHub release has a matching
container tag. Ignore prereleases unless the user asks for one. If no stable
container tag exists, explain that and ask whether to use `latest` temporarily
or stop; never silently substitute it. Confirm the selected version, then
create this `Containerfile` with an exact base tag:

```dockerfile
FROM ghcr.io/aakarim/openlore:1.2.3
```

Do not use `latest`, a range, a Git branch, or a digest-only reference for the
initial project. The Containerfile only selects the published OpenLore build
today and remains the extension point for custom packages later. Do not bake
`openlore.yml`, `lore.json`, or bootstrap files into the image.

Ask the user to select an existing SSH public key. Offer separate keys for the
OpenLore principal and infrastructure administration, but default to using the
same selected key for both. Never read, copy, print, generate, replace, or
commit a private key without explicit need and approval.

## 3. Create tracked configuration

Create root `openlore.yml` with config schema `version: "1"` and at least:

```yaml
version: "1"

port: 2222
http_port: 8080
metrics_port: 0
default_cwd: /user/onboarding

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
```

These standard Unix paths are identical locally and remotely. Dynamic policy
does not belong in this file.

Create `.gitignore` with `/.local/`. Create `deploy/README.md` explaining that
provider deployment skills place committed scripts and non-secret resource
metadata below `deploy/<provider>/`; credentials and runtime state never go
there.

## 4. Create local bootstrap state

Create `.local/lore.json` with:

- `allow_keyless: false`;
- unknown identities denied;
- one `onboarding` identity using the selected public key;
- a unique writable home docset at `/user/onboarding`;
- an `administrator` role with `lore:config:edit`;
- an `agent` role;
- a `general` docset rooted at `/channel/general` granting `agent` `rw`;
- `onboarding` assigned both `administrator` and `agent`.

Use this shape, substituting the selected public key:

```json
{
  "allow_keyless": false,
  "unknown_identity": "deny",
  "default_cwd": "/user/onboarding",
  "roles": {
    "administrator": {
      "allow": {
        "capabilities": ["lore:config:edit"]
      }
    },
    "agent": {}
  },
  "docsets": {
    "onboarding-home": {
      "paths": ["/user/onboarding"]
    },
    "general": {
      "paths": ["/channel/general"],
      "access": {
        "allow": {
          "agent": "rw"
        }
      }
    }
  },
  "identities": [
    {
      "name": "onboarding",
      "public_key": "<selected SSH public key>",
      "home": "onboarding-home",
      "roles": ["administrator", "agent"]
    }
  ]
}
```

Create matching empty directories below `.local/filesystem/`. This directory is
the initial SSH-visible filesystem. Do not put generated host private keys,
signing keys, audit logs, tokens, or runtime databases in bootstrap state; each
deployed server creates its own operational state.

## 5. Create disposable local Compose state

Put `compose.yml` and `runtime.env` inside `.local/`, not at project root. The
Compose definition must:

- build the root `Containerfile`;
- mount root `openlore.yml` read-only at
  `/var/lib/openlore/config/openlore.yml`;
- mount `.local/lore.json` at `/var/lib/openlore/config/lore.json`;
- mount `.local/filesystem` at `/var/lib/openlore/published`;
- use named local volumes for `/var/lib/openlore/data` and
  `/var/lib/openlore/ssh` so operational state is not bootstrap state;
- map the free local HTTP and SSH ports recorded in `runtime.env`;
- run `./out --config /var/lib/openlore/config/openlore.yml` directly.

Use this minimal structure, replacing the Compose project name with the team
slug:

```yaml
name: team-lore
services:
  openlore:
    build:
      context: ..
      dockerfile: Containerfile
    restart: unless-stopped
    command: ["./out", "--config", "/var/lib/openlore/config/openlore.yml"]
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

Write `OPENLORE_SSH_PORT` and `OPENLORE_HTTP_PORT` to `runtime.env`, and always
invoke Compose with
`docker compose --env-file .local/runtime.env -f .local/compose.yml` (or the
Podman equivalent) so interpolation is deterministic.

If Compose is unavailable, explain the direct-Go fallback rather than silently
changing the project architecture:

1. Clone OpenLore into a separate temporary directory and check out the
   selected release.
2. Remove that clone's embedded `assets/config/openlore.yml` before building,
   because a binary cannot load embedded and external configuration together.
3. Build `./cmd/openlore` into an ignored local path.
4. Create an ignored `.local/openlore.local.yml` derived from root
   `openlore.yml`, replacing `/var/lib/openlore` paths with absolute paths below
   this project's `.local/`. Do not modify the tracked production config.
5. Run the binary with `--config .local/openlore.local.yml`.

Do not commit the clone, binary, or local config. Run the same acceptance suite
regardless of which local runtime was used.

## 6. Require local acceptance

Build and start the local server. Setup is not successful until all checks pass:

1. HTTP readiness succeeds.
2. `/mcp` completes an MCP initialization exchange.
3. The selected key authenticates the `onboarding` identity over OpenLore SSH.
4. The SSH session starts at `/user/onboarding`.
5. A uniquely named disposable document can be written to
   `/channel/general`, read back, and removed.
6. After restarting the container, authentication, filesystem contents, and
   the SSH host key persist.

Diagnose failures at their owning layer and rerun the failed check. Never weaken
authentication or hardcode around a failed check.

## 7. Finish reviewably

Initialize Git if needed. Show all tracked files and prove `.local/` is ignored.
Summarize the selected image version, local URLs, exact SSH command, key used,
and acceptance evidence. Offer to make an initial commit only after explicit
approval; never push automatically.

Tell the user to run `onboarding` for more initial identities or folders and
`deploy` when the local server is ready. One project represents one
authoritative deployed server; do not create staging environments.
