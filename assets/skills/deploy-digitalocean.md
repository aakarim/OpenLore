# Deploy OpenLore to DigitalOcean

Deliver one production OpenLore server on DigitalOcean with volume persistence, HTTPS/MCP, authenticated OpenLore SSH, and Droplet administrative access.

## Preconditions and plan

- Work only at the Git project root containing `openlore.yml`; stop if absent. Deploy one authoritative server and no staging environment.
- Use the locally authenticated `doctl` context. Never request API tokens/SSH private keys in chat or commit credentials and secrets.
- Discover account, project, region, VPC, Droplets, volumes, firewalls, domains, certificates, reserved IPs, and load balancers. Present Droplet size/image, volume size, region, LB/reserved IP, DNS/TLS, firewall, and cost impact and ask before creating shared or billable resources.
- Preserve resources on partial failure and never automatically delete, detach destructively, recreate, or format a volume.

## Repository and deployment contract

- Create a root `Containerfile` derived from an exact semver (`FROM ghcr.io/aakarim/openlore:1.2.3`, never `latest`) and deploy that derivative, leaving room for future package customization.
- Root `openlore.yml` is authoritative. Set `auth_file: /var/lib/openlore/config/lore.json`, `writable_dir: /var/lib/openlore/published`, `data_dir: /var/lib/openlore/data`, `host_key_path: /var/lib/openlore/ssh/openlore_ed25519`, internal OpenLore SSH `2222`, and the selected HTTP port.
- Bootstrap state is exclusively gitignored `.local/lore.json` and `.local/filesystem`.
- Put idempotent, discovery-first scripts and non-secret resource metadata under `deploy/digitalocean/`. Tag/check before creating, show diffs, and commit only with explicit approval. IaC is allowed but no specific tool is mandatory.

## Provision and networking

- Use `doctl` to provision one Droplet and one block-storage volume in the same region. Mount the volume persistently at `/var/lib/openlore`; ensure the derivative container restarts after container/Droplet reboot.
- Use the Droplet image's provider-default administrative SSH (and `doctl compute ssh` where available/configured). Do not add another public OS SSH daemon just for OpenLore.
- Prefer a DigitalOcean Load Balancer with HTTPS termination/valid certificate to the OpenLore HTTP port and a TCP forwarding rule from public `22` to Droplet `2222`. Do not expose `2222` publicly when this rule exists. If the LB is not feasible or approved, use valid host TLS and expose `2222`, explicitly reporting standard-port ingress optional/pending.
- Apply a least-privilege Cloud Firewall while preserving provider-default admin access. Record Droplet, volume, LB/IP, domain, region, and immutable image digest metadata without secrets.

## Bootstrap and production verification

Use provider-default administrative SSH to inspect the mounted volume. Upload `.local/lore.json` and `.local/filesystem` only if `/var/lib/openlore/config/lore.json` is absent and published/data state is empty. Create secure `config`, `published`, `data`, and `ssh` directories. If any remote state exists, stop and preserve it; never overwrite or merge remote lore or filesystem content.

Verify valid HTTPS, MCP initialization, authenticated OpenLore SSH, write/read of a unique probe, persistence following container and Droplet restart (including auth and host key), provider administrative shell access, and intended firewall/LB rules. When LB port 22 exists, verify `ssh <domain>` without `-p` and confirm public `2222` is closed.

Return exact HTTPS/MCP, OpenLore SSH, and provider-default administrative SSH reconnect commands, DigitalOcean resource IDs/region, deployed image digest, and any pending standard-port ingress. Only after all required checks pass may the generic deploy skill offer deletion of `.local`; default keep.
