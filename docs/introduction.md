# What is OpenLore

OpenLore is a minimal, customisable, agent-native knowledge base that keeps your context current and inspectable. You point it at a directory of Markdown, and every agent on your team reads the same files over SSH or MCP.

> **Note:** OpenLore is a single Go binary. There is no vector database, ingestion pipeline or SDK.

Agents use the tools they already know, such as `cat`, `grep`, `find` and pipes, against a virtual filesystem that shows each identity only what it is allowed to see. Knowledge becomes infrastructure: versioned, scoped and governed like any other service.

---

## Serve

You start by serving a directory.

```bash
openlore ./docs
```

SSH listens on port 2222; the web view and the MCP endpoint share port 8080.

```bash
ssh -p 2222 localhost "grep -r 'retry policy' /"
```

Learn more about [ways to use OpenLore](usage.md).

---

## Connect

Any agent that can run a shell command or speak MCP can use OpenLore. There is no client library.

Claude Code connects over MCP.

```bash
claude mcp add --transport http openlore http://localhost:8080/mcp
```

Any agent with a shell connects over SSH. The `agents` command prints the instructions to paste into `AGENTS.md`.

```bash
ssh -p 2222 localhost agents >> AGENTS.md
```

Learn more about connecting [Claude Code](start-claude-code.md) or [any agent over SSH](start-ssh.md).

---

## Scope

Once more than one agent is connected, you will want them to see different things. OpenLore does this with docsets and identities in `lore.json`.

```json
{
  "docsets": {
    "public":  { "paths": ["/public"],  "access": { "allow": { "guest": "ro" } } },
    "backend": { "paths": ["/backend"], "access": { "allow": { "backend": "rw" } } }
  },
  "roles": { "backend": {} },
  "identities": [
    { "name": "backend-agent", "public_key": "ssh-ed25519 AAAA…", "roles": ["backend"] }
  ]
}
```

A docset is a set of paths with an access rule. An identity is an SSH key, passkey or OAuth login with roles. An agent connecting as `backend-agent` sees `/backend`; anyone else sees `/public` and nothing more.

Learn more about [configuration and identity](configuration-and-identity.md).

---

## Govern

OpenLore is read-only by default. When you turn writes on, every write goes through one path that checks who is writing, whether the file passes the folder's [rules](folder-rules.md), and whether the file changed since the agent read it. Stale writes are rejected, not merged.

```bash
echo "# Findings" | publish /backend/research/findings.md
```

`publish` lets an agent contribute into a docset's inbox without being able to edit anything else.

Learn more about [writing and publishing](writing.md).

---

## Observe

The dashboard shows what agents are reading, how large each file is in tokens, and which documents nobody has touched.

Learn more about the [dashboard](dashboard.md).

---

## FAQ

**Why not just keep Markdown in the repo?**

For one agent in one repository, do that. OpenLore is for when several agents, repositories or people need the same knowledge, and you need to say who may read, write or publish what.

**Is the shell a real shell?**

No. Commands are Go functions over the virtual filesystem. There is no `bash`, `exec`, `curl` or process execution in a normal session. Learn more in the [security evaluation](https://github.com/aakarim/OpenLore/blob/main/SECURITY.md).

**Can agents write?**

Only if you enable it, only in docsets they have a grant on, and only through whole-file atomic writes that reject stale content. Learn more about [writing and publishing](writing.md).

---

## Next steps

- [Connect Claude Code](start-claude-code.md) to a served directory over MCP.
- [Connect any agent over SSH](start-ssh.md) and give it an identity.
- Learn how [configuration and identity](configuration-and-identity.md) scope what each agent sees.
