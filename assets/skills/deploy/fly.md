# Deploy OpenLore to Fly.io

Deliver one production OpenLore server on Fly.io with HTTPS/MCP, authenticated
OpenLore SSH, durable state, and Fly administrative shell access.

## Preconditions and safety

- Work only from the Git project root containing `openlore.yml`; stop if it is absent. There is one authoritative server and no staging deployment.
- Use the locally authenticated `flyctl`/`fly` session. Never request cloud credentials in chat or write credentials into Git.
- Inspect the account, organization, app names, regions, existing Machines, IPs, and volumes before planning. Present the region, Machine size, volume size, public IP/networking, DNS/TLS, and estimated cost impact; get approval before creating any shared or billable infrastructure.
- Preserve resources after partial failure. Never destroy or replace a volume automatically.

## Build and repository contract

1. Inspect `openlore.yml`, `.gitignore`, and existing deployment assets. Show every proposed diff.
2. Create discoverable, rerunnable deployment scripts and non-secret resource metadata only under `deploy/fly/`. Make operations idempotent by discovering resources before creating them. Do not commit unless explicitly approved.
3. Create a root `Containerfile` derived from an explicitly pinned semver image, for example `FROM ghcr.io/aakarim/openlore:1.2.3` (never `latest` or a range). Deploy this derivative image, even if it currently adds nothing, so future packages can be customized there. Do not bake configuration into it.
4. Make root `openlore.yml` the Git/IaC authority, deploy it separately at `/var/lib/openlore/config/openlore.yml`, and configure:
   - `auth_file: /var/lib/openlore/config/lore.json`
   - `writable_dir: /var/lib/openlore/published`
   - `data_dir: /var/lib/openlore/data`
   - `host_key_path: /var/lib/openlore/ssh/openlore_ed25519`
   - OpenLore SSH on internal `2222` and HTTP on the configured application port.
5. Keep bootstrap state only in gitignored `.local/lore.json` and `.local/filesystem`. Ensure `.local/` is ignored.

## Provision and deploy

- Use a Fly app with one Machine and a Fly Volume mounted at `/var/lib/openlore`. Keep the Machine and volume in the same region. Disable automatic stop if it would violate restart availability.
- Start OpenLore as `./out --config /var/lib/openlore/config/openlore.yml`. Copy or reconcile root `openlore.yml` onto the volume independently of the image before starting or restarting the Machine.
- Configure an HTTP service for the OpenLore web/MCP port, health checks, Fly HTTPS termination, and the production domain.
- Prefer a dedicated Fly TCP service/public IP mapping public TCP `22` to Machine port `2222`; do not also expose public `2222`. Confirm this mapping is supported for the chosen shared/dedicated IP arrangement before creation. If it is not feasible, publish `2222` and explicitly report standard-port SSH as optional/pending.
- Use `fly deploy` and record the deployed immutable image digest, app, Machine, volume, region, hostname, and service mappings under `deploy/fly/`.
- Use `fly ssh console` for provider administrative access; do not install a public OS SSH daemon for administration.

## Bootstrap without data loss

Before transfer, inspect the mounted volume through `fly ssh console`. Deploy root `openlore.yml` to `/var/lib/openlore/config/openlore.yml`. Transfer `.local/lore.json` and `.local/filesystem` only if the remote lore file does not exist **and** the remote published/data state is empty. Create the standard directories with restrictive permissions and copy bootstrap filesystem content into the intended persistent paths. If mutable remote state exists, stop and preserve it; never merge over or overwrite remote `lore.json` or filesystem data.

## Required production verification

All checks must pass:

1. HTTPS succeeds with a valid certificate and the MCP endpoint completes protocol initialization.
2. Authenticated OpenLore SSH succeeds; use `ssh <domain>` without `-p` when public `22 -> 2222` exists, otherwise verify the reported `ssh -p 2222 <domain>` limitation.
3. Write a unique probe through OpenLore, read it back, restart the Machine, then read it again. Confirm the same auth state and SSH host key survive restart.
4. `fly ssh console -a <app>` reaches an administrative shell.
5. Confirm only intended public services exist and internal `2222` is not separately public when port 22 ingress exists.

Return exact HTTPS/MCP and SSH reconnect commands, `fly ssh console` command, app/region/volume identifiers, image reference and digest, and any pending standard-port ingress. Only after every required check passes may the generic deploy skill offer to delete `.local`; default to keeping it.
