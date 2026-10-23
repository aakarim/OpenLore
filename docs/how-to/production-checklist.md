# Production checklist

What a shared OpenLore server needs before agents that run in a vendor's cloud,
such as Meta Muse, OpenAI dots, Grok Bot and Claude Duet, can reach it: a
public HTTPS address, a credential on every request, and config, policy and
documents that survive a restart. Work through the list once; each item links
to the page that explains it.

A server on your own laptop needs none of this. It serves one person, and an
agent on the same machine reaches `http://localhost:8080` without a
certificate. The checklist is for the server your whole team, and their
agents, will share.

---

## 1. Pick a host with HTTPS out of the box

Cloud agents only connect to MCP servers over public HTTPS, and OAuth sign-in,
passkeys and the dashboard assume it too. OpenLore has no built-in ACME
client, so the simplest way to get a certificate is to not manage one: run the
container on a platform that terminates TLS for you, gives every service a
`https://` hostname and renews the certificate itself.

- [Render](https://render.com/) — a web service gets `https://<name>.onrender.com`
  automatically, with a managed certificate for custom domains. Only HTTP(S)
  is public, so agents connect over MCP; SSH stays on the private network.
  See [Deploy to Render](deploy/render.md).
- [Fly.io](https://fly.io/) — `https://<app>.fly.dev` plus TCP ingress, so
  agents can also use `ssh <domain>`. See [Deploy to Fly.io](deploy/fly.md).
- [EC2](deploy/ec2.md) and [DigitalOcean](deploy/digitalocean.md) — a load
  balancer in front of the instance terminates TLS; more parts, but the same
  result.

The platform forwards plain HTTP to the container's `http_port`, so no TLS
setting in `openlore.yml` is involved. To serve a machine of your own
instead, put a certificate-fetching proxy such as Caddy in front of it, or
hand OpenLore a certificate with `tls_cert` and `tls_key`; both are in
[HTTPS in Configure the server](configure-the-server.md#https).

## 2. Run a pinned image on a persistent volume

Deploy `ghcr.io/aakarim/openlore:<version>`, not `latest`, so an upgrade is a
change you make rather than one that happens to you. Mount persistent storage
at `/var/lib/openlore` and point `auth_file`, `writable_dir`, `data_dir` and
`host_key_path` under it. Config, policy, documents, passkeys, signing keys
and the SSH host key then outlive any container or machine replacement.

Keep `openlore.yml` in Git and deploy it to
`/var/lib/openlore/config/openlore.yml`; every key is in the
[`openlore.yml` reference](../reference/openlore-yml.md). Back the volume up
on whatever schedule the platform offers. See
[Deploy a shared server](../start/deploy.md) for the full shape of a
deployment.

## 3. Tell OpenLore its public origin

OpenLore builds the links it publishes, the OAuth issuer and the passkey
origin from its configuration, not from the incoming request. Set all of them
to the `https://` origin the platform gave you, or your custom domain:

```yaml
external_url: https://lore.example.com
tokens:
  issuer: https://lore.example.com
  audience: https://lore.example.com
passkeys:
  rp_id: lore.example.com
  rp_origins: ["https://lore.example.com"]
```

Passkeys are bound to their domain, so settle the domain before anyone
registers one; changing it later means registering again.

## 4. Require a credential on `/mcp`

A server on the open internet must not answer anonymous MCP or JSON API
requests. Set `mcp.require_auth: true`; with the `tokens` block above in
place, an unauthenticated request gets `401 Unauthorized` and a
`WWW-Authenticate` header pointing at `/.well-known/oauth-protected-resource`,
which is how OAuth-capable agents discover where to sign in. In `lore.json`,
set `"unknown_identity": "deny"` so an unrecognised SSH key is rejected rather
than resolved to `guest`. See [Authentication posture](../concepts/auth.md#authentication-posture).

## 5. Create identities and register your passkey

Add one identity per person and one per autonomous agent, each with a role
that grants only the docsets it needs. People sign in to OAuth-based agents
and the dashboard with a passkey; register yours by minting a short-lived
token on the server, then asking for a registration link:

```shell
# On the server
openlore token mint --identity alice --ttl 15m --config /var/lib/openlore/config/openlore.yml

# On your machine
curl -s https://lore.example.com/api/shell \
  -H "Authorization: Bearer <token>" -H "Content-Type: application/json" \
  -d '{"command":"passkey register --identity alice --name laptop"}' | jq -r .output
```

Open the printed `/passkey/r/...` link within five minutes. Agents that hold
their own credential, such as Meta Muse, get a long-lived token minted with
`--scope read` for an identity of their own instead. See
[Auth](../concepts/auth.md) for roles, docsets and delegates.

## 6. Decide whether SSH is public

Agents that speak MCP do not need SSH. If shell-based agents will connect from
outside, expose port `2222` through a TCP load balancer on public port `22`,
set `allow_keyless: false`, and publish the host key so clients can verify
it before the first connection. See
[Expose SSH through a load balancer](../start/deploy.md#expose-ssh-through-a-load-balancer).

## 7. Check it

Run these from your own machine against the public address. The first two
should name your `https://` origin; the third must be a `401`:

```shell
curl -s https://lore.example.com/.well-known/oauth-protected-resource
curl -s https://lore.example.com/host-key
curl -si -X POST https://lore.example.com/mcp -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' -d '{}'
```

Then connect one agent and ask it to run `tree -L 2 /`. The read appears in
the [dashboard](../concepts/dashboard.md) attributed to your identity, which is
the point of the whole exercise: every agent on the team now reads the same
files, and you can see what they read.

---

## Next steps

- [Deploy a shared server](../start/deploy.md) for the shape of a deployment.
- [HTTPS in Configure the server](configure-the-server.md#https) if the host does not provide a certificate.
- Connect [Meta Muse](../start/agents/meta-muse.md), [OpenAI dots](../start/agents/openai-dots.md), [Grok Bot](../start/agents/grok-bot.md) or [Claude Duet](../start/agents/claude-duet.md).
