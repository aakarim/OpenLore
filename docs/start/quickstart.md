# Quickstart

Install OpenLore, serve a folder of Markdown, and connect an agent.

## Install

Every installation ships two binaries: `openlore`, the server, and `lore`, the client that signs in to a server and syncs knowledge into it.

### macOS and Linux: Homebrew

```shell
brew install --cask aakarim/tap/lore
```

The cask installs `openlore` and `lore` together.

### Linux release

Download and install the latest x86-64 release:

```shell
curl -L https://github.com/aakarim/OpenLore/releases/latest/download/openlore_linux_amd64.tar.gz | tar xz
sudo install openlore lore /usr/local/bin
```

Arm64 archives are published as `openlore_linux_arm64.tar.gz`.

### Windows release

Download and extract the latest 64-bit release from PowerShell:

```powershell
Invoke-WebRequest `
  -Uri https://github.com/aakarim/OpenLore/releases/latest/download/openlore_windows_amd64.zip `
  -OutFile openlore.zip
Expand-Archive openlore.zip -DestinationPath .
.\openlore.exe version
```

The archive also contains `lore.exe`; keep both on your `PATH`.

### Build from source

See [Building from source](../reference/building-from-source.md). Build the web UI before
the binary. `go install` and a plain `go build` produce a binary without the
dashboard.

### Which installations include the dashboard

| Installation | Dashboard (`/dashboard/`) |
| --- | --- |
| Homebrew, Linux and Windows releases, containers, GitHub Action | Included |
| `make distribution` from source | Included |
| `go install`, `go build` from a clean checkout | Not included; `/dashboard/` returns 404 |

SSH, MCP, the JSON API, and published file links work in every installation.
The startup banner's `Dashboard:` line shows whether your binary includes it.

To run a hosted server instead, see [Deploy a shared server](deploy.md).

## Serve a folder

```shell
openlore ./docs
```

SSH runs on port `2222`. The web view and MCP share port `8080`. The contents
of `./docs` appear at `/`.

## Connect an agent

```shell
# Claude Code
claude mcp add --transport http openlore http://localhost:8080/mcp

# Codex
codex mcp add openlore --url http://localhost:8080/mcp

# Anything with a shell
ssh -p 2222 localhost "grep -r 'retry' /"
```

Claude Code also needs the OpenLore tools allowed before the first call.
Claude Code reviews every tool call, and OpenLore looks like a filesystem, so
a search such as `grep -r "retry" /` can be mistaken for a scan of your local
disk and prompted for or blocked. Allow the server once in
`~/.claude/settings.json` or the project's `.claude/settings.json`:

```json
{
  "permissions": {
    "allow": ["mcp__openlore"]
  }
}
```

Then tell your agents when to use it:

```shell
ssh -p 2222 localhost agents >> AGENTS.md
```

The section also tells Claude that `/` is the documentation root, not your
machine, so keep it in `CLAUDE.md` too. See
[Claude Code with OpenLore](agents/start-claude-code.md#2-add-the-server-and-allow-its-tools)
for the details.

## Next steps

- [Claude Code with OpenLore](agents/start-claude-code.md)
- [Any agent with OpenLore over SSH](agents/start-ssh.md)
- [Ways to use OpenLore](../how-to/usage.md)
