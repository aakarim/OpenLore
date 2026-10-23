# Claude Code with OpenLore

Give Claude Code a shared knowledge repository over MCP.

We are going to serve a docs directory, connect Claude Code to it over MCP with its tools allowed, teach Claude when to use it, and limit what it can see.

Before you start, [install OpenLore](../quickstart.md#install).

---

### 1. Serve a directory

Pick any folder of Markdown. We will use `./docs`.

```shell
openlore ./docs
```

The server logs SSH on port `2222` and HTTP on port `8080`. The contents of `./docs` appear at `/`, so a file at `./docs/public/retry.md` is `/public/retry.md` to an agent.

Check that the MCP endpoint is up.

```shell
curl -s -X POST localhost:8080/mcp -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
```

The response is an `initialize` result whose `serverInfo.name` is `openlore`.

---

### 2. Add the server and allow its tools

Register the server, then allow its tools before you ask Claude anything. Claude Code reviews every tool call before it runs, and OpenLore looks like a filesystem: a search such as `grep -r "retry" /` reads as a scan of your local disk for secrets. Without the rule, Manual mode prompts on the first call and auto mode can refuse it with `Permission denied by the Claude Code auto mode classifier`.

```shell
claude mcp add --transport http openlore http://localhost:8080/mcp
```

Add this to `~/.claude/settings.json` for every project, or to `.claude/settings.json` in the project:

```json
{
  "permissions": {
    "allow": ["mcp__openlore"]
  }
}
```

`mcp__openlore` covers every tool the server exposes. Use `mcp__openlore__shell` to allow only the shell. Wildcards such as `mcp__openlore__*` do not match. With the rule in place, Claude Code skips both the prompt and the classifier for OpenLore calls. You can also set it for one session: `claude --allowedTools mcp__openlore`.

> **Note:** A project `.claude/settings.json` only applies after you have accepted the folder-trust prompt that Claude Code shows the first time it starts in that directory. Until then the rule is ignored and calls are still reviewed. `~/.claude/settings.json` always applies.

Now start Claude Code and ask it something about your docs.

```
> What does the knowledge repository say about our retry policy?
```

Claude runs `grep -r 'retry' /` through the `shell` tool and answers from the result.

> **Note:** Claude gets a restricted shell over a virtual filesystem, not a shell on your machine. It can `cat`, `grep`, `find` and pipe, and nothing else.

---

### 3. Teach Claude the conventions

Every OpenLore server has an `agents` command that prints a ready-made instructions section.

```shell
ssh -p 2222 localhost agents >> CLAUDE.md
```

The section tells Claude when to search the knowledge repository, to prefer the MCP tool over `ssh` when the server is connected, and which commands to use. The auto mode classifier reads `CLAUDE.md` too, and it only sees the tool name and the command, never the tool's description, so this is also what tells it that `/` is the documentation root and not your machine. Do this even with the permission rule from step 2 in place.

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

```shell
ssh -p 2222 localhost "lore docsets"
```

The output lists `public` as the only docset.

Learn more about [identities, roles and docsets](../../concepts/auth.md).

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

Learn more about [authenticated OAuth clients](../../reference/authenticated-oauth-clients.md).

---

## Next steps

- [Connect any agent over SSH](start-ssh.md) so a CI job or another coding agent shares the same server.
- [Let an agent publish into an inbox](../../how-to/publish-to-inbox.md) so Claude can contribute notes without editing anything else.
- Learn how [writing and publishing](../../concepts/writing.md) keep every write atomic and attributed.
