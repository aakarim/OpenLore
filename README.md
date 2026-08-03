# 📜 OpenLore

Sponsored by <a href="https://oiya.ai/?utm_source=github&amp;utm_medium=referral&amp;utm_campaign=openlore&amp;utm_content=sponsor_logo"><img src="assets/oiya-logo.svg" alt="Oiya" height="24" align="absmiddle"></a>

**Serve your docs to AI agents over SSH.**

OpenLore is an agent-native, governed knowledge server.

---

## About

AI coding agents already know how to explore files with `ls`, `cat`, `grep`,
`find`, pipes, and shell loops. OpenLore gives them that same interface over
SSH, backed by your documentation instead of a real machine.

```text
Agent ──SSH or MCP──▶ OpenLore ──▶ docs, knowledge, and artifacts
```

It starts as a single-binary, zero-config, read-only documentation server. When
you need a live knowledge base, you can add identity-scoped access, controlled
publishing, atomic writes, validation, and human approval without changing how
agents read or navigate the content.

### Store and retrieve Markdown

Put documentation, runbooks, project context, or agent-authored notes in
ordinary Markdown files. There is no ingestion pipeline: point OpenLore at a
directory and it serves the existing hierarchy directly. Organize documents
with folders, connect them with standard Markdown links, and group them into
docsets when different people or agents need different access. OpenLore is
read-only by default; enable writing when you want agents to create and update
Markdown too.

<video src="assets/demo/v0.3.0/openlore-okf-demo.mp4" controls width="100%"></video>

## Quick Start

The fastest path is to let your agent set up OpenLore:

```bash
# Teach your agent how to install, configure, and bundle OpenLore
ssh openlore.sh teach | your-agent-cli

# Add documentation access instructions to AGENTS.md
ssh openlore.sh agents >> AGENTS.md
```

Or install and run it directly:

```bash
go install github.com/aakarim/go-openlore/cmd/openlore@latest

openlore ./docs

ssh -p 2222 localhost
ssh -p 2222 localhost "grep -r 'authentication' /docs"
```

By default this starts:

- SSH on `localhost:2222`
- the human-facing web view on `http://localhost:8080`
- MCP over HTTP on `http://localhost:8080/mcp`

See [Installation](#installation) for more ways to install and package OpenLore.

## Features

- **Agent-native retrieval** — Agents use the shell tools and composition
  patterns they already understand instead of learning a bespoke retrieval API.
- **One knowledge surface, multiple transports** — Serve the same virtual
  filesystem over SSH, SFTP/SSHFS, MCP, and a human-friendly web view.
- **Live, governed knowledge** — Keep content read-only, allow scoped publishing,
  or enable full writes per docset. Writes are atomic, conflict-aware, and can
  require human approval.
- **Identity-scoped views** — Give each person or agent only the docsets it
  needs, with role-based `ro`, `publish`, and `rw` grants, path aliases, and
  private home directories.
- **Safe by construction** — The shell is an in-memory Go interpreter, not a
  real operating-system shell. There is no shell escape, arbitrary process
  execution, or ambient network access in a normal session.
- **Portable knowledge bundles** — Embed docs into a self-contained binary,
  build cross-platform bundles with the GitHub Action, or package them as a
  desktop MCP extension.
- **Structured knowledge without a new query language** — Inspect frontmatter
  as NDJSON with `lore meta`, query it with `jq`, and validate Google's
  [Open Knowledge Format (OKF)](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md)
  bundles and Agent Skills close to the write path.
- **Extensible policy and processing** — Plugins can add validation, grants,
  read/write middleware, metadata, and post-commit processing while preserving
  the same filesystem interface.

## Use Cases

- **Documentation for coding agents** — Put internal API docs, runbooks, product
  context, and architecture notes behind a familiar, greppable interface.
- **A shared live memory for teams of agents** — Give agents separate or shared
  docsets so they can publish findings, hand off work, and accumulate durable
  context across sessions.
- **Skills sharing** — Publish Agent Skills into shared collections so every
  authorized agent can discover and use the same governed procedures.
- **Governed knowledge contribution** — Let contributors publish into inboxes
  while reserving sensitive paths for approvers and preventing accidental
  overwrites.
- **Remote review of agent artifacts** — Expose reports, logs, screenshots, and
  generated files through the browser or SSH without building a custom artifact
  viewer or granting access to the agent's machine.
- **Identity-specific workspaces** — Mount a private home for each agent plus
  shared team knowledge, all through one server and one authorization model.
- **Portable customer or project knowledge** — Ship a versioned executable with
  the relevant docs embedded, or distribute the same knowledge as an MCPB
  desktop extension.
- **Validated knowledge catalogs** — Enforce frontmatter and bundle conventions,
  inspect metadata cheaply, and stop malformed knowledge at admission time.

## How It Works

OpenLore is built on [Wish](https://github.com/charmbracelet/wish) for SSH
transport. A connection is handled entirely against a virtual filesystem:

1. **Authenticate** — connect keylessly or resolve an SSH key, certificate,
   passkey, or OAuth login to an identity.
2. **Compose a view** — mount only the docsets and paths granted to that identity.
3. **Explore** — run shell commands implemented as pure Go functions over that
   view, or use the equivalent MCP `shell` tool.
4. **Contribute safely** — if writing is enabled, authorize and validate a
   whole-file change before committing it atomically or routing it for approval.

The normal shell cannot invoke `bash`, `exec`, `curl`, or arbitrary host
processes. Embedded documentation is always read-only. Explicitly trusted
identities can be granted narrowly scoped asynchronous processing through the
`spawn` capability.

## Governed Writing

OpenLore is read-only by default. Writable deployments keep a single,
policy-controlled write path for redirects, append, `tee`, `patch`, `sed -i`,
file moves, publishing, and approved external jobs.

```bash
echo "# Research" | publish backend findings.md
cat change.diff | patch /backend/api.md
sed -i 's/old/new/g' /backend/runbook.md
```

Writes are whole-object atomic swaps. Compare-and-swap protection rejects stale
edits by default, docset grants constrain the target, and selected paths can
produce reviewable changesets under `/requests` instead of committing directly.

See [Writing and publishing](docs/writing.md) for user-facing setup and
[Write system internals](docs/write-system.md) for the implementation model.

## Installation

### Install with Go

Requires Go 1.26 or later:

```bash
go install github.com/aakarim/go-openlore/cmd/openlore@latest
```

### Build from source

```bash
git clone https://github.com/aakarim/go-openlore.git
cd go-openlore
go build -o openlore ./cmd/openlore
```

### Embed docs in a binary

Place documentation in `assets/lore/` and build. The resulting binary contains
the docs and serves them read-only at `/docs` when run with no directory
argument:

```bash
go build -o my-docs ./cmd/openlore
```

### Build with the GitHub Action

Produce cross-platform binaries with your docs embedded:

```yaml
- uses: aakarim/openlore@v1
  with:
    docs-dir: ./docs
    config: ./openlore.yml
```

See [Ways to use OpenLore](docs/usage.md) for MCP stdio, MCPB desktop
packaging, SSHFS, and Go library usage.

### HTTP inbox uploads

Configure a docset `inbox` and a role with its `publish` grant, then create a
credential for an existing identity (the server configuration must name
`auth_file` so the CLI can validate it):

```bash
openlore inbox token create --identity alice --label webhook --config openlore.yml
curl -H 'Authorization: Bearer olin_ID_SECRET' -H 'Content-Type: text/markdown' \
  --data-binary @note.md 'https://docs.example.com/inbox/docs?name=note.md'
```

`POST /inbox/{docset}` accepts bearer credentials or an exact-body HMAC using
`X-OpenLore-Token-Id` and `X-OpenLore-Signature`. OAuth access tokens are used
only for `POST/GET /inbox/tokens` and `DELETE /inbox/tokens/{id}`; inbox
credentials are separate and revocable. See
[Configuration and identity](docs/configuration-and-identity.md#http-inbox-credentials).

## Documentation

| Guide | Contents |
|---|---|
| [Ways to use OpenLore](docs/usage.md) | SSH, MCP, web, SSHFS, embedded binaries, GitHub Action, MCPB, and library usage |
| [Command reference](docs/commands.md) | Complete shell, introspection, publishing, syntax, CLI command, and flag reference |
| [Configuration and identity](docs/configuration-and-identity.md) | `openlore.yml`, authentication, roles, docsets, aliases, homes, and host verification |
| [Writing and publishing](docs/writing.md) | Write modes, inboxes, conflict handling, approvals, and jobs |
| [Plugins and knowledge formats](docs/plugins.md) | Plugin interfaces, OKF validation, `lore validate`, `lore meta`, and Agent Skills |
| [Write system internals](docs/write-system.md) | Filesystem layering, write seam, changesets, hooks, and async jobs |
| [Security evaluation](SECURITY.md) | Threat model and security properties |

## Security

- Commands run in a pure-Go interpreter, not through `os/exec`.
- The virtual filesystem cleans paths and enforces docset boundaries.
- Allowed file patterns and ignored directories keep secrets out of the view.
- RBAC controls reads, publishing, writes, approvals, and trusted capabilities.
- The web endpoint can publish the SSH host key over TLS to avoid blind trust on
  first use; SSH user and host certificates are also supported.

See [SECURITY.md](SECURITY.md) for the full security evaluation.

## License

[MIT](LICENSE) — Adil Karim

OpenLore bundles third-party open-source components. Their licenses and required
notices are listed in
[assets/legal/THIRD_PARTY_NOTICES.md](assets/legal/THIRD_PARTY_NOTICES.md), with
full license texts in [assets/legal/licenses/](assets/legal/licenses/). These are
embedded in the binary and served by the running service at `/legal`.
