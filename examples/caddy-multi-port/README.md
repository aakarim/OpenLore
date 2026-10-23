# Multi-Port SSH with Caddy

Serve OpenLore SSH on multiple ports for agents behind restrictive firewalls.

## Why?

Many corporate networks block non-standard ports. By also listening on port 443 (HTTPS) and port 22 (standard SSH), you ensure agents can connect regardless of firewall rules.

## Setup

1. Install Caddy with the SSH module:
   ```shell
   xcaddy build --with github.com/mohammed90/caddy-ssh
   ```

2. Start OpenLore:
   ```shell
   openlore --http-port 8080 -p 2222 ./docs
   ```

3. Run Caddy:
   ```shell
   sudo caddy run --config Caddyfile
   ```
   (sudo required for ports 22, 80, 443)

## Connection Options

```shell
# Standard SSH port
ssh docs.example.com

# Port 443 (bypasses most firewalls)
ssh -p 443 docs.example.com

# Direct to OpenLore (if accessible)
ssh -p 2222 docs.example.com

# HTTP front page
curl http://docs.example.com
```

## Agent Configuration

Give agents the most reliable connection option:

```markdown
Connect to docs: `ssh -p 443 docs.example.com "cat /docs/api.md"`
Fallback: `ssh docs.example.com "cat /docs/api.md"`
```
