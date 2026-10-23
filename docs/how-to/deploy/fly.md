# Deploy OpenLore to Fly.io

Run OpenLore on [Fly.io](https://fly.io/) with a persistent volume and public SSH on port `22`, so agents connect with `ssh <domain>` and no port flag.

Fly.io forwards raw TCP, which most platform load balancers do not. The `deploy-fly` routine carries the full procedure for your coding agent; this page runs it and verifies the result.

## Before you begin

- An OpenLore project that passes local verification. Create one with `ssh openlore.sh setup | claude`.
- A locally authenticated `flyctl` session. Check it with `fly auth whoami`.

---

## 1. Run the deploy routine

Pipe the routine into your coding agent:

```shell
ssh openlore.sh deploy-fly | claude
```

The agent provisions the app, volume, HTTPS and the TCP listener on port `22`, then runs its production acceptance checks before it reports done. It asks before creating anything billable.

---

## Verify

From your own machine, connect the way an agent will:

```shell
ssh <domain> whoami
```

You should see:

```text
guest
```

Then open `https://<domain>/` in a browser and sign in. If the SSH command asks about an unknown host or cannot connect, the TCP forwarding rule is missing; send the agent the error.

---

## Related

- [Deploy a shared server](../../start/deploy.md) — the components and SSH exposure advice behind these steps.
- [Deploy to Render](render.md) — the one-click alternative if public SSH is not needed.
- [Auth](../../concepts/auth.md) — scope what each identity sees on the new server.
