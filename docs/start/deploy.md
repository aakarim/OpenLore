# Deploy a shared server

Run one OpenLore server that every agent on your team reaches over SSH and MCP.

A server on your own machine serves one person. A deployed server gives every agent, on every machine, the same current files — and gives you a record of what they did with them.

> **Note:** OpenLore is one binary and one directory of Markdown. You deploy a container image, a config file and a volume. There is no database cluster and no sidecar.

---

## What you deploy

**The image.** Start from a pinned release: `ghcr.io/aakarim/openlore:<version>`. If you add anything to the image, build a small derivative `Containerfile` that begins `FROM ghcr.io/aakarim/openlore:<version>` and keep configuration out of it.

**The volume.** Mount persistent storage at `/var/lib/openlore`. Your config, the policy file, the served filesystem, the analytics store and the SSH host key all live there, so a container or machine replacement loses nothing.

**The config.** Keep `openlore.yml` in Git and deploy it separately to `/var/lib/openlore/config/openlore.yml`. Point `auth_file`, `writable_dir`, `data_dir` and `host_key_path` at the volume. Every key is listed in the [`openlore.yml` reference](../reference/openlore-yml.md).

### Why SSH and MCP earn their keep

The web view is for people. Agents use the two other transports: MCP for clients that speak it, and SSH for anything with a shell. Every connection carries an identity, so the server can attribute each read and write.

That attribution is the payoff of deploying. The [dashboard](../concepts/dashboard.md) aggregates it per identity and per folder, so you can see which documents agents open every day and which nobody touches. Documents that are read earn their upkeep; documents that are never read are candidates to cut or rewrite. You are measuring what context is valuable instead of guessing.

Learn more about [the dashboard and metrics](../concepts/dashboard.md).

---

## Requirements

A deployment needs all of the following. The platform guides below set them up for you.

- A machine or service with administrative shell access.
- The pinned image running at a recorded immutable digest.
- Valid HTTPS for the web view, the MCP endpoint and OAuth discovery.
- `mcp.require_auth: true`, so unauthenticated MCP requests get a 401 challenge.
- Authenticated OpenLore SSH.
- A persistent volume at `/var/lib/openlore`, with `auth_file`, `writable_dir`, `data_dir` and `host_key_path` under it.
- Config, policy, documents and host key surviving a container and machine restart.

Learn more about [identity and access policy](../concepts/auth.md).

---

## Expose SSH through a load balancer

Raw SSH has no hostname or SNI routing, so an HTTP load balancer cannot route it by domain. Expose it with a TCP load balancer instead: listen on public port `22` and forward to the server's port `2222`. Agents then connect with `ssh <domain>` and no port flag. Do not also expose port `2222` publicly.

When a public port `22` listener is not possible, expose port `2222` and tell agents to connect with `ssh -p 2222 <domain>`.

Learn more about [configuring the server](../how-to/configure-the-server.md).

---

## Set the host public key for verification

The server generates an SSH host key on first start. Set `host_key_path` under the persistent volume so the same key survives redeploys:

```yaml
# openlore.yml
host_key_path: /var/lib/openlore/ssh/openlore_ed25519
```

Agents verify the server against this key. Publish it so they can check it before the first connection: the server serves the public key at `https://<domain>/host-key`, and a client appends it to `known_hosts` once.

```shell
echo "<domain> $(curl -s https://<domain>/host-key)" >> ~/.ssh/known_hosts
```

The next `ssh <domain>` connects without a host-key prompt.

> **Caution:** Rotating the host key changes the server's fingerprint. Every client sees a host-key warning until it removes the old line from `known_hosts` and adds the new key.

---

## Pick a platform

Four maintained guides cover common destinations. Each one runs the matching deploy routine through your coding agent and verifies the result.

- [Deploy to Render](../how-to/deploy/render.md) — a one-click blueprint with HTTPS and a persistent disk. SSH stays on the private network.
- [Deploy to Fly.io](../how-to/deploy/fly.md) — a small fleet with a volume and TCP forwarding, so agents get `ssh <domain>`.
- [Deploy to EC2](../how-to/deploy/ec2.md) — an AWS instance with an EBS volume and a network load balancer on port `22`.
- [Deploy to DigitalOcean](../how-to/deploy/digitalocean.md) — a Droplet with block storage and a load balancer in front.

---

## Next steps

- [Production checklist](../how-to/production-checklist.md) for what the server needs before cloud agents can reach it.
- [Deploy to Render](../how-to/deploy/render.md) for the fastest path from zero to a shared server.
- [Configure the server](../how-to/configure-the-server.md) once it is running.
- Learn how [auth](../concepts/auth.md) scopes what each identity sees.
