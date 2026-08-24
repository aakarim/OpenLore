# Ways to Use OpenLore

Every mode serves the same virtual filesystem. Choose the transport and
packaging that fit the client.

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

## Connect an agent

Add a directory listing or the built-in agent instructions to `AGENTS.md`:

```bash
ssh -p 2222 localhost "tree -L 2 /" >> AGENTS.md
ssh -p 2222 localhost agents >> AGENTS.md
```

You can also give the agent a direct tool instruction:

```markdown
## Documentation Access

Connect to the docs server for project documentation:

    ssh -p 2222 docs.internal "cat /api/endpoints.md"

Use `ls`, `cat`, `grep`, `find`, and pipes to explore. Run `help` for the full
command list.
```

OpenLore skills output instructions to stdout rather than appearing in the
filesystem. `setup` creates and locally verifies a team deployment project;
`onboarding` adds initial identities and folders; `deploy` provisions and
verifies one authoritative server; and `upgrade` prepares a pinned image
version change. Provider deployment guides cover Fly.io, Railway, AWS, Google
Cloud, Azure, and DigitalOcean. `teach` covers general OpenLore setup and
`agents` emits an `AGENTS.md` snippet. Run `skills` for Agent Skills management
instructions and a list of built-in and configured runtime instruction
commands.

Pipe an instruction command from a public OpenLore server into your coding
agent, for example:

```bash
ssh openlore.sh setup | amp
```

The generated `<team>-lore` repository tracks `openlore.yml`, a thin
`Containerfile` based on a stable OpenLore release, and provider artifacts under
`deploy/`. Initial policy and filesystem state live in gitignored `.local/`
until the first verified deployment initializes its persistent volume.

## Embed docs in a binary

Place docs in `assets/lore/` and build:

```bash
go build -o my-docs ./cmd/openlore
```

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

The action produces cross-platform binaries containing the selected docs.

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

`require_auth: true` forces OAuth login for MCP while retaining the separately
configured SSH posture. `false` permits anonymous MCP. If omitted, MCP inherits
the keyless posture. `--mcp-path /custom` changes the path; MCP over HTTP
requires the HTTP server to remain enabled.

The MCP server exposes:

| Tool | Description |
|---|---|
| `shell` | Execute a command against the virtual filesystem |
| `list_commands` | List commands supported by that server |

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

## Mount with SSHFS

SFTP support lets editors and local tools mount the virtual filesystem:

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

In addition to browsing content, the page displays the SSH host key and provides
it at `GET /host-key`. Serve this endpoint over TLS when using it as the trust
anchor for an SSH connection.

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
	openlore.WithMCPServerName("Company Knowledge Base"),
	openlore.WithMCPInstructions("Use grep and cat to explore the docs."),
)
```
