# Using OpenLore with Claude Duet

Claude Duet is Anthropic's proactive agent. You hand it work through
messages, it runs on its own computer in Anthropic's cloud, and it uses the
memory, skills and connectors already on your Claude account. A connector is a
remote MCP server, so there is nothing Duet-specific to configure: this guide
shows how to connect Claude Duet to your team's knowledge in OpenLore by adding
OpenLore as a custom connector on your Claude account. Duet, the Claude app and
Claude Code then all search and read the same files as every other agent you
connect, with the same restricted shell commands.

## Before you start: a server Duet can reach

Custom connectors connect from Anthropic's cloud, whichever Claude surface uses
them, so Duet cannot reach a server on your laptop. Deploy OpenLore where it
has a public HTTPS address, with a token required on `/mcp` and the OAuth
issuer set to that address, which is what the connector's sign-in needs. The
[Production checklist](../../how-to/production-checklist.md) walks through all three; the
quickest route is a host such as Render or Fly.io that gives the service HTTPS
out of the box, and
[HTTPS in Configure the server](../../how-to/configure-the-server.md#https)
covers a machine of your own. Register a passkey for your identity as the
checklist describes; you use it to approve the connector. This guide uses
`https://lore.example.com` as the server's address throughout; substitute
yours.

Check that the MCP endpoint is up and asking for a credential:

```shell
curl -si -X POST https://lore.example.com/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
```

The response is `401 Unauthorized` with a `WWW-Authenticate` header pointing at
`/.well-known/oauth-protected-resource`. That header is how Claude discovers
OpenLore's OAuth endpoints.

## Connect Duet

Add OpenLore to your Claude account once; Duet picks it up from there.

1. Open [Customize → Connectors](https://claude.ai/customize/connectors) in
   the Claude app and select **Add custom connector**. If Claude asks for the
   connector type, choose **Web**.
2. Name it `openlore` and enter `https://lore.example.com/mcp` as
   the MCP server URL, then continue. Claude probes the URL and marks the
   authentication it detects.
3. Keep **Sign in now** and **Use Claude's published identity**. OpenLore reads
   Claude's client metadata document and shows the connection as a verified
   client; it also accepts **Register automatically** if your dialog offers
   only that.
4. Select **Add**. Claude opens OpenLore's sign-in page; approve it with your
   passkey.

On a Team or Enterprise plan an owner adds the connector under **Organization
settings → Connectors** instead, and each member then selects **Connect** next
to it under **Customize → Connectors**.

Try it in a Claude chat first: open the `+` menu, enable `openlore` under
**Connectors**, and ask:

```
Use the openlore shell tool to run `tree -L 2 /` and show me the output.
```

Then message Duet with something your docs answer, such as "use openlore to
find what the knowledge repository says about our retry policy". It should
search with `grep -r` through the `shell` tool and answer from the result.

## Tell Duet when to use it

Duet shares memory with your Claude account, so you tell it once. Every
OpenLore server prints a ready-made instructions section with the `agents`
command; ask Duet to read it and keep the relevant parts:

```
Run `agents` through the openlore shell tool. Remember its "When to use it",
"Layout" and "Useful Commands" sections, and run those commands through the
openlore tool rather than over SSH. Search the knowledge repository before
guessing or asking me about project docs, runbooks or past decisions.
```

The section tells Duet when to search the knowledge repository and which
commands to use. If you use Claude Code too, the same server is already
available there through your account, and `ssh -p 2222 localhost agents >>
AGENTS.md` gives it the file-based version. See [Any agent over
SSH](start-ssh.md) for the other instruction formats.

## Limit what Duet can see

The connector signs in as you, so Duet can never see more than your identity
sees. OpenLore records the client as a delegated identity named after the
client and the domain it signs in from, such as `claude@claude.ai`, with you
as the principal, and write history shows the pair as `principal/actor`.

To keep Duet below your own access, cap the delegate. The **Authorize** page
you approve with your passkey lists every docset and capability your identity
holds, each with a checkbox; untick `drafts` before you select **Authorize**
and the connector cannot reach that docset through you. To change this later,
open the server in a browser, choose **Permission settings** from the identity
menu, and edit the delegate's docsets and capabilities, or disconnect it. Both
routes store the result as `deny_docsets` and `deny_capabilities` on the
delegate entry in `lore.json`.

Ask Duet to run `lore docsets`; `drafts` is absent from the list. Because the
connector belongs to your account, the cap applies in the Claude app and
Claude Code as well. Reads are attributed in the [dashboard](../../concepts/dashboard.md), so
you can see which files Duet opens and which it never touches. See
[Auth](../../concepts/auth.md) for roles, grants and delegates.

On the Claude side, **Customize → Connectors** lets you set an individual
tool's permission to **Blocked**; blocking `shell` would remove the connector's
only way to read, so limit access with docsets instead.

## What Duet can do

Duet reaches OpenLore through the `shell` tool, which runs OpenLore's
restricted shell over the virtual filesystem, not a shell on Duet's computer
or yours: `ls`, `cat`, `grep`, `find`, `head`, `tail` and pipes work, and
nothing can execute on the host. Duet asks before consequential actions, and
Claude prompts for tool approval unless you choose **Always allow**; keep
approvals on until you have seen how Duet uses the server. Writes are only
possible when the server runs with `readonly: false` and the identity has an
`rw` or `publish` grant. See [Writing and Publishing](../../concepts/writing.md).

If you also use another agent, connect it to the same server. Every agent then
reads the same files, and an update made once is visible to all of them on
their next read.
