# Deploy OpenLore to Microsoft Azure

Deliver one production OpenLore server on Azure with managed-disk persistence, HTTPS/MCP, authenticated OpenLore SSH, and Azure administrative access.

## Preconditions and plan

- Operate only from the Git project root containing `openlore.yml`; stop otherwise. Deploy one authoritative server and no staging server.
- Use the locally authenticated Azure CLI/subscription. Never ask for pasted cloud credentials and never store credentials or secrets in Git.
- Discover subscription, resource groups, region, VNets/subnets, NSGs, DNS, certificates, VMs, managed disks, public IPs, load balancers, and gateways. Normally choose an Azure VM plus managed disk.
- Present VM size/image, disk SKU/size, region, public IP, Application Gateway/load balancer or host TLS choice, NSG changes, DNS, and cost impact. Ask before creating shared or billable resources.
- Preserve resources after partial failure. Never automatically delete, recreate, detach destructively, or format an existing managed disk.

## Repository and deployment contract

- Use a root `Containerfile` derived from an exact pinned semver such as `FROM ghcr.io/aakarim/openlore:1.2.3`, never a floating tag. Deploy the derivative, retain room for future package customization, and do not bake configuration into it.
- Root `openlore.yml` is Git/IaC authoritative and deployed separately at `/var/lib/openlore/config/openlore.yml`. Configure `auth_file: /var/lib/openlore/config/lore.json`, `writable_dir: /var/lib/openlore/published`, `data_dir: /var/lib/openlore/data`, `host_key_path: /var/lib/openlore/ssh/openlore_ed25519`, internal SSH `2222`, and the selected HTTP port.
- Runtime bootstrap state lives only in gitignored `.local/lore.json` and `.local/filesystem`.
- Store idempotent scripts/templates and non-secret IDs under `deploy/azure/`. Discover/tag resources before creation, show diffs, and offer commits only with explicit approval; do not mandate one IaC tool.

## Provision and networking

- Create one VM and one managed data disk, mount it persistently at `/var/lib/openlore`, deploy root `openlore.yml` independently of the image, and run `./out --config /var/lib/openlore/config/openlore.yml`. Configure the derivative container to restart after container/VM reboot.
- Use `az ssh vm` when supported/configured, or the VM image's provider-default administrative SSH access. Do not install an additional public OS SSH daemon merely for OpenLore.
- Use Application Gateway (or an approved valid-TLS host proxy) for HTTPS web/MCP. Raw OpenLore SSH requires a TCP Azure Load Balancer, not Application Gateway: prefer public listener `22` to backend `2222`, and do not expose `2222` publicly too. If TCP port translation is unavailable or not approved, expose `2222` and mark standard-port ingress optional/pending.
- Keep NSGs least-privilege and record VM, disk, IP/gateway/LB, DNS, resource group/region, and immutable image digest metadata without secrets.

## Bootstrap and production verification

Use the provider administrative shell to inspect `/var/lib/openlore`. Copy or reconcile root `openlore.yml` to the persistent config path. Transfer `.local/lore.json` and `.local/filesystem` only when the remote lore file is absent and published/data state is empty. Securely create `config`, `published`, `data`, and `ssh`. Stop if mutable remote state exists; never overwrite or merge lore or filesystem content.

Verify valid HTTPS, MCP initialization, authenticated OpenLore SSH, a unique write/read probe, persistence after container and VM restart (including auth and host key), Azure administrative shell, and NSG/LB exposure. With port 22 mapping, verify `ssh <domain>` without `-p` and confirm public `2222` is closed.

Return exact HTTPS/MCP, OpenLore SSH, and `az ssh vm` (or provider-default SSH) reconnect commands, resource identifiers/region, image digest, and any pending standard-port ingress. Only after every required check passes may the generic deploy skill offer deleting `.local`; default keep.
