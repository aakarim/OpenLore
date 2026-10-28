# What is OpenLore

OpenLore is a minimal, customisable, agent-native knowledge repository that keeps your context current and inspectable. You point it at a directory of Markdown, and every agent on your team reads the same files over SSH or MCP.

> **Note:** OpenLore is a single Go binary. There is no vector database, ingestion pipeline or SDK.

Agents use the tools they already know, such as `cat`, `grep`, `find` and pipes, against a virtual filesystem that shows each identity only what it is allowed to see. Knowledge becomes infrastructure: versioned, scoped and governed like any other service.

---

## Serve

You start by serving a directory.

```shell
openlore ./docs
```

SSH listens on port 2222; the web view and the MCP endpoint share port 8080.

```shell
ssh -p 2222 localhost "grep -r 'retry policy' /"
```

Learn more about [ways to use OpenLore](how-to/usage.md).

---

## Connect

Any agent that can run a shell command or speak MCP can use OpenLore. There is no client library.

Claude Code connects over MCP.

```shell
claude mcp add --transport http openlore http://localhost:8080/mcp
```

Any agent with a shell connects over SSH. The `agents` command prints the instructions to paste into `AGENTS.md`.

```shell
ssh -p 2222 localhost agents >> AGENTS.md
```

Learn more about connecting [Claude Code](start/agents/start-claude-code.md) or [any agent over SSH](start/agents/start-ssh.md).

### Link knowledge you already keep elsewhere

Not every piece of context starts life in OpenLore. Coding rules live in a Git repository, Claude Code keeps its instructions and memory next to each project, and plenty of notes sit in plain folders. The knowledge gateway lets you link those sources into OpenLore without moving them: the `lore` client copies the files into the server's docstore, you choose which of them to expose as a read-only docset, and from then on agents see them alongside everything else. The original stays where it is and stays authoritative; run `lore sync` again whenever it changes.

```shell
lore source add ~/notes --name notes
```

One `lore sync` later, the folder is a docset your agents can read. Learn more about the [knowledge gateway](concepts/knowledge-gateway.md) and [importing knowledge](start/import-knowledge.md).

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

Learn more about [docsets](concepts/docsets.md) and [identities and roles](concepts/auth.md).

---

## Govern

OpenLore is read-only by default. When you turn writes on, every write goes through one path that checks who is writing, whether the file passes the folder's [rules](concepts/folder-rules.md), and whether the file changed since the agent read it. Stale writes are rejected, not merged.

```shell
echo "# Findings" | publish /backend/research/findings.md
```

`publish` lets an agent contribute into a docset's inbox without being able to edit anything else.

Learn more about [writing and publishing](concepts/writing.md).

---

## Observe

The dashboard shows what agents are reading, how large each file is in tokens, and which documents nobody has touched.

Learn more about the [dashboard](concepts/dashboard.md).

---

## FAQ

**Why not just keep Markdown in the repo?**

For one agent in one repository, do that. OpenLore is for when several agents, repositories or people need the same knowledge, and you need to say who may read, write or publish what.

**Is the shell a real shell?**

No. Commands are Go functions over the virtual filesystem. There is no `bash`, `exec`, `curl` or process execution in a normal session. Learn more in the [security evaluation](https://github.com/aakarim/OpenLore/blob/main/SECURITY.md).

**Can agents write?**

Only if you enable it, only in docsets they have a grant on, and only through whole-file atomic writes that reject stale content. Learn more about [writing and publishing](concepts/writing.md).

---

## Next steps

- [Connect Claude Code](start/agents/start-claude-code.md) to a served directory over MCP.
- [Connect any agent over SSH](start/agents/start-ssh.md) and give it an identity.
- [Import knowledge](start/import-knowledge.md) from folders, Git repositories and Claude Code with the `lore` client.
- Learn how [identities, roles and docsets](concepts/auth.md) scope what each agent sees.
