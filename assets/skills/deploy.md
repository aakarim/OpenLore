# Deploy an OpenLore Project

Deploy one locally verified OpenLore project to one authoritative server. Use a
maintained provider guide when applicable; otherwise interview the user, create
custom deployment artifacts, and prove the same outcomes.

## Project contract

Work only from a directory containing root `openlore.yml`. Inspect before
changing anything:

- `Containerfile` must derive from a pinned semantic
  `ghcr.io/aakarim/openlore:<version>` image without configuration baked in;
- root `openlore.yml` is the Git/IaC authority;
- `.local/lore.json` and `.local/filesystem` are gitignored bootstrap state;
- provider scripts and non-secret deployment metadata belong under `deploy/`;
- one project has one authoritative deployed server and no staging copy.

For a first deployment, require `.local` and rerun the local acceptance checks
from `setup`. For an existing deployment, remote state is authoritative and
`.local` may legitimately be absent.

## Select deployment guidance

Ask where the user wants to deploy. Fetch and follow the matching instruction
command from the same OpenLore server:

- `deploy-fly`
- `deploy-railway`
- `deploy-aws`
- `deploy-gcp`
- `deploy-azure`
- `deploy-digitalocean`

If none applies, ask how the user wants to deploy and what CLI, IaC, CI/CD, DNS,
registry, and infrastructure already exist. Generate a custom implementation
under `deploy/<provider-or-system>/`. Label it custom in metadata, but do not
call it unsuccessful merely because it is not a maintained recipe. Validate
outcomes, not a preferred tool.

## Required outcome specification

Every implementation must provide:

1. A machine or service with provider-supported administrative shell access.
2. The derivative Containerfile image running at a recorded immutable digest.
3. Valid HTTPS and a reachable MCP endpoint.
4. Authenticated OpenLore SSH.
5. A persistent volume mounted at `/var/lib/openlore`.
6. Deploy root `openlore.yml` separately at
   `/var/lib/openlore/config/openlore.yml`. Keep the inherited command/entrypoint,
   which runs `./out --config /var/lib/openlore/config/openlore.yml`; never
   override it with an argument array. If required, pass the whole command as
   one shell string. The config must set:
   - `auth_file: /var/lib/openlore/config/lore.json`
   - `writable_dir: /var/lib/openlore/published`
   - `data_dir: /var/lib/openlore/data`
   - `host_key_path: /var/lib/openlore/ssh/openlore_ed25519`
7. `openlore.yml`, `lore.json`, the SSH-visible filesystem, server data, and
   host key surviving container and machine replacement supported by that
   provider.

Use provider defaults for administrative SSH; do not add an OS SSH daemon for
OpenLore.
When the provider supports TCP ingress, prefer public port 22 forwarded to
OpenLore 2222 and do not also expose 2222. Otherwise expose the assigned 2222
endpoint and suggest an external TCP load balancer or forwarding system. Raw
SSH has no hostname/SNI routing, so standard-port ingress needs a
dedicated address/listener rather than HTTP hostname routing.

## Safety and IaC rules

- Use locally authenticated provider CLIs. Never request credentials or private
  keys in chat and never commit secrets.
- Discover existing resources and state before planning.
- Show topology, region, sizing, persistent storage, ingress, DNS, and likely
  cost impact; ask before creating or changing shared/billable resources.
- Make deployment idempotent and discovery-first. Reruns reconcile instead of
  duplicating machines, disks, load balancers, records, or repositories.
- Preserve partial resources for diagnosis/resume. Never delete, replace,
  reformat, or overwrite a persistent disk automatically.
- Root `openlore.yml` follows IaC and is deployed separately from the image to
  the persistent config directory. A platform-native projection such as a
  Kubernetes ConfigMap is valid. Detect runtime drift and ask whether to import
  it or reconcile from Git. Dynamic remote `lore.json` and filesystem edits are
  not drift and must never be overwritten.
- Store scripts plus non-secret resource IDs, endpoints, image digest, and
  verification status under `deploy/<provider>/`. Show diffs and offer a commit
  only after explicit approval; never push automatically.

## Bootstrap exactly once

Before OpenLore first starts against a new volume, prove the volume is empty.
Copy root `openlore.yml` to `/var/lib/openlore/config/openlore.yml`, transfer
`.local/lore.json` to `/var/lib/openlore/config/lore.json`, and transfer
`.local/filesystem` into `/var/lib/openlore/published`. Put no bootstrap or
mutation logic in the OpenLore process. Generate operational host keys, signing
keys, logs, and databases on the remote server itself.

If remote `lore.json` or filesystem state exists, stop initialization and
preserve it. A missing local metadata file never authorizes reseeding. Never
merge or synchronize `.local` over initialized remote state.

On later deployments, reconcile only tracked `openlore.yml` to its persistent
path (or update the provider's equivalent external config projection), then
restart OpenLore. Never replace `lore.json` or the SSH-visible filesystem as
part of continuous deployment.

## Required production acceptance

Deployment is not verified until all applicable checks pass:

1. HTTPS has a valid certificate.
2. `/mcp` completes protocol initialization.
3. The principal key authenticates over OpenLore SSH and starts in its home.
4. A unique document can be written and read through OpenLore.
5. After a container/service restart, the document, auth policy, and SSH host
   key persist.
6. Provider administrative shell access works.
7. When standard ingress was provisioned, `ssh <domain>` succeeds without `-p`
   and public 2222 is closed. Otherwise the assigned port is verified and
   standard ingress is reported as optional/pending.

Return exact HTTPS/MCP, OpenLore SSH, and administrative reconnect commands,
resource identifiers, image digest, persistence evidence, and pending optional
ingress. Recommend provider snapshots/backups, but do not require them.

Only after every required check passes, ask whether to delete `.local`
entirely. Explain that the server is now authoritative and deletion removes the
bootstrap/recovery copy. Default to keeping it. Never delete it without an
explicit affirmative answer.
