# Caddy Reverse Proxy for OpenLore

Forward requests to `/agents` on your existing domain to the OpenLore HTTP front page.

## Setup

1. Start OpenLore with the HTTP server enabled:
   ```shell
   openlore --http-port 8080 ./docs
   ```

2. Update the Caddyfile with your domain and backend port.

3. Run Caddy:
   ```shell
   caddy run --config Caddyfile
   ```

## How it works

- `https://example.com/agents` → OpenLore HTTP front page (port 8080)
- `https://example.com/*` → Your existing backend (port 3000)
- SSH on port 2222 remains separate (agents connect directly via SSH)
