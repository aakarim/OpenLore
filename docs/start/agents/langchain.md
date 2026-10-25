# LangChain with OpenLore

Connect a LangChain agent to an OpenLore server, read your team's lore, and let the agent publish a document your team can inspect.

LangChain's `MCPAdapter` turns OpenLore's MCP tools into LangChain tools, so
your agent searches and reads the knowledge repository with the same shell
commands as every other agent you connect.

---

### 1. Install

Start a server with the folder you want to share:

```bash
openlore ./docs
```

The server prints its addresses. Your folder appears at `/`, and the MCP
endpoint is `http://localhost:8080/mcp`.

Install LangChain with the MCP extra and the provider package for your model:

```bash
pip install "langchain[mcp]" langchain-anthropic
```

`pip` installs `langchain`, `fastmcp`, which handles the MCP connection, and
`langchain-anthropic`.

> **Note:** `langchain.mcp` needs `langchain[mcp]` 1.4 or later. LangChain's
> [migration guide](https://docs.langchain.com/oss/python/migrate/langchain-mcp-adapters)
> covers moving from the `langchain-mcp-adapters` package.

---

### 2. Connect LangChain

Open an `MCPAdapter` on the MCP endpoint and list its tools. Save this as
`lore.py`:

```python
import asyncio

from langchain.mcp import MCPAdapter


async def main():
    async with MCPAdapter("http://localhost:8080/mcp") as adapter:
        for tool in await adapter.list_tools():
            print(tool.name)


asyncio.run(main())
```

Run it with `python lore.py`. You should see:

```text
list_commands
shell
```

`shell` runs a command in OpenLore's restricted shell over the virtual
filesystem: `ls`, `cat`, `grep`, `find`, `head`, `tail` and pipes.
`list_commands` lists what this server allows. Both are LangChain tools you
can hand to an agent.

> **Note:** Commands run on the OpenLore server as Go functions over the
> virtual filesystem, so the machine running your Python stays untouched.

---

### 3. Read some lore

Every OpenLore server prints instructions for agents with the `agents`
command: when to search the knowledge repository and which commands to use.
Fetch it over the [JSON API](../../concepts/transports.md) when the agent
starts and pass it to `create_agent` as the system prompt. Replace `lore.py`
with:

```python
import asyncio
import sys

import httpx
from langchain.agents import create_agent
from langchain.mcp import MCPAdapter

OPENLORE = "http://localhost:8080"


def instructions() -> str:
    response = httpx.post(f"{OPENLORE}/api/shell", json={"command": "agents"})
    response.raise_for_status()
    return response.json()["stdout"]


async def main(question: str):
    async with MCPAdapter(f"{OPENLORE}/mcp") as adapter:
        tools = await adapter.list_tools()
        agent = create_agent(
            "anthropic:claude-sonnet-5", tools, system_prompt=instructions()
        )
        result = await agent.ainvoke(
            {"messages": [{"role": "user", "content": question}]}
        )
        print(result["messages"][-1].content)


asyncio.run(main(sys.argv[1]))
```

Set `ANTHROPIC_API_KEY` in your environment, then ask your agent something
your docs answer by passing the question as the script's argument:

```prompt
What does the knowledge repository say about our retry policy?
```

The agent searches with `grep -r` through the `shell` tool and answers from
the result. Add your own instructions after the fetched text as you would in
any other system prompt. See [Any agent over SSH](start-ssh.md) for the other
instruction formats.

---

### 4. Scope what it sees

So far the agent reads as the built-in `guest` role, which sees every file
the server serves. Give the agent an [identity](../../concepts/auth.md) of
its own: every read is then attributed to that identity, and the agent sees
exactly what the identity is granted.

##### Define a role and a docset

Create `lore.json` next to the folder, with a role for the agent and a
[docset](../../concepts/docsets.md) the role can read:

```json
{
  "allow_keyless": true,
  "unknown_identity": "allow",
  "default_cwd": "/handbook",
  "roles": { "assistant": {} },
  "docsets": {
    "handbook": {
      "paths": ["/handbook"],
      "access": { "allow": { "assistant": "ro" } }
    }
  }
}
```

The `assistant` role has read-only access to `handbook`, and every session
starts there.

##### Add the identity

Add an identity for the agent with that role:

```bash
openlore identity add --name langchain --role assistant --auth ./lore.json
```

The command adds `langchain` to `identities` in `lore.json`.

##### Require a token

Point the server at the policy, require a token on the MCP endpoint and turn
on the token issuer in `openlore.yml`:

```yaml
auth_file: ./lore.json
mcp:
  require_auth: true
tokens:
  issuer: http://localhost:8080
  audience: http://localhost:8080
```

Restart the server. `/mcp` and `/api` now answer `401 Unauthorized` until a
request carries a token.

##### Mint a token

Mint a read-only token for the identity and keep it in the environment of
the process that runs the agent:

```bash
export OPENLORE_TOKEN="$(openlore token mint --identity langchain --scope read --ttl 720h --config ./openlore.yml)"
```

The token is a password: keep it out of code and version control, and mint a
new one when it expires after the `--ttl` you chose. `--scope read` caps the
token at reads, whatever the identity is granted.

##### Pass the token to the agent

`MCPAdapter` delegates authentication to FastMCP, so pass the token as the
`auth` of a prebuilt `Client` and hand that client to the adapter. The same
token authenticates the JSON API request for the instructions:

```python
import asyncio
import os
import sys

import httpx
from fastmcp.client import Client
from langchain.agents import create_agent
from langchain.mcp import MCPAdapter

OPENLORE = "http://localhost:8080"
TOKEN = os.environ["OPENLORE_TOKEN"]


def instructions() -> str:
    response = httpx.post(
        f"{OPENLORE}/api/shell",
        json={"command": "agents"},
        headers={"Authorization": f"Bearer {TOKEN}"},
    )
    response.raise_for_status()
    return response.json()["stdout"]


async def main(question: str):
    async with MCPAdapter(Client(f"{OPENLORE}/mcp", auth=TOKEN)) as adapter:
        tools = await adapter.list_tools()
        agent = create_agent(
            "anthropic:claude-sonnet-5", tools, system_prompt=instructions()
        )
        result = await agent.ainvoke(
            {"messages": [{"role": "user", "content": question}]}
        )
        print(result["messages"][-1].content)


asyncio.run(main(sys.argv[1]))
```

Ask your agent what it can see:

```prompt
Run lore docsets through the shell tool and show me the output.
```

It lists `handbook` as the only docset. Reads show up in the
[dashboard](../../concepts/dashboard.md) under the `langchain` identity, so
you can see which files the agent opens and which it never touches.

> **Tip:** For an agent a person runs at a keyboard, pass `auth="oauth"` to
> the `Client` instead of a token. FastMCP opens the browser, you approve with
> your passkey, and the agent acts as a delegate of your identity.

---

### 5. Make a write

A `publish` grant lets the agent add documents to one folder of the docset,
the [inbox](../../how-to/publish-to-inbox.md), and keeps every existing
document as it is. That suits an agent whose work a person reviews.

##### Add an inbox and grant publish

Name the inbox folder on the docset in `lore.json`:

```json
{
  "docsets": {
    "handbook": {
      "paths": ["/handbook"],
      "inbox": "inbox",
      "access": { "allow": { "assistant": "ro" } }
    }
  }
}
```

`inbox` is relative to the docset, so contributions land under
`/handbook/inbox/`. Grant the role `publish` on the docset:

```bash
openlore role grant --role assistant --docset handbook --grant publish --auth ./lore.json
```

The command replaces the role's `ro` grant on `handbook` with `publish`,
which reads the whole docset and writes inside its inbox.

##### Enable writes

OpenLore serves read-only by default. Turn on writes in `openlore.yml`:

```yaml
readonly: false
```

Restart the server. [Writing and publishing](../../concepts/writing.md)
covers how a write is checked before it commits.

##### Mint a full-scope token

Mint a token that carries the identity's full authority:

```bash
export OPENLORE_TOKEN="$(openlore token mint --identity langchain --scope full --ttl 720h --config ./openlore.yml)"
```

The agent's `publish` command now works with the script from step 4
unchanged.

##### Publish a document

Ask your agent to publish. This creates a file on the server:

```prompt
Summarise what the knowledge repository says about our retry policy and publish the summary as /handbook/retry-summary.md.
```

The agent pipes the summary into `publish`, and the server routes the file to
`/handbook/inbox/retry-summary.md`, attributed to `langchain`. A write
anywhere else in the docset is refused with `read-only filesystem`.

Read the published file back with the same token:

```bash
curl -s -X POST http://localhost:8080/api/shell -H "Authorization: Bearer $OPENLORE_TOKEN" -H 'Content-Type: application/json' -d '{"command": "cat /handbook/inbox/retry-summary.md"}'
```

You should see the summary in the response's `stdout`.

Congrats! Your LangChain agent reads your team's lore as an identity of its
own and publishes into an inbox you can review.

---

## Next steps

- [Let an agent publish into an inbox](../../how-to/publish-to-inbox.md) covers reviewing contributions and capping their size.
- [Workload identity federation](../../concepts/auth.md#workload-identity-federation) exchanges a CI or Kubernetes token for an OpenLore one, so a deployed agent signs in with the short-lived token its platform already issues.
- [Production checklist](../../how-to/production-checklist.md) deploys a server over HTTPS for an agent that runs on a LangGraph server or another host.
