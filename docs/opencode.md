# Using OpenLore with OpenCode

OpenCode connects to OpenLore as a remote MCP server, so it can search and read a shared knowledge repository with ordinary shell commands. Because the knowledge lives on the OpenLore server rather than in each project, OpenCode and any other agent you connect read the same, current files.

## Connect OpenCode

Start a server with the folder you want to share. Its contents appear at `/`.

```bash
openlore ./docs
```

Add OpenLore to `opencode.json` at the root of your project, or to
`~/.config/opencode/opencode.json` to make it available everywhere:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "openlore": {
      "type": "remote",
      "url": "http://localhost:8080/mcp",
      "enabled": true
    }
  }
}
```

Ask OpenCode something your docs answer, such as "what does the knowledge repository say
about our retry policy?". It should search with `grep -r` through the `shell`
tool and answer from the result.

## Tell OpenCode when to use it

OpenCode reads `AGENTS.md` from your project automatically. Every OpenLore server prints a ready-made section for it:

```bash
ssh -p 2222 localhost agents >> AGENTS.md
```

The section tells OpenCode when to search the knowledge repository and which commands to
use. See [Any agent over SSH](start-ssh.md) for the other instruction formats.

## Limit what OpenCode can see

Without authentication, OpenCode connects as the built-in `guest` role and sees
every file the server serves. To narrow that, define a docset in `lore.json`
and grant `guest` read-only access to it:

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

Point the server at it in `openlore.yml` and restart:

```yaml
auth_file: ./lore.json
```

`ssh -p 2222 localhost "lore docsets"` should now list `public` as the only
docset. See [Auth](auth.md) for
roles, grants and docsets.

## Sign OpenCode in

To give OpenCode its own identity, so it can read private docsets or publish into
an inbox, require OAuth on the MCP endpoint:

```yaml
auth_file: ./lore.json
mcp:
  require_auth: true
tokens:
  issuer: http://localhost:8080
  audience: http://localhost:8080
```

Restart the server, then sign OpenCode in. It opens a browser so you can log in with a passkey:

```bash
opencode mcp auth openlore
```

See [Authenticated OAuth Clients](authenticated-oauth-clients.md) for how
tokens are issued and scoped.

## What OpenCode can do

OpenCode reaches OpenLore through its `shell` tool, which runs OpenLore's
restricted shell over the virtual filesystem, not a shell on your machine:
`ls`, `cat`, `grep`, `find`, `head`, `tail` and pipes work, and nothing can
execute on the host. Writes are only possible when the server runs with
`readonly: false` and the identity has an `rw` or `publish` grant. See
[Writing and Publishing](writing.md).

If you also use another agent, connect it to the same server. Every agent then
reads the same files, and an update made once is visible to all of them on
their next read.
