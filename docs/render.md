# Deploy OpenLore to Render

Run a shared OpenLore server on [Render](https://render.com/) with one click. You get HTTPS, the web view, MCP at `/mcp` and a persistent disk for your knowledge, policy and keys.

[![Deploy to Render](https://render.com/images/deploy-to-render-button.svg)](https://render.com/deploy?repo=https://github.com/aakarim/OpenLore)

We are going to deploy the server, sign in as the starter `admin` identity with a passkey, and connect an agent.

Before you start, you need a Render account. Persistent disks require a paid instance type, so the Blueprint uses the **Starter** plan with a 1 GB disk. See [Render pricing](https://render.com/pricing).

> **Note:** Render web services accept only HTTP(S) traffic from the internet. Agents connect over MCP or the HTTP API. OpenLore SSH on port `2222` is reachable only from other services on your Render private network. If you need public SSH, use the [`deploy` skill](../README.md#create-a-customised-deployment) with Fly.io or another provider that supports TCP ingress.

---

### 1. Deploy the Blueprint

Click **Deploy to Render** above. Render reads [`render.yaml`](../render.yaml) and shows you one web service called `openlore` with a disk mounted at `/var/lib/openlore`. Pick a name, approve the plan, and click **Deploy Blueprint**.

Render builds [`deploy/render/Dockerfile`](../deploy/render/Dockerfile) on top of the official `ghcr.io/aakarim/openlore` image and starts the service. When the deploy finishes, open the service URL, such as `https://openlore-abcd.onrender.com`. You should see the OpenLore front page.

On the first boot, [`deploy/render/start.sh`](../deploy/render/start.sh) writes two starter files to the disk using the service's public URL:

| File | Contents |
|---|---|
| `/var/lib/openlore/config/openlore.yml` | Server config: HTTP on `$PORT`, MCP and the JSON API require a token, passkeys and token issuer set to your `onrender.com` URL |
| `/var/lib/openlore/config/lore.json` | Policy: no anonymous access, one `admin` identity with a private home at `/user/admin`, and a shared `/channel/general` docset |

The script only creates missing files. Later restarts, redeploys and upgrades never overwrite your config, policy or documents.

---

### 2. Register a passkey for `admin`

The starter `admin` identity has no key yet. Mint a short-lived token on the server, then use it once to create a passkey registration link.

Open your service in the Render Dashboard, go to **Shell**, and run:

```bash
./out token mint --identity admin --ttl 15m \
  --config /var/lib/openlore/config/openlore.yml
```

Copy the printed token. On your own machine, ask OpenLore for a registration link:

```bash
export OPENLORE_URL=https://openlore-abcd.onrender.com
export TOKEN=<token from the Render shell>

curl -s "$OPENLORE_URL/api/shell" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"command":"passkey register --identity admin --name laptop"}' \
  | jq -r .output
```

Open the printed `/passkey/r/...` link within five minutes and create the passkey. You can now browse your knowledge at `$OPENLORE_URL/lore/`.

> **Note:** Treat minted tokens like passwords. The token above expires after 15 minutes; mint a new one whenever you need it.

---

### 3. Connect an agent

Add the server to your agent. It opens a browser so you can sign in with the passkey from step 2.

```bash
# Claude Code: add the server, then run /mcp inside Claude Code to sign in
claude mcp add --transport http openlore https://openlore-abcd.onrender.com/mcp

# Codex
codex mcp add openlore --url https://openlore-abcd.onrender.com/mcp
codex mcp login openlore
```

Ask the agent to run `tree -L 2 /` through OpenLore. It should see `/user/admin` and `/channel/general`.

---

### 4. Add people and agents

Edit `lore.json` to add identities, roles and docsets. From the Render **Shell**:

```bash
./out identity add --name alice --role user \
  --auth /var/lib/openlore/config/lore.json
```

Restart the service from the dashboard so the running server loads the new policy. Then mint an `admin` token as in step 2 and run `passkey register --identity alice --name alice-laptop` to create Alice's registration link. See [Configuration and identity](configuration-and-identity.md) for the full policy model.

---

## Day-two operations

**Change server config.** Edit `/var/lib/openlore/config/openlore.yml` from the Render Shell, then restart the service from the dashboard. Every key is listed in the [openlore.yml reference](openlore-yml.md).

**Use a custom domain.** Add the domain in Render, then update `tokens.issuer`, `tokens.audience`, `passkeys.rp_id` and `passkeys.rp_origins` in `openlore.yml` to the new origin and restart. Passkeys are bound to their domain, so register new ones afterwards. To use a custom domain from the very first boot, set the `OPENLORE_PUBLIC_URL` environment variable (for example `https://lore.example.com`) before the first deploy.

**Upgrade OpenLore.** The service builds from the `latest` image by default and does not redeploy automatically. To upgrade, choose **Manual Deploy → Clear build cache & deploy**. To pin a release, set the `OPENLORE_VERSION` environment variable to a version such as `0.7.3`; Render passes it to the Docker build.

**Back up.** Your config, policy, documents, passkeys, signing keys and SSH host key all live on the disk at `/var/lib/openlore`. Render takes daily disk snapshots; see [Render disks](https://render.com/docs/disks).

> **Note:** Render disables zero-downtime deploys for services with a disk, so expect a short interruption while a deploy restarts the server.

## Troubleshooting

**`/mcp` returns 401.** This is expected without a token. Sign in through your agent, or pass `Authorization: Bearer <token>`.

**The service misbehaves after a config edit.** Check the logs in the Render Dashboard; OpenLore reports each config value it ignores. For more detail, set `debug: true` in `openlore.yml`, restart, and set it back once fixed.

**Start again from scratch.** Delete the files in `/var/lib/openlore/config/` from the Render Shell and restart. The next boot writes fresh starter files. Documents under `/var/lib/openlore/published` are kept.
