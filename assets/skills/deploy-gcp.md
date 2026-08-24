# Deploy OpenLore to Google Cloud

Deliver one production OpenLore server on Google Cloud with persistent disk state, HTTPS/MCP, authenticated OpenLore SSH, and Compute Engine administrative access.

## Preconditions and plan

- Work only from the Git root containing `openlore.yml`; stop if missing. There is one authoritative production server and no staging server.
- Use the locally authenticated `gcloud` account/project. Never ask users to paste service-account keys or other credentials and never commit secrets.
- Discover project, billing context, region/zone, VPC/firewalls, DNS, certificates, instances, disks, addresses, and load balancers. Normally use Compute Engine plus a persistent disk; reject products that cannot meet stable admin and persistence requirements.
- Present machine type, image, zone, disk type/size, static IP/load balancers, DNS/TLS, firewall rules, and cost impact. Ask before creating shared or billable infrastructure.
- Preserve resources on partial failure; never delete, recreate, or format an existing persistent disk automatically.

## Repository and deployment contract

- Build/deploy a root `Containerfile` derived from an exact semver, e.g. `FROM ghcr.io/aakarim/openlore:1.2.3`; no floating tags. Keep the derivative for future package customization and do not bake configuration into it.
- Root `openlore.yml` is authoritative, deployed separately at `/var/lib/openlore/config/openlore.yml`, and must set `auth_file: /var/lib/openlore/config/lore.json`, `writable_dir: /var/lib/openlore/published`, `data_dir: /var/lib/openlore/data`, and `host_key_path: /var/lib/openlore/ssh/openlore_ed25519`, with OpenLore SSH internal port `2222` and the configured HTTP port.
- Bootstrap state is only gitignored `.local/lore.json` and `.local/filesystem`.
- Put rerunnable, discovery-first scripts and non-secret resource metadata under `deploy/gcp/`; use labels and checks for idempotency, show diffs, and commit only on explicit approval. IaC tooling is optional, not prescribed.

## Provision and networking

- Create one Compute Engine VM, attach one persistent disk, mount it at `/var/lib/openlore` via stable disk identity, deploy root `openlore.yml` independently of the image, and run `./out --config /var/lib/openlore/config/openlore.yml`. Ensure the derivative container restarts after container/VM reboot.
- Use `gcloud compute ssh` (including IAP/OS Login where configured) for administrative access. Do not add a separate public OS SSH daemon just for OpenLore.
- Terminate HTTPS for web/MCP with a Google Cloud HTTPS load balancer and managed certificate when feasible, or an approved valid-TLS host proxy for a simpler footprint.
- Prefer a regional external passthrough Network Load Balancer forwarding public TCP `22` to OpenLore internal `2222`; do not publicly expose `2222` as well. If this topology is unavailable or disproportionate, expose `2222` and explicitly mark standard-port ingress optional/pending.
- Record VM, disk, address/load-balancer, DNS, project/zone, and immutable image digest metadata without secrets.

## Bootstrap and production verification

Inspect the mounted disk over `gcloud compute ssh`. Copy or reconcile root `openlore.yml` to the persistent config path. Transfer `.local/lore.json` and `.local/filesystem` only if the remote lore file is absent and published/data state is empty. Create `config`, `published`, `data`, and `ssh` securely. Stop when mutable remote state exists; never overwrite or merge it.

Verify valid HTTPS, MCP initialization, authenticated OpenLore SSH, write/read of a unique probe, persistence after container and VM restart (including auth and host key), `gcloud compute ssh` administrative access, and intended firewall/forwarding rules. When TCP 22 forwarding exists, verify `ssh <domain>` works without `-p` and that `2222` is not public.

Return exact HTTPS/MCP, OpenLore SSH, and `gcloud compute ssh` reconnect commands, project/zone/resource IDs, image digest, and any pending standard-port ingress. Only after all checks pass may the generic deploy skill offer deletion of `.local`; keep by default.
