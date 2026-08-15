# MCP Server

OpenLore speaks the [Model Context Protocol](https://modelcontextprotocol.io)
so MCP-aware clients (Claude Desktop, Cowork, and other agents) can browse your
docs without SSH. The MCP server exposes the same filesystem as the SSH shell
through two tools:

- `shell` — run a bash command against the docs filesystem (`ls`, `cat`, `grep`,
  `find`, pipes, loops, and all the usual commands)
- `list_commands` — list every available shell command

## Always-on HTTP endpoint (default)

The MCP-over-HTTP endpoint (Streamable HTTP transport) is **on by default** and
mounted at a path on the HTTP server — so it reuses the same port and TLS as the
front page (and any load balancer rule fronting it):

```bash
openlore ./docs
#   SSH:  ssh -p 2222 localhost
#   HTTP: http://localhost:8080
#   MCP:  http://localhost:8080/mcp
```

Point a Streamable-HTTP MCP client at `http://localhost:8080/mcp` (behind a
TLS-terminating proxy/load balancer this is `https://your-host/mcp`).

Configure it in `openlore.yml`:

```yaml
mcp:
  enabled: true   # on by default; set false to disable
  path: /mcp      # path on the HTTP server
  require_auth: true # force OAuth login instead of anonymous access
```

`require_auth` is optional. When omitted, MCP inherits the SSH posture from
`allow_keyless`; set it to `true` to make OAuth-native clients such as Claude
open the browser login flow even while keyless SSH remains enabled. Set it to
`false` to allow anonymous MCP access even when SSH requires a key.

Or with flags: `--mcp-path /custom` to change the path. The endpoint requires
the HTTP server (`http_port`) to be enabled.

## Plain JSON HTTP API (default)

For clients that prefer simple REST calls over the MCP wire protocol, a plain
JSON HTTP API is mounted on the HTTP server. It is backed by the same MCP server
(requests are routed through the MCP tools) and exposes the same two tools:

```bash
# Run a shell command
curl -X POST http://localhost:8080/api/shell \
  -H 'Content-Type: application/json' \
  -d '{"command": "grep -r auth /docs"}'
# => {"output":"...","is_error":false}

# List available shell commands
curl http://localhost:8080/api/commands
# => {"output":"Available commands:\n  cat\n  ...","is_error":false}
```

Configure it in `openlore.yml`:

```yaml
api:
  enabled: true   # on by default; set false to disable
  path: /api      # path on the HTTP server
```

## Stdio (Claude Desktop, Cowork, etc.)

For clients that launch a local process and talk over stdio, run the dedicated
`mcp` subcommand:

```bash
# Serve the embedded docs over MCP (stdio)
openlore mcp

# Or serve a directory
openlore mcp ./docs

# Restrict which files are exposed
openlore mcp --allowed '*.md,*.txt' --ignore '.git,node_modules' ./docs
```

Point your MCP client at the command. For example, in a `mcpServers` config:

```json
{
  "mcpServers": {
    "openlore": {
      "command": "openlore",
      "args": ["mcp", "./docs"]
    }
  }
}
```

## Package as a desktop extension (.mcpb)

For one-click installation in Claude Desktop, package the binary (with its
embedded docs) as an MCPB extension:

```bash
go build -o openlore ./cmd/openlore
./openlore mcpb -o openlore.mcpb
```

Double-click the resulting `.mcpb` file (or drag it into Claude Desktop) to
install. If the binary has no embedded docs, the user is prompted to select a
docs directory on install. Add `--docs-dir ./docs` to bundle a directory.

## Authenticated access (bearer tokens)

By default the MCP + HTTP API mirror the SSH server's posture: if anonymous SSH
is allowed, tokenless MCP/HTTP callers are served **anonymously** — they land in
the `default` lore, read-only, and cannot see docsets outside it. To act as a
**specific identity** (with that identity's docset access, write capability, and
home), a caller presents a bearer token:

```bash
curl -X POST https://your-host/api/shell \
  -H 'Authorization: Bearer <token>' \
  -H 'Content-Type: application/json' \
  -d '{"command": "ls /"}'
```

The same header works on `/mcp`. A **present but invalid** token (bad signature,
expired, wrong audience) is always rejected, even in keyless mode. Tokens are
short-lived; a refresh token (issued alongside) mints new ones at
`/oauth/token` without re-authenticating.

Token auth is active only when `tokens` is configured in `openlore.yml` (it is
server infrastructure — how this instance mints/verifies tokens — not per-lore
access policy):

```yaml
tokens:
  issuer: https://your-host
  audience: https://your-host
  access_ttl: 30m
  refresh_ttl: 720h
```

Public keys are published at `/.well-known/jwks.json` (ES256), so any resource
server can verify tokens without a shared secret.

## Logging in with a passkey (OAuth)

Tokens are minted through a standard OAuth **authorization-code + PKCE** flow
backed by a passkey (Face ID / Touch ID / security key). This is what a native
client like the Obsidian plugin uses.

**1. Register a passkey for an identity.** Ask your agent (or run in the shell):

```bash
passkey register --identity alice --name "Adil MacBook"
```

The identity (`alice`) must exist in your auth config. This prints a 5-minute
link; open it in your browser and enroll your passkey. Thereafter, logging in
mints tokens **for that identity**.

**2. The OAuth flow.** A client kicks off login by opening:

```
https://your-host/authorize
  ?response_type=code
  &client_id=<your-client>
  &redirect_uri=http://127.0.0.1:<port>/callback
  &state=<random>
  &code_challenge=<base64url(sha256(verifier))>
  &code_challenge_method=S256
  &scope=full
```

OpenLore validates the request and shows the passkey login page. On successful
authentication it redirects the browser back to your `redirect_uri` with an
authorization `code` (and your `state`). The client then exchanges the code:

```bash
curl -X POST https://your-host/oauth/token \
  -d grant_type=authorization_code \
  -d code=<code> \
  -d code_verifier=<verifier> \
  -d redirect_uri=http://127.0.0.1:<port>/callback
# => {"access_token":"...","refresh_token":"...","token_type":"Bearer","expires_in":1800}
```

`redirect_uri` must be a loopback address (`http://127.0.0.1` / `localhost`) or
a custom application scheme (e.g. `obsidian://…`); remote origins are rejected
to prevent open redirects.

## Obsidian plugin setup

The Obsidian plugin talks to `/mcp` (or `/api`) as your identity:

1. In the plugin settings, set the **server URL** to `https://your-host` and the
   **MCP path** to `/mcp` (or `/api` for the JSON API).
2. Click **Log in**. The plugin opens `/authorize` in your browser, runs the
   PKCE flow above, and captures the callback on a local loopback port.
3. After you approve with your passkey, the plugin stores the returned
   `access_token` + `refresh_token` and sends `Authorization: Bearer <token>`
   on every request. It refreshes automatically when the access token expires.

If you only need public docs, skip login — the plugin connects anonymously and
sees the `default` lore (read-only), exactly like a tokenless SSH session.
