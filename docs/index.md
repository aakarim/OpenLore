# Start here

OpenLore is a shared knowledge repository for teams and their agents. It serves Markdown over SSH and MCP, so agents in different repositories can read the same current context, with identity-scoped permissions and governed writes.

## Set up against your repository

From your existing repository, give the setup instructions to your coding agent:

```shell
ssh openlore.sh setup | amp
```

This fetches instructions, not a remote installer. Your agent interviews you and inspects your repository to create a separate, customer-owned `<team>-lore` deployment project. It configures and verifies a local server before you deploy a shared one. You do not need to hand-write `openlore.yml` first. See [repo setup](how-to/usage.md#set-up-against-an-existing-repo) for the workflow and the other instruction commands.

## What to read next

1. [What is OpenLore](introduction.md) explains when shared knowledge helps and how serving, access and writes fit together.
2. [Ways to use OpenLore](how-to/usage.md) starts with agent-driven repo setup, then covers transports and packaging. For a local trial instead of a deployment project, use the [Quickstart](start/quickstart.md).
3. [Deploy a shared server](start/deploy.md) covers deployment requirements and platform guides after local setup.
4. [Docsets](concepts/docsets.md) and [Auth](concepts/auth.md) explain which knowledge each identity can see.
5. [Writing and publishing](concepts/writing.md) and [Folder rules](concepts/folder-rules.md) explain how agents contribute safely.
6. Use [Commands & CLI](reference/commands.md), the [configuration reference](reference/openlore-yml.md), and the [rules standard library](reference/rules-stdlib.md) for lookup, not as prerequisites for setup.
