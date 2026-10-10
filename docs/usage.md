# Ways to Use OpenLore

Set up each way of serving, packaging and connecting to OpenLore. Every mode
serves the same virtual filesystem; [Transports](transports.md) explains how the
transports relate, what they return and how long shell state lasts.

## Serve a directory over SSH, web, and MCP

```bash
openlore ./docs
```

This starts SSH on port 2222 and HTTP on port 8080. The HTTP server includes the
human-facing front page and the default MCP endpoint at `/mcp`.

```bash
ssh -p 2222 localhost
ssh -p 2222 localhost "find / -name '*.md' | head -20"
ssh -p 2222 localhost "cat /docs/api-reference.md"
```

Use `--allowed '*.md,*.txt'` and `--ignore '.git,node_modules'` to constrain the
served tree from the command line, or configure these rules in `openlore.yml`.
An explicitly loaded `--config` file replaces (rather than merges with) an
embedded `openlore.yml`; otherwise the embedded config takes precedence over
built-in defaults. Command-line flags always win.

## Connect an agent

Add a directory listing or the built-in agent instructions to `AGENTS.md`:

```bash
ssh -p 2222 localhost "tree -L 2 /" >> AGENTS.md
ssh -p 2222 localhost agents >> AGENTS.md
```

You can also give the agent a direct tool instruction:

```markdown
## Documentation access

Connect to the docs server for project documentation:

    ssh -p 2222 docs.internal "cat /api/endpoints.md"

Use `ls`, `cat`, `grep`, `find`, and pipes to explore. Run `help` for the full
command list.
```

Save a server's [instruction commands](transports.md#instruction-commands) as
agent instructions or skills:

```bash
ssh <server> agents > AGENTS.md
ssh <server> agents-shellm > .skills/openlore/SKILL.md
```

See [shellm.md](shellm.md) for Docker caveats and trajectory sharing.

Pipe an instruction command from a public OpenLore server into your coding
agent, for example:

```bash
ssh openlore.sh setup | amp
```

The generated `<team>-lore` repository tracks `openlore.yml`, a thin
`Containerfile` based on a stable OpenLore release, and provider artifacts under
`deploy/`. Initial policy and filesystem state live in gitignored `.local/`
until the first verified deployment initialises its persistent volume. The
deployment copies tracked `openlore.yml` separately into the volume (or projects
it through a facility such as a Kubernetes ConfigMap); it is not baked into the
container image.

## Embed docs in a binary

Place docs in `assets/lore/` and build:

```bash
make dashboard-build   # optional: include the web UI
go build -o my-docs ./cmd/openlore
```

Without `make dashboard-build` the binary has no dashboard. See
[Building from source](building-from-source.md).

The resulting binary contains the docs and serves them at `/docs` when run with
no directory argument. Embedded docs are always read-only.

Extract embedded docs when needed:

```bash
openlore export -o ./extracted-docs
```

## Build with the GitHub Action

```yaml
- uses: aakarim/openlore@v1
  with:
    docs-dir: ./docs
    config: ./openlore.yml
```

The action produces cross-platform binaries containing the selected docs. It
also builds and embeds the dashboard using the repository's Nix-pinned Node
toolchain. The resulting binary does not require Node at runtime.

## MCP over HTTP

The Streamable HTTP MCP endpoint shares the HTTP server and its TLS or reverse
proxy configuration:

```bash
openlore ./docs
# SSH:  ssh -p 2222 localhost
# Web:  http://localhost:8080
# MCP:  http://localhost:8080/mcp
```

Configure it in `openlore.yml`:

```yaml
mcp:
  enabled: true
  path: /mcp
  require_auth: true
```

`require_auth: true` forces OAuth for both MCP over HTTP and the JSON API. See
[Authentication posture](auth.md#authentication-posture) for how it combines
with the SSH posture and what happens when no `tokens` block is configured.
`--mcp-path /custom` changes the MCP path; MCP over HTTP requires the HTTP
server to remain enabled.

The MCP tools, the command result format, session behaviour and the JSON API
endpoints are described in [Transports](transports.md).

## MCP over stdio

Use stdio for clients that launch a local process, including Claude Desktop:

```bash
openlore mcp
openlore mcp ./docs
openlore mcp --allowed '*.md,*.txt' --ignore '.git,node_modules' ./docs
```

Example MCP client configuration:

```json
{
  "mcpServers": {
    "openlore": {
      "command": "openlore",
      "args": ["mcp", "./docs"]
    }
  }
}
```

## Package a desktop extension

Package an embedded binary as an MCPB extension for one-click installation:

```bash
go build -o openlore ./cmd/openlore
./openlore mcpb -o openlore.mcpb
```

If the binary does not contain embedded docs, installation prompts for a docs
directory. Pass `--docs-dir ./docs` to bundle one during packaging.

## Browse and edit with VS Code

An SFTP filesystem extension can open OpenLore's directory tree directly in VS
Code without cloning, mounting, or synchronising it into a local project
folder. Saving an editor writes the individual file back through OpenLore's
governed write path. See [Editing OpenLore Files](editors.md) for VS Code setup,
other compatible editors, save behaviour, and limitations.

## Mount with SSHFS

SFTP also lets local tools mount a read-only view of the virtual filesystem:

```bash
mkdir -p /mnt/docs
sshfs -p 2222 localhost:/ /mnt/docs -o ro

grep -r "API" /mnt/docs/
code /mnt/docs/

fusermount -u /mnt/docs  # Linux
umount /mnt/docs          # macOS
```

## Human-facing web view

The front page is enabled on port 8080 by default:

```bash
openlore ./docs
openlore --http-port 3000 ./docs
openlore --http-port 0 ./docs
```

In addition to browsing content, the page displays the SSH host key and serves
it at `GET /host-key`. See
[Verify the SSH host key over HTTPS](auth.md#verify-the-ssh-host-key-over-https)
before using it as the trust anchor for SSH connections.

## Use OpenLore as a Go library

```go
package main

import (
	"log"

	openlore "github.com/aakarim/go-openlore/pkg/openlore"
)

func main() {
	srv, err := openlore.NewServer("./docs",
		openlore.WithPort(2222),
		openlore.WithHTTPPort(8080),
		openlore.WithAllowedPatterns([]string{"*.md", "*.txt"}),
	)
	if err != nil {
		log.Fatal(err)
	}

	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
```

An MCP-only server can be built against any OpenLore filesystem:

```go
fs := openlore.NewDirFS("./docs", openlore.FilesConfig{
	Allowed: []string{"*.md", "*.txt"},
})

srv := openlore.NewMCPServer(fs,
	openlore.WithMCPServerName("Company Knowledge Repository"),
	openlore.WithMCPInstructions("Use grep and cat to explore the docs."),
)
```

## Next steps

- [Transports](transports.md) explains the MCP tools, command results and
  session behaviour shared by these modes.
- [Claude Code with OpenLore](start-claude-code.md) walks through the MCP
  connection end to end.
- [Auth](auth.md) explains how to give each connection its own identity and
  grants.
- [Commands](commands.md) lists every command an agent can run once connected.
