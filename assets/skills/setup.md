# Set Up an OpenLore Project

You are installing OpenLore, not merely generating files. Open with “👋 Let's
set up your lore server.” Interview one question at a time and wait; explain why
it matters, recommend a default, and define unfamiliar terms just in time. If
there is no safe default, say why a choice is required. Use restrained emojis.
Run checks quietly and mention only failures or decisions. After each stage give
a short ✅ plain-language summary, never “acceptance evidence”. Use an official
release; do not clone, fork, vendor, or add OpenLore as a submodule.

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
    │   └── channel/general/README.md
    └── runtime.env
```

`openlore.yml` is the project marker and the Git/IaC authority for server
configuration. `.local/` is gitignored bootstrap state. It is never part of the
image and is never committed.

## 1. Interview and inspect

Ask in order, one per turn, with its reason and default:
1. Team display name (required to identify its owner; no safe default).
2. Derive a lowercase slug using letters, numbers, and single hyphens, then
   suffix `-lore` (`Acme Research` → `acme-research-lore`); allow correction.
3. Creation location (default: a new child of the current directory).
4. SSH public key and matching private-key path. Explain that this key identifies
   their first account; the private key stays put for their SSH client. Default
   to one key for OpenLore and infrastructure, but offer separate keys.

Silently inspect the destination, Compose tooling, and ports. For a non-empty
destination show conflicts and require approval; never overwrite `openlore.yml`.
Prefer Docker then Podman Compose. Default to ports 2222/8080; choose free ones
when occupied and report them in the stage summary.

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
- one `onboarding` identity using the selected public key—explain that an
  identity is the account OpenLore recognizes when that key connects;
- a unique writable home docset at `/user/onboarding`—explain that a docset is
  a permission-controlled knowledge folder and this one is their private start;
- an `administrator` role with `lore:config:edit`;
- an `agent` role—explain that roles group permissions;
- a `general` docset rooted at `/channel/general` granting `agent` `rw`;
- `onboarding` assigned both `administrator` and `agent`.

Use exactly those fields and validate the complete JSON. The `onboarding-home`
docset has `paths: ["/user/onboarding"]`; `general` has
`paths: ["/channel/general"]` and grants role `agent` `rw`; the identity has
`name: "onboarding"`, the selected `public_key`, `home: "onboarding-home"`, and
roles `["administrator", "agent"]`.

Create both paths below `.local/filesystem/` and put a short `README.md` in each,
describing the private onboarding area and shared general area respectively, so
the first login always has visible content. Do not overwrite user content. This
is the initial SSH-visible filesystem. Do not put generated host private keys,
signing keys, audit logs, tokens, or runtime databases in bootstrap state; each
deployed server creates its own operational state.

## 5. Create disposable local Compose state

Put `compose.yml` and `runtime.env` inside `.local/`. Use this minimal structure,
replacing the project name with the team slug; its mounts keep config, content,
data, and host keys outside the image and persistent:

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

Build and start the local server. Capture its host key into
`.local/known_hosts`, replacing only a stale entry for this loopback host and
port. Create `.local/ssh_config` with the selected private-key path, `User
onboarding`, the chosen port, `UserKnownHostsFile`, and strict host-key checking.
Never disable host-key checking or mutate global `~/.ssh/known_hosts`.

Quietly run all checks; setup is not successful until they pass:

1. HTTP readiness succeeds.
2. `/mcp` completes an MCP initialization exchange.
3. `ssh -F .local/ssh_config lore-local` authenticates `onboarding`.
4. The SSH session starts at `/user/onboarding`.
5. Both default READMEs are visible; a unique disposable document can be
   written to `/channel/general`, read back, and removed.
6. After restarting the container, authentication, filesystem contents, and
   the SSH host key persist.

Diagnose failures at their owning layer and rerun the failed check. Never weaken
authentication or hardcode around a failed check.

## 7. Finish reviewably

Initialize Git if needed. Show all tracked files and prove `.local/` is ignored.
Finish with a friendly ✅ summary: what was created, what Compose now does
(builds and keeps the local server running with persistent state), selected
image version, local URLs, identity/home, ports, and key used. Do not narrate
successful internal checks. Give these commands for a new terminal:

```bash
ssh -F .local/ssh_config lore-local
ssh -F .local/ssh_config lore-local 'cat /channel/general/README.md'
```

Offer an initial commit only after explicit approval; never push automatically.

Then ask whether to continue in this session (“run onboarding”) or later with
`ssh openlore.sh onboarding | <agent-cli>` to add people/folders; deployment is
similarly “run deploy” or `ssh openlore.sh deploy | <agent-cli>`. Explain that
setup is complete and onboarding is optional customization. One project is one
authoritative server; do not create staging environments.
