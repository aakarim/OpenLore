# Claude Code with OpenLore

Give Claude Code a shared knowledge base over MCP.

We are going to serve a docs directory, connect Claude Code to it over MCP, teach Claude when to use it, and limit what it can see.

Before you start, install OpenLore.

```bash
go install github.com/aakarim/go-openlore/cmd/openlore@latest
```

---

### 1. Serve a directory

Pick any folder of Markdown. We will use `./docs`.

```bash
openlore ./docs
```

The server logs SSH on port `2222` and HTTP on port `8080`. The contents of `./docs` appear at `/`, so a file at `./docs/public/retry.md` is `/public/retry.md` to an agent.

Check that the MCP endpoint is up.

```bash
curl -s -X POST localhost:8080/mcp -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
```

The response is an `initialize` result whose `serverInfo.name` is `openlore`.

---

### 2. Add the server to Claude Code

```bash
claude mcp add --transport http openlore http://localhost:8080/mcp
```

Now start Claude Code and ask it something about your docs.

```text
> What does the knowledge base say about our retry policy?
```

Claude runs `grep -r 'retry' /` through the `shell` tool and answers from the result.

> **Note:** Claude gets a restricted shell over a virtual filesystem, not a shell on your machine. It can `cat`, `grep`, `find` and pipe, and nothing else.

---

### 3. Teach Claude the conventions

Every OpenLore server has an `agents` command that prints a ready-made instructions section.

```bash
ssh -p 2222 localhost agents >> CLAUDE.md
```

The section tells Claude when to search the knowledge base and which commands to use.

---

### 4. Limit what Claude can see

Right now anyone who connects sees every file you served. A `lore.json` file scopes the view. Anonymous callers, including Claude over MCP, use the built-in `guest` role, so grant it read access to one docset.

```json
{
  "allow_keyless": true,
  "unknown_identity": "allow",
  "default_cwd": "/public",
  "docsets": {
    "public": {
      "paths": ["/public"],
      "access": { "allow": { "guest": "ro" } }
    }
  }
}
```

Point the server at it in `openlore.yml`, in the directory you run `openlore` from.

```yaml
auth_file: ./lore.json
```

Restart the server. Claude now sees `/public` and nothing else.

##### Verify

```bash
ssh -p 2222 localhost "lore docsets"
```

The output lists `public` as the only docset.

Learn more about [configuration and identity](configuration-and-identity.md).

---

### 5. Require a login

To give Claude its own identity, so that it can read private docsets or publish, turn on OAuth for MCP. Claude Code then opens a browser to sign in with a passkey the next time it connects.

```yaml
auth_file: ./lore.json
mcp:
  require_auth: true
tokens:
  issuer: http://localhost:8080
  audience: http://localhost:8080
```

Learn more about [authenticated OAuth clients](authenticated-oauth-clients.md).

---

## Next steps

- [Connect any agent over SSH](start-ssh.md) so a CI job or another coding agent shares the same server.
- [Let an agent publish into an inbox](publish-to-inbox.md) so Claude can contribute notes without editing anything else.
- Learn how [writing and publishing](writing.md) keep every write atomic and attributed.
