# Import a Claude project

Share the instructions and memory Claude Code keeps for a team project with everyone on it, and keep the copy current with one command.

## Before you begin

- A `lore login` completed against your server. See [Import knowledge](../start/import-knowledge.md).
- A project you have used with Claude Code, so it has a `CLAUDE.md` or a memory directory under `~/.claude/projects/`.
- A [role](../concepts/auth.md) in `lore.json` that the project's teammates hold, such as `payments-team`.
- Understand how the [knowledge gateway](../concepts/knowledge-gateway.md) turns a synced store into a docset.

---

## 1. Connect the project

Change into the project directory, the one you run Claude Code in, and run the guided import from there. `lore` finds your knowledge repository through your last `lore login`, so you can run this from any folder. `lore agent import claude` and `lore source add claude` run the same operation.

```shell
lore agent import claude --project
```

`--project` offers only the groups that belong to this project and ticks the two Claude Code fills on its own: the instructions in the project folder and the auto memory Claude Code keeps for it under `~/.claude/projects/`, in a directory named after the project path. `lore` works that directory out for you:

```text
Connecting a synced Claude source, not a one-off import.
┃ Choose groups to sync
┃ > [•] Project instructions · ~/src/payments · 3 files
┃   [•] Project auto memory · ~/.claude/projects/-Users-alice-src-payments/memory · 1 file
┃   [ ] Session transcripts · ~/.claude/projects/-Users-alice-src-payments · 1 file

x toggle • ↑ up • ↓ down • / filter • enter submit • ctrl+a select all
```

Press Enter to keep the ticked groups. `lore` lists every file in each one and asks once before writing your `sources.yaml`:

```text
payments-claude-instructions → claude/alice/-Users-alice-src-payments/instructions (3 files)
    .claude/rules/testing.md
    AGENTS.md
    CLAUDE.md

payments-claude-memory → claude/alice/-Users-alice-src-payments/memory (1 file)
    MEMORY.md

┃ Write these synced source definitions to sources.yaml?
┃
┃                      Yes     No

←/→ toggle • enter submit • y Yes • n No
```

Press `y`. Each group becomes one source in `home/<identity>/sources.yaml`, where `<identity>` is the name you logged in with. Pass a path after `claude` to import a project other than the current directory, `--name` to replace the `payments-claude` prefix, and `--dry-run` to preview without writing. Esc cancels either prompt and connects nothing.

The groups `--project` offers, and where each syncs to:

- **Project instructions**: `CLAUDE.md`, `.claude/CLAUDE.md`, `.claude/rules/**`, `CLAUDE.local.md` and `AGENTS.md` in the project. Store `claude/<identity>/<project-key>/instructions`.
- **Project auto memory**: everything under `~/.claude/projects/<project-key>/memory/`. Store `claude/<identity>/<project-key>/memory`.
- **Session transcripts**: the `*.jsonl` conversation logs under `~/.claude/projects/<project-key>/`. Store `claude/<identity>/<project-key>/transcripts`. Never ticked by default; press `x` on it to include them.

`<project-key>` is Claude Code's own name for the project directory: the absolute path with every non-alphanumeric character replaced by `-`. Without `--project`, the picker also offers **Personal instructions** from `~/.claude`, which [Import your Claude memory](import-claude-memory.md) covers.

> **Note:** Settings, agent memory, `.env` files and any file whose name looks like a credential, key or token are always excluded, even with a broad `--include`. Transcripts are not: they are off by default because they record whole conversations, and nothing filters their contents, so read them before you tick the group. Filename filtering does not read file contents, so check the list before confirming.

---

## 2. Sync

Push the chosen groups to the server:

```shell
lore sync
```

You should see:

```text
Syncing as alice to https://lore.example.com. Identity-scoped sources:
  payments-claude-instructions → claude/alice/-Users-alice-src-payments/instructions
  payments-claude-memory → claude/alice/-Users-alice-src-payments/memory
┃ Push these sources under this identity?
┃
┃                      Yes     No
←/→ toggle • enter submit • y Yes • n No
```

Press `y`. The question appears once per server and identity on this machine, and each source then reports what it uploaded:

```text
payments-claude-instructions: 3 files · 3 new blobs · +3 ~0 -0 · snapshot 9b1e4c70…
payments-claude-memory: 1 files · 1 new blobs · +1 ~0 -0 · snapshot c47d02a1…
```

Run `lore sync` again whenever Claude Code updates its memory; only new content is uploaded.

---

## 3. Give the team access

Synced content is invisible until a docset projects it. Add one projection per store to `lore.json` and grant the roles your teammates hold, exactly as for any other [docset](../concepts/docsets.md). Edit the file yourself, or ask an agent that already has the knowledge repository open to do it:

```json tab="lore.json"
{
  "docsets": {
    "payments-claude": {
      "projection": {
        "store": "claude/alice/-Users-alice-src-payments/instructions"
      },
      "access": { "allow": { "payments-team": "ro" } }
    },
    "payments-memory": {
      "projection": {
        "store": "claude/alice/-Users-alice-src-payments/memory",
        "include": ["**/*.md"]
      },
      "access": { "allow": { "payments-team": "ro", "platform": "ro" } }
    }
  }
}
```

```prompt tab="Ask your agent"
Run `lore source list` and find the two stores for the payments Claude import. In lore.json, add a docset named payments-claude that projects the instructions store and a docset named payments-memory that projects the memory store with only **/*.md included. Grant payments-team read-only access to both and platform read-only access to payments-memory. Keep every other docset unchanged.
```

Run `lore config reload` in an OpenLore session with `lore:config:edit`; no server restart is needed. The docsets mount read-only at `/payments-claude` and `/payments-memory`, including in existing sessions, and everyone who holds `payments-team` reads both; `platform` reads only the memory. Add a role to `access.allow` to widen the audience and remove it to narrow it, then reload again; a projection never accepts writes, whatever grant you give.

---

## 4. Commit the definitions

`home/<identity>/sources.yaml` now holds the two sources with a portable project path:

```yaml
version: "1"
sources:
  - name: payments-claude-instructions
    type: claude
    group: instructions
    project: ~/src/payments
  - name: payments-claude-memory
    type: claude
    group: memory
    project: ~/src/payments
```

Commit it with `lore.json` to the [knowledge repository](../start/create-knowledge-repository.md). A teammate who wants to contribute their own Claude Code files for the same project runs the same import under their own identity; their sources land in their own `home/<identity>/sources.yaml` and their own `claude/<identity>/…` stores, and a second projection makes those readable too.

---

## Verify

Read the synced memory through the server as a teammate who holds `payments-team`. Set `OPENLORE_SERVER` to your server's hostname first, for example `lore.example.com`, or `localhost` for a local server:

```shell
ssh -p 2222 "$OPENLORE_SERVER" "cat /payments-memory/MEMORY.md"
```

You should see:

```text
# Payments memory
- The retry policy for webhooks is three attempts with exponential backoff.
```

The content matches the file under `~/.claude/projects/<project-key>/memory/` on your machine.

---

## Related

- [Knowledge gateway](../concepts/knowledge-gateway.md)
- [Import your Claude memory](import-claude-memory.md)
- [Import a Git repository](import-git-repository.md)
- [lore CLI reference](../reference/lore-cli.md)
