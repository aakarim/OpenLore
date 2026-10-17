# Deploy OpenLore to DigitalOcean

Run OpenLore on a DigitalOcean Droplet with block-storage persistence and a load balancer that exposes SSH on port `22`.

With a TCP forwarding rule on public port `22`, agents connect with `ssh <domain>` and no port flag. The `deploy-digitalocean` routine carries the full procedure for your coding agent; this page runs it and verifies the result.

## Before you begin

- An OpenLore project that passes local verification. Create one with `ssh openlore.sh setup | claude`.
- A locally authenticated `doctl` session. Check it with `doctl auth list`.

---

## 1. Run the deploy routine

Pipe the routine into your coding agent:

```bash
ssh openlore.sh deploy-digitalocean | claude
```

The agent provisions the Droplet, block-storage volume and load balancer, deploys your `openlore.yml` separately from the image, then runs its production acceptance checks before it reports done. It asks before creating anything billable.

---

## Verify

From your own machine, connect the way an agent will:

```bash
ssh <domain> whoami
```

You should see:

```text
guest
```

Then open `https://<domain>/` in a browser and sign in. If the SSH command asks about an unknown host or cannot connect, the load balancer forwarding rule is missing; send the agent the error.

---

## Related

- [Deploy a shared server](deploy.md) — the components and SSH exposure advice behind these steps.
- [Deploy to EC2](ec2.md) — the equivalent setup on AWS with an EBS volume.
- [Auth](auth.md) — scope what each identity sees on the new server.
