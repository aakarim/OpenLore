# Deploy OpenLore to Railway

Deliver one production OpenLore server on Railway with HTTPS/MCP, authenticated OpenLore SSH, a persistent volume, and `railway ssh` administrative access.

## Preconditions and safety

- Work only from the Git project root containing `openlore.yml`; stop if it is absent. Deploy one authoritative server, never staging.
- Use the locally authenticated Railway CLI session (`railway whoami`). Never ask for credentials in chat or commit tokens/secrets.
- Discover the selected workspace, project, environment, service, domains, TCP proxies, and volumes. Present the service plan, region, volume size, HTTP domain, TCP proxy, and expected cost impact, and ask before creating shared or billable resources.
- Preserve all resources on partial failure and never delete a volume automatically.

## Build and repository contract

1. Inspect and show diffs for root `openlore.yml`, `.gitignore`, and a root `Containerfile` based on an exact semver such as `FROM ghcr.io/aakarim/openlore:1.2.3`. Never use `latest`; deploy the derivative image and leave it available for future package customization. Do not bake configuration into it.
2. Root `openlore.yml` is authoritative and must be deployed separately at `/var/lib/openlore/config/openlore.yml`. Set `auth_file: /var/lib/openlore/config/lore.json`, `writable_dir: /var/lib/openlore/published`, `data_dir: /var/lib/openlore/data`, and `host_key_path: /var/lib/openlore/ssh/openlore_ed25519`; listen for OpenLore SSH on `2222` and HTTP on the Railway application port. After Railway allocates its TCP proxy, record that port as `external_ssh_port` in root `openlore.yml` before deploying the config.
3. Runtime bootstrap exists only as gitignored `.local/lore.json` and `.local/filesystem`; ensure `.local/` is ignored.
4. Put idempotent scripts and non-secret project/service/environment/volume/domain metadata under `deploy/railway/`. Discover before creating, show diffs, and offer commits only after explicit approval.

## Provision and deploy

- Link or create one Railway project/environment/service and deploy the root Containerfile with the Railway CLI.
- Attach one persistent volume at `/var/lib/openlore`. Generate/configure an HTTP domain and HTTPS routing to OpenLore's HTTP port for web and MCP.
- Start OpenLore as `./out --config /var/lib/openlore/config/openlore.yml`. Copy or reconcile root `openlore.yml` onto the volume independently of image deployment before starting or restarting the service.
- Create a Railway TCP proxy targeting internal `2222`. Railway assigns a proxy hostname and random external port; report public standard port 22 as **pending**. Do not claim `ssh <domain>` works without `-p`. Only use standard port 22 if the user already provides and approves an external TCP load balancer that maps `22` to Railway's assigned proxy endpoint.
- Record immutable deployment/image details and generated endpoint metadata under `deploy/railway/`, excluding secrets.
- Use `railway ssh` as the administrative shell. Do not add an OS SSH daemon merely for administration.

## Bootstrap without data loss

Inspect `/var/lib/openlore` using `railway ssh` or Railway volume file operations before upload. Deploy root `openlore.yml` to the persistent config directory. Transfer `.local/lore.json` and `.local/filesystem` only when mutable state is empty: no remote lore file and no existing published/data filesystem state. Create `config`, `published`, `data`, and `ssh` beneath `/var/lib/openlore` with restrictive permissions. If mutable remote state exists, stop; never overwrite or merge over remote `lore.json` or filesystem data.

## Required production verification

1. Verify valid HTTPS and complete an MCP initialization exchange.
2. Verify authenticated OpenLore SSH using the exact Railway TCP proxy hostname and assigned port.
3. Write and read a unique OpenLore probe, restart/redeploy the service, and read it again; verify auth and host key persistence too.
4. Verify `railway ssh` reaches the running service administratively.
5. Confirm the volume remains mounted at `/var/lib/openlore`, there is only one production service, and no unintended public port exists.

Return exact HTTPS/MCP, `railway ssh`, and `ssh -p <assigned-port> <proxy-host>` reconnect commands, service/environment/volume/domain identifiers, and image digest. Clearly mark standard port 22 ingress optional/pending unless an external TCP load balancer was supplied and verified. Only after every check passes may the generic deploy skill offer deletion of `.local`; keep it by default.
