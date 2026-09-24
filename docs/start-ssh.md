# Any agent with OpenLore over SSH

Connect a coding agent, a CI job or a script to a shared knowledge base with nothing but `ssh`.

We are going to serve a docs directory, connect to it from a shell, give one agent its own identity, and teach the agent when to use the server.

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

---

### 2. Explore from a shell

Any SSH client works, with or without a key.

```bash
ssh -p 2222 localhost "tree -L 2 /"
```

Commands compose the way they do in a normal shell.

```bash
ssh -p 2222 localhost "grep -rl 'retry' / | xargs wc -l"
```

Run `help` to see every command the server supports.

```bash
ssh -p 2222 localhost help
```

> **Note:** The shell is a Go interpreter over a virtual filesystem. `bash`, `curl` and `exec` do not exist, so an agent cannot reach the host machine through it.

---

### 3. Give the agent an identity

Anonymous sessions use the built-in `guest` role. To tell agents apart, and to let one of them write later, map its SSH public key to an identity in `lore.json`.

```json
{
  "allow_keyless": true,
  "unknown_identity": "allow",
  "roles": { "backend": {} },
  "docsets": {
    "public": {
      "paths": ["/public"],
      "access": { "allow": { "guest": "ro", "backend": "ro" } }
    },
    "backend": {
      "paths": ["/backend"],
      "access": { "allow": { "backend": "rw" } }
    }
  },
  "identities": [
    { "name": "backend-agent", "public_key": "ssh-ed25519 AAAA…", "roles": ["backend"] }
  ]
}
```

Point the server at it in `openlore.yml`, in the directory you run `openlore` from, and restart.

```yaml
auth_file: ./lore.json
```

##### Verify

Connect with the agent's key and check who the server thinks you are.

```bash
ssh -p 2222 -i ~/.ssh/backend_agent localhost whoami
```

The output is `backend-agent`. Connect without the key and the same command prints `guest`.

Learn more about [configuration and identity](configuration-and-identity.md).

---

### 4. Teach the agent the conventions

Every OpenLore server has an `agents` command that prints a ready-made instructions section. Append it to the file your agent reads on start.

```bash
ssh -p 2222 localhost agents >> AGENTS.md
```

The section tells the agent when to search the knowledge base and which commands to use. For an agent on another machine, replace `localhost` with the server's hostname.

---

## Next steps

- [Let an agent publish into an inbox](publish-to-inbox.md) so it can contribute without editing existing files.
- [Connect Claude Code](start-claude-code.md) to the same server over MCP.
- Learn how [folder rules](folder-rules.md) cap file size and enforce structure per folder.
