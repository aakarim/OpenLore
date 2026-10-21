# Claude Code with OpenLore

Give Claude Code a shared knowledge repository over MCP.

We are going to serve a docs directory, connect Claude Code to it over MCP, allow the OpenLore tools, teach Claude when to use it, and limit what it can see.

Before you start, [install OpenLore](quickstart.md#install).

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
> What does the knowledge repository say about our retry policy?
```

Claude runs `grep -r 'retry' /` through the `shell` tool and answers from the result.

> **Note:** Claude gets a restricted shell over a virtual filesystem, not a shell on your machine. It can `cat`, `grep`, `find` and pipe, and nothing else.

---

### 3. Allow the OpenLore tools

Claude Code reviews every tool call before it runs. In Manual mode it asks you; in auto mode (the default) a classifier decides. The classifier only sees the tool name and the command, not the tool's description, and OpenLore looks like a filesystem. A call such as `grep -r "retry" /` reads as a scan of your local disk for secrets, so Claude can refuse it with `Permission denied by the Claude Code auto mode classifier`. It happens most on the first few searches, before Claude has learned what the server is.

Allow the server once. Add this to `~/.claude/settings.json` for every project, or to `.claude/settings.json` in the project:

```json
{
  "permissions": {
    "allow": ["mcp__openlore"]
  }
}
```

`mcp__openlore` covers every tool the server exposes. Use `mcp__openlore__shell` to allow only the shell. Wildcards such as `mcp__openlore__*` do not match.

With the rule in place, Claude Code skips both the prompt and the classifier for OpenLore calls. You can also set it for one session: `claude --allowedTools mcp__openlore`.

> **Note:** A project `.claude/settings.json` only applies after you have accepted the folder-trust prompt that Claude Code shows the first time it starts in that directory. Until then the rule is ignored and calls are still reviewed.

---

### 4. Teach Claude the conventions

Every OpenLore server has an `agents` command that prints a ready-made instructions section.

```bash
ssh -p 2222 localhost agents >> CLAUDE.md
```

The section tells Claude when to search the knowledge repository and which commands to use. The auto mode classifier reads `CLAUDE.md` too, so this is also what tells it that `/` is the documentation root and not your machine. Do this even with the permission rule from step 3 in place.

---

### 5. Limit what Claude can see

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

Learn more about [identities, roles and docsets](auth.md).

---

### 6. Require a login

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
