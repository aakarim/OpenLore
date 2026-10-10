# Quickstart

Install OpenLore, serve a folder of Markdown, and connect an agent.

## Install

### macOS and Linux: Homebrew

```bash
brew install --cask aakarim/tap/lore
```

### Linux release

Download and install the latest x86-64 release:

```bash
curl -L https://github.com/aakarim/OpenLore/releases/latest/download/openlore_linux_amd64.tar.gz | tar xz
sudo install openlore /usr/local/bin/openlore
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

### Build from source

See [Building from source](building-from-source.md). Build the web UI before
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

To run a hosted server instead, see [Deploy OpenLore to Render](render.md).

## Serve a folder

```bash
openlore ./docs
```

SSH runs on port `2222`. The web view and MCP share port `8080`. The contents
of `./docs` appear at `/`.

## Connect an agent

```bash
# Claude Code
claude mcp add --transport http openlore http://localhost:8080/mcp

# Codex
codex mcp add openlore --url http://localhost:8080/mcp

# Anything with a shell
ssh -p 2222 localhost "grep -r 'retry' /"
```

Tell your agents when to use it:

```bash
ssh -p 2222 localhost agents >> AGENTS.md
```

## Next steps

- [Claude Code with OpenLore](start-claude-code.md)
- [Any agent with OpenLore over SSH](start-ssh.md)
- [Ways to use OpenLore](usage.md)
