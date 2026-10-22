# Using OpenLore with Grok Bot

Grok Bot is xAI's persistent agent. Each Bot you create works from a shared
cloud computer, keeps memory and files between conversations, and reaches
tools through plugins that are installed once for your whole account. A custom
MCP server is one of those plugins, so this guide shows how to
connect Grok Bot to your team's knowledge in OpenLore. Every Bot on your
account then searches and reads the same files as every other agent you
connect, with the same restricted shell commands.

## Before you start: a server your Bot can reach

Grok Bot calls MCP servers from xAI's cloud, not from your laptop, and rejects
`localhost` and private-network addresses. Deploy OpenLore where it has a
public HTTPS address, with a token required on `/mcp` and the OAuth issuer set
to that address, which is what Grok Bot's sign-in needs. The
[Production checklist](../../how-to/production-checklist.md) walks through all three; the
quickest route is a host such as Render or Fly.io that gives the service HTTPS
out of the box, and
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
`/.well-known/oauth-protected-resource`. That header is how Grok Bot discovers
OpenLore's OAuth endpoints.

## Connect your Bot

Plugins are installed from the app, not from inside a Bot's conversation, and
an installed plugin is available to every Bot on your account.

1. Open **Connect apps** from the sidebar, then **Your plugins**, and add a
   custom MCP server. The form takes a name, a URL and optional headers.
2. Name it `openlore` and enter `https://lore.example.com/mcp` as
   the URL. Leave the headers empty: the server uses OAuth, so there is no key
   to paste.
3. Save it, then choose **Authenticate** on the new plugin. Grok Bot opens
   OpenLore's sign-in page in your browser; approve it with your passkey. The
   OAuth token stays on xAI's connector backend, and your Bots call tools
   without ever holding it.
4. Back in **Connect apps → Your plugins → Manage plugins and skills**, check
   that `openlore` is listed under **Installed**.

If you cannot find the form, ask a Bot to add the server for you with the
same name and URL; it uses the same mechanism and the plugin lands in the same
list.

Now open a conversation with a Bot, type `@`, pick `openlore`, and ask:

```text
Use the openlore plugin's shell tool to run `tree -L 2 /` and show me the
output.
```

Then ask something your docs answer, such as "use the openlore plugin to find
what the knowledge repository says about our retry policy". It should search
with `grep -r` through the `shell` tool and answer from the result.

## Tell your Bot when to use it

A Bot keeps memory, and skills are a library shared by all your Bots, so you
tell it once. Every OpenLore server prints a ready-made instructions section
with the `agents` command; ask a Bot to turn it into a skill:

```text
Run `agents` through the openlore plugin's shell tool. Save its "When to use
it", "Layout" and "Useful Commands" sections as a skill called "openlore",
with every command run through the openlore plugin rather than over SSH.
Search the knowledge repository before guessing or asking me about project
docs, runbooks or past decisions.
```

The skill now appears when you type `/` in the composer, and any Bot can use
it. The section tells the Bot when to search the knowledge repository and
which commands to use. See [Any agent over SSH](start-ssh.md) for the other
instruction formats.

If a Bot should check the knowledge repository on a schedule, for example to
summarise what changed under `/channel/general` each morning, ask the Bot that
should own the job to create a routine and use **Test run** before enabling it.

## Limit what your Bot can see

Grok Bot signs in as you, so your Bots can never see more than your identity
sees. OpenLore records the client as a delegated identity named after the
client and the domain it signs in from, such as `grok-bot@x.ai`, with you as
the principal, and write history shows the pair as `principal/actor`.

To keep your Bots below your own access, cap the delegate. The **Authorize**
page you approve with your passkey lists every docset and capability your
identity holds, each with a checkbox; untick `drafts` before you select
**Authorize** and the plugin cannot reach that docset through you. To change
this later, open the server in a browser, choose **Permission settings** from
the identity menu, and edit the delegate's docsets and capabilities, or
disconnect it. Both routes store the result as `deny_docsets` and
`deny_capabilities` on the delegate entry in `lore.json`.

Ask a Bot to run `lore docsets`; `drafts` is absent from the list. Because
plugins are account-wide, the cap applies to every Bot you run. Reads are
attributed in the [dashboard](../../concepts/dashboard.md), so you can see which files your
Bots open and which they never touch. See [Auth](../../concepts/auth.md) for roles, grants
and delegates.

## What your Bot can do

Your Bots reach OpenLore through the `shell` tool, which runs OpenLore's
restricted shell over the virtual filesystem, not the terminal on the Bot's
cloud computer or a shell on yours: `ls`, `cat`, `grep`, `find`, `head`,
`tail` and pipes work, and nothing can execute on the host. Writes are only
possible when the server runs with `readonly: false` and the identity has an
`rw` or `publish` grant; keep the default `readonly: true` until a Bot has a
reason to publish, and put write actions behind an approval rule in the Bot's
instructions when you turn it on. See [Writing and Publishing](../../concepts/writing.md).

If you also use another agent, connect it to the same server. Every agent then
reads the same files, and an update made once is visible to all of them on
their next read.
