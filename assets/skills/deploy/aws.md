# Deploy OpenLore to AWS

Deliver one production OpenLore server on AWS with durable EBS state,
HTTPS/MCP, authenticated OpenLore SSH, and provider administrative session
access.

## Preconditions and plan

- Operate only at a Git project root containing `openlore.yml`; stop otherwise. Build one authoritative server, with no staging copy.
- Use the locally authenticated AWS CLI/profile and never request pasted AWS credentials or commit credentials/secrets.
- Discover account, region, VPC/subnets, DNS zone, certificates, security groups, load balancers, EC2 instances, IAM roles, and EBS volumes. Normally choose EC2; do not use Beanstalk, Fargate, or another service that cannot provide stable administrative access and persistent mounted state.
- Present the AMI/architecture, instance type, EBS type/size, region/AZ, Elastic IP or load balancers, certificate/DNS, IAM/SSM, ingress, and estimated cost impact. Ask before creating or changing shared infrastructure.
- Preserve resources on partial failure. Never delete, recreate, or reformat an EBS volume automatically.

## Repository and host contract

- Create a root `Containerfile` from an exact pinned semver (`FROM ghcr.io/aakarim/openlore:1.2.3`, not `latest`) and deploy that derivative image, retaining it for future package additions. Do not bake configuration into it.
- Root `openlore.yml` is Git/IaC authoritative and is deployed separately at `/var/lib/openlore/config/openlore.yml`. Set `auth_file: /var/lib/openlore/config/lore.json`, `writable_dir: /var/lib/openlore/published`, `data_dir: /var/lib/openlore/data`, and `host_key_path: /var/lib/openlore/ssh/openlore_ed25519`; use internal SSH `2222` and the chosen HTTP port.
- Keep bootstrap state exclusively in gitignored `.local/lore.json` and `.local/filesystem`.
- Store idempotent scripts/templates and non-secret IDs/configuration under `deploy/aws/`. Discover/tag resources before creating them, show diffs, and commit only with explicit approval. Do not require one particular IaC tool.

## Provision and deploy

- Provision one EC2 instance and one separately identifiable EBS data volume, attach/mount it persistently at `/var/lib/openlore`, deploy root `openlore.yml` independently of the image, and run `./out --config /var/lib/openlore/config/openlore.yml`. Configure the derivative container to restart after host reboot. Use least-privilege IAM and preferably Systems Manager Session Manager for administrative shell; provider-default EC2 SSH is acceptable when already configured. Do not add another public OS SSH daemon for OpenLore.
- Provide HTTPS for the HTTP/MCP port using an ALB with ACM or a simpler host proxy with a valid certificate where appropriate.
- Prefer an NLB TCP listener on public `22` targeting instance port `2222`; do not expose `2222` publicly at the same time. Keep administrative access through SSM or provider-default SSH separately controlled. If an NLB is not justified/feasible, expose `2222` narrowly and report standard-port ingress optional/pending.
- Record instance, volume, target group/load balancer, DNS, region, and immutable image digest metadata without secrets.

## Bootstrap and verify

Use SSM or provider-default admin access to inspect the mounted EBS filesystem. Copy or reconcile root `openlore.yml` to the persistent config path. Upload `.local/lore.json` and `.local/filesystem` only when the remote lore file is absent and published/data state is empty. Create `config`, `published`, `data`, and `ssh` with restrictive ownership. Stop on any mutable existing state; never overwrite or merge remote lore or filesystem content.

Required checks: valid HTTPS; successful MCP initialization; authenticated OpenLore SSH (without `-p` when NLB port 22 exists); write/read a unique probe; restart both the container and instance and read it again; confirm auth and host key persist; open an AWS administrative session; and inspect ingress to ensure `2222` is not public when port 22 mapping exists.

Return exact browser/MCP, OpenLore SSH, and `aws ssm start-session --target <instance-id>` (or provider-default SSH) reconnect commands, resource IDs/region, and deployed image digest. State any pending standard-port ingress. Only after all checks pass may the generic deploy skill offer deleting `.local`; default keep.
