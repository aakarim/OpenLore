# Using OpenLore with Meta Muse

Meta Muse is Meta's personal agent. It runs on its own cloud computer and works
inside the apps you connect to it. Muse has no MCP settings screen; instead you
ask it to build a custom connector, and it writes a small MCP client on its
machine, tests it and saves it as a skill. This guide shows how to
connect Meta Muse to your team's knowledge in OpenLore so it can search and
read the same files as every other agent you connect, over the same restricted
shell commands.

Meta's own terminal agent, Muse Code, speaks MCP natively. It is covered
[at the end](#muse-code).

## Before you start: a server Muse can reach

Muse runs in Meta's cloud, so it cannot reach a server on your laptop. Deploy
OpenLore where it has a public HTTPS address and requires a token on `/mcp`.
The [Production checklist](../../how-to/production-checklist.md) walks through both; the quickest
route is a host such as Render or Fly.io that gives the service HTTPS out of
the box, and [HTTPS in Configure the server](../../how-to/configure-the-server.md#https)
covers a machine of your own. This guide uses `https://lore.example.com` as the
server's address throughout; substitute yours.

Check that the MCP endpoint is up and asking for a credential:

```shell
curl -si -X POST https://lore.example.com/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
```

The response is `401 Unauthorized` with a `WWW-Authenticate` header pointing at
`/.well-known/oauth-protected-resource`. That is correct: nothing reads your
knowledge without an identity.

## Give Muse an identity

Muse's custom connector flow is built around a credential it stores in its
Secure Credentials Store, so the cleanest setup is an identity of Muse's own and
a token for it. Every read Muse makes is then attributed to `muse`, and what
Muse can see is whatever that identity is granted.

From a shell on the server, add the identity and restart the service so the
policy loads:

```shell
openlore identity add --name muse --role user \
  --auth /var/lib/openlore/config/lore.json
```

Then mint a read-only token for it:

```shell
openlore token mint --identity muse --scope read --ttl 720h \
  --config /var/lib/openlore/config/openlore.yml
```

Copy the printed token. It is a password: it expires after the `--ttl` you
chose, and you mint a new one when it does.

## Connect Muse

Ask Muse to build the connector. One message is enough:

```text
Build a custom connector to OpenLore, our team's knowledge server. It is a
remote MCP server over Streamable HTTP at
https://lore.example.com/mcp. Every request needs an
"Authorization: Bearer <token>" header; ask me for the token through the
secure credential flow, not in this chat. The server exposes two tools,
`shell` and `list_commands`. `shell` runs a read-only command against a
virtual filesystem of Markdown. Connect to it, call `list_commands`, then run
`tree -L 2 /` and `grep -ri "retry" /` through `shell`, show me the output,
and save the integration as a reusable skill called "openlore".
```

Muse asks whether the server is reachable over HTTP and what auth it needs; the
message above answers both. When it prompts for the credential, enter the token
in the secure credential prompt rather than pasting it into the conversation.
Meta swaps the real token into requests at the network edge, so Muse's own code
never holds it.

Muse reports the tool list and the output of both commands, then confirms the
skill is saved. Start a fresh conversation and ask something your docs answer,
such as "what does the knowledge repository say about our retry policy?". It
should search with `grep -r` through the `shell` tool and answer from the
result.

If you would rather not hand Muse a token, ask it to use the server's OAuth
sign-in instead. You approve the sign-in in a browser with your passkey, and
Muse then acts as a delegate of your identity rather than as `muse`. The token
route keeps Muse on its own identity, which is easier to scope and audit.

## Tell Muse when to use it

Every OpenLore server prints a ready-made instructions section with the
`agents` command. Muse keeps its own skills and memory, so ask it to fold that
section into the skill it just saved:

```text
Run `agents` through the openlore shell tool. Add its "When to use it",
"Layout" and "Useful Commands" sections to the openlore skill, running every
command through the openlore shell tool rather than over SSH.
```

The section tells Muse when to search the knowledge repository and which
commands to use. See [Any agent over SSH](start-ssh.md) for the other
instruction formats.

## Limit what Muse can see

Muse sees exactly what the `muse` identity is granted. A general `user` role is
a reasonable default; to narrow it, give `muse` a role of its own and grant
that role read-only access to one docset in `lore.json`:

```json
{
  "roles": { "assistant": {} },
  "docsets": {
    "handbook": {
      "paths": ["/handbook"],
      "access": { "allow": { "assistant": "ro" } }
    }
  },
  "identities": [
    { "name": "muse", "roles": ["assistant"] }
  ]
}
```

Restart the server. Ask Muse to run `lore docsets` and it lists `handbook` as
the only docset it can see. Reads show up in the [dashboard](../../concepts/dashboard.md)
under the `muse` identity, so you can see which files it opens and which it
never touches. See [Auth](../../concepts/auth.md) for roles, grants and docsets.

## What Muse can do

Muse reaches OpenLore through the `shell` tool, which runs OpenLore's restricted
shell over the virtual filesystem, not a shell on Muse's computer or yours:
`ls`, `cat`, `grep`, `find`, `head`, `tail` and pipes work, and nothing can
execute on the host. A token minted with `--scope read` can never write, and
writes are only possible at all when the server runs with `readonly: false`
and the identity has an `rw` or `publish` grant. See
[Writing and Publishing](../../concepts/writing.md).

Meta does not review custom connectors, and Muse's connector code runs inside
Muse's own runtime, so connect only servers you operate or trust. If you also
use another agent, connect it to the same server. Every agent then reads the
same files, and an update made once is visible to all of them on their next
read.

## Muse Code

Muse Code, Meta's coding agent for the terminal, declares MCP servers in
`~/.config/muse/settings.json`. It runs on your machine, so a local server
works too:

```shell
openlore ./docs
```

```json
{
  "schema_version": 1,
  "mcp_servers": {
    "openlore": {
      "transport": "streamable_http",
      "url": "http://localhost:8080/mcp",
      "mode": "optional"
    }
  }
}
```

`mode: optional` lets a session start when the server is down; the default
`required` aborts the run instead. Run `/mcp` inside Muse Code to see the
server and its tools. For a deployed server with `mcp.require_auth: true`,
point `url` at it and sign in with a passkey:

```shell
muse mcp login openlore
```

Muse Code reads `AGENTS.md` from your project once you have trusted the
folder, so the usual instruction step applies:

```shell
ssh -p 2222 localhost agents >> AGENTS.md
```
