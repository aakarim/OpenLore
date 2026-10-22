# Using OpenLore with OpenAI dots

A dot is OpenAI's always-on agent in ChatGPT. It works on its own cloud
computer, keeps going between conversations, and uses the plugins installed on
your ChatGPT account; plugin permissions are shared across dots, ChatGPT,
ChatGPT Work and Codex. ChatGPT can add any remote MCP server as a plugin, so
this guide shows how to connect OpenAI dots to your team's knowledge in
OpenLore. Your dot then searches and reads the same files as every other agent
you connect, with the same restricted shell commands.

## Before you start: a server your dot can reach

Your dot runs in OpenAI's cloud, so it cannot reach a server on your laptop.
Deploy OpenLore where it has a public HTTPS address, with a token required on
`/mcp` and the OAuth issuer set to that address, which is what ChatGPT's OAuth
sign-in needs. The [Production checklist](../../how-to/production-checklist.md) walks through
all three; the quickest route is a host such as Render or Fly.io that gives
the service HTTPS out of the box, and
[HTTPS in Configure the server](../../how-to/configure-the-server.md#https)
covers a machine of your own. Register a passkey for your identity as the
checklist describes; you use it to approve the plugin. This guide uses
`https://lore.example.com` as the server's address throughout; substitute
yours.

Check that the MCP endpoint is up and asking for a credential:

```bash
curl -si -X POST https://lore.example.com/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
```

The response is `401 Unauthorized` with a `WWW-Authenticate` header pointing at
`/.well-known/oauth-protected-resource`. That header is how ChatGPT discovers
OpenLore's OAuth endpoints.

## Connect your dot

Dots use plugins, and a plugin is created in ChatGPT on the web, not in the
dot itself.

1. Open [ChatGPT Plugins](https://chatgpt.com/plugins), select the plus
   button, then **Add custom MCP server**. If the option is missing, turn on
   Developer mode in ChatGPT's settings; on a Business workspace an owner may
   need to allow custom MCP servers first.
2. Name it `openlore`. Under **Connection**, enter
   `https://lore.example.com/mcp` as the server URL.
3. Choose **OAuth** as the authentication method, accept the custom server
   notice, and select **Create as a plugin**.
4. Install the plugin and connect it. ChatGPT opens OpenLore's sign-in page;
   approve it with your passkey.

Try it in an ordinary chat before involving your dot. Type `@`, pick
`openlore`, and ask:

```text
Use the openlore app's shell tool to run `tree -L 2 /` and show me the output.
```

Then message your dot with something your docs answer, such as "use the
openlore app to find what the knowledge repository says about our retry
policy". It should search with `grep -r` through the `shell` tool and answer
from the result. The plugin needs no extra step to reach the dot, because a dot
uses the plugins installed on your account.

## Tell your dot when to use it

Your dot keeps memory across conversations, so you tell it once. Every OpenLore
server prints a ready-made instructions section with the `agents` command; ask
your dot to read it and keep the relevant parts:

```text
Run `agents` through the openlore app's shell tool. Remember its "When to use
it", "Layout" and "Useful Commands" sections, and run those commands through
the openlore app rather than over SSH. Search the knowledge repository before
guessing or asking me about project docs, runbooks or past decisions.
```

The section tells your dot when to search the knowledge repository and which
commands to use. See [Any agent over SSH](start-ssh.md) for the other
instruction formats.

## Limit what your dot can see

ChatGPT signs in as you, so your dot can never see more than your identity
sees. OpenLore records the client as a delegated identity such as
`chatgpt@chatgpt.com`, with you as the principal, and write history shows the
pair as `principal/actor`.

To keep your dot below your own access, cap the delegate. The **Authorize**
page you approve with your passkey lists every docset and capability your
identity holds, each with a checkbox; untick `drafts` before you select
**Authorize** and the plugin cannot reach that docset through you. To change
this later, open the server in a browser, choose **Permission settings** from
the identity menu, and edit the delegate's docsets and capabilities, or
disconnect it. Both routes store the result as `deny_docsets` and
`deny_capabilities` on the delegate entry in `lore.json`.

Ask your dot to run `lore docsets`; `drafts` is absent from the list. Reads
are attributed in the [dashboard](../../concepts/dashboard.md), so you can see which files
your dot opens and which it never touches. See [Auth](../../concepts/auth.md) for roles,
grants and delegates.

## What your dot can do

Your dot reaches OpenLore through the `shell` tool, which runs OpenLore's
restricted shell over the virtual filesystem, not a shell on the dot's computer
or yours: `ls`, `cat`, `grep`, `find`, `head`, `tail` and pipes work, and
nothing can execute on the host.

ChatGPT asks you to confirm every tool call it considers a write action, and it
decides that from the tool's `readOnlyHint` annotation. OpenLore marks `shell`
read-only while the server runs with the default `readonly: true`, so reads go
through without a prompt. Once you enable writes with `readonly: false`, every
`shell` call asks for confirmation, which is the right default for an agent
that can now `publish`. Writes still need an identity with an `rw` or `publish`
grant. See [Writing and Publishing](../../concepts/writing.md).

If you also use another agent, connect it to the same server. Every agent then
reads the same files, and an update made once is visible to all of them on
their next read.
