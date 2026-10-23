# Deploy OpenLore to EC2

Run OpenLore on an AWS EC2 instance with a durable EBS volume and a network load balancer that exposes SSH on port `22`.

With a TCP listener on public port `22`, agents connect with `ssh <domain>` and no port flag. The `deploy-aws` routine carries the full procedure for your coding agent; this page runs it and verifies the result.

## Before you begin

- An OpenLore project that passes local verification. Create one with `ssh openlore.sh setup | claude`.
- A locally authenticated AWS CLI session. Check it with `aws sts get-caller-identity`.

---

## 1. Run the deploy routine

Pipe the routine into your coding agent:

```shell
ssh openlore.sh deploy-aws | claude
```

The agent provisions the EC2 instance, EBS volume and TCP load balancer, deploys your `openlore.yml` separately from the image, then runs its production acceptance checks before it reports done. It asks before creating anything billable.

---

## Verify

From your own machine, connect the way an agent will:

```shell
ssh <domain> whoami
```

You should see:

```
guest
```

Then open `https://<domain>/` in a browser and sign in. If the SSH command asks about an unknown host or cannot connect, the load balancer listener is missing; send the agent the error.

---

## Related

- [Deploy a shared server](../../start/deploy.md) — the components and SSH exposure advice behind these steps.
- [Deploy to DigitalOcean](digitalocean.md) — the equivalent setup on a Droplet with block storage.
- [Auth](../../concepts/auth.md) — scope what each identity sees on the new server.
