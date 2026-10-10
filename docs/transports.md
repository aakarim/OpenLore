# Transports

OpenLore serves one virtual filesystem over several transports. An agent over
SSH, an MCP client, a script calling the JSON API and a person in an SFTP editor
all reach the same files, run the same restricted shell, and get the same
access for the same identity. Choosing a transport is a question of what the
client can speak, not of what it can see.

For setup steps for each transport, see [Ways to use OpenLore](usage.md).

## One server, several ways in

`openlore ./docs` starts SSH on port 2222 and HTTP on port 8080. Everything
except stdio MCP is served by those two listeners:

| Transport | Where | Typical client |
|---|---|---|
| SSH shell | Port 2222 | Agents with a shell tool, CI jobs, people |
| SFTP | Port 2222 | Editors and SSHFS mounts |
| MCP over Streamable HTTP | `/mcp` on port 8080 | Claude Code, Codex, Cursor, OpenCode and other MCP clients |
| JSON API | `/api` on port 8080 | Scripts and services that want plain HTTP |
| Web front page | `/` on port 8080 | People browsing content or fetching the SSH host key |
| Dashboard | `/dashboard/` on port 8080 | People reviewing analytics and files |
| MCP over stdio | A local `openlore mcp` process | Clients that launch a local command, such as Claude Desktop |

Each transport resolves the caller to a `lore.json` identity before any command
runs, and every session sees only the [docsets](docsets.md) that identity is
granted. [Auth](auth.md) lists which credentials each transport accepts.

MCP over stdio is the exception. `openlore mcp` is a separate local process
that serves a directory or embedded docs directly to the client that launched
it. It does not load `lore.json`, so it has no identities or docsets; use it
for a single local user, and use MCP over HTTP when access needs to be scoped.

## The shell is the interface

SSH, MCP and the JSON API all run commands in the same restricted shell. SFTP
reads and writes files through the same filesystem and write checks without a
shell. The shell is not Bash: commands are Go functions over the virtual
filesystem, with pipes, loops and variables but no process execution. That is why there is
no client library: anything that can send a command string can use OpenLore.
The full command list is in [Commands](commands.md), and `help` or
`list_commands` shows what a particular server allows.

### MCP tools

The MCP server, over HTTP or stdio, exposes two tools:

| Tool | Description |
|---|---|
| `shell` | Execute a command against the virtual filesystem |
| `list_commands` | List commands supported by that server |

### Command results

The `shell` tool returns completed command invocations as normal MCP results,
including when the command exits non-zero. Its structured content keeps
`stdout`, `stderr`, and `exit_code` separate. The existing `output` field and
text content contain stdout followed by stderr for compatibility, without a
synthetic exit-code line. MCP `isError` is reserved for failures of the tool
invocation itself rather than command exit status.

The JSON API uses the same contract and returns HTTP 200 for every completed
command, whatever its exit code:

```json
{
  "output": "...",
  "stdout": "...",
  "stderr": "...",
  "is_error": false,
  "exit_code": 1
}
```

## Sessions and shell state

Transports differ in how long shell state, such as the working directory and
variables, lasts:

| Transport | Shell state |
|---|---|
| SSH | Lasts for the connection |
| MCP over HTTP | Each `shell` call starts a fresh shell. The MCP session ID stays the same across calls. |
| JSON API, `POST /api/shell` | Each request starts a fresh shell |
| JSON API, persistent session | Lasts until the session is closed or idle for 30 minutes |

With MCP over HTTP, the shell's `OPENLORE_SESSION_ID` environment variable is
the MCP session ID, so session-scoped files or command logs can be addressed
from the shell. For example, `cat /sessions/$OPENLORE_SESSION_ID/history` works when
such a history mount is configured.

The JSON API endpoints mirror the MCP tools and add persistent sessions:

| Endpoint | Purpose |
|---|---|
| `POST /api/shell` | Run `{"command": "..."}` in a fresh shell |
| `GET /api/commands` | Same as the `list_commands` tool |
| `POST /api/sessions` | Create a persistent session |
| `POST /api/sessions/{id}/shell` | Run a command in that session |
| `POST /api/sessions/{id}/touch` | Extend the session's idle lifetime |
| `DELETE /api/sessions/{id}` | Close the session |

## Instruction commands

A server can also give agents their instructions. Instruction commands such as
`agents` and `agents-shellm` print guidance for a particular agent type to
stdout rather than existing as files, so you pipe them into an agent or save
them as `AGENTS.md` or a `SKILL.md`:

```bash
ssh <server> agents > AGENTS.md
ssh <server> agents-shellm > .skills/openlore/SKILL.md
```

Run `skills` on the server for the full list of built-in and configured
instruction commands.

## Next steps

- [Ways to use OpenLore](usage.md) sets up each transport, from SSH to stdio
  MCP, embedded binaries and the Go library.
- [Auth](auth.md) explains how each transport's credentials resolve to an
  identity.
- [Docsets](docsets.md) explains what an identity can see once connected.
