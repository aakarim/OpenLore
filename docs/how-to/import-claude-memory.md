# Import your Claude memory

Sync the instructions and rules Claude Code keeps for you in `~/.claude`, mounted inside your home on the server so no one else can read them.

## Before you begin

- A `lore login` completed against your server. See [Import knowledge](../start/import-knowledge.md).
- A `~/.claude/CLAUDE.md` or `~/.claude/rules/` directory on your machine, which Claude Code creates once you give it personal instructions.
- An identity with a home docset in `lore.json`, such as `/user/<identity>`. [Auth](../concepts/auth.md) explains homes.

---

## 1. Connect your personal files

Run the guided import with `--personal` from any folder; `lore` finds your knowledge repository through your last `lore login`. `--name` sets the prefix of the source names; the default is the current directory's name followed by `-claude`.

```shell
lore agent import claude --personal --name claude
```

`--personal` offers only the group that describes you, already ticked, and leaves every project group out:

```text
Connecting a synced Claude source, not a one-off import.
┃ Choose groups to sync
┃ > [•] Personal instructions · ~/.claude · 2 files

x toggle • ↑ up • ↓ down • / filter • enter submit • ctrl+a select all
```

Press Enter, check the file list, and press `y` at the confirmation:

```text
claude-personal → claude/alice/personal (2 files)
    CLAUDE.md
    rules/style.md

┃ Write these synced source definitions to sources.yaml?
┃
┃                      Yes     No

←/→ toggle • enter submit • y Yes • n No
```

The source lands in `home/<identity>/sources.yaml` in the knowledge repository on your machine, where `<identity>` is the name you logged in with. Settings and anything named like a credential are always left out; the personal group reads only `CLAUDE.md` and `rules/`, so no transcript is ever picked up.

---

## 2. Sync

Push the files to the server:

```shell
lore sync claude-personal
```

You should see:

```text
claude-personal: 2 files · 2 new blobs · +2 ~0 -0 · snapshot 7d3a0b19…
```

The store `claude/alice/personal` exists on the server now, and nothing serves it yet.

---

## 3. Mount it inside your home

Project the store at a path beneath your home docset. A projection with no `access` block is readable only by the identity whose home it sits under, so leave `access` out:

```json
{
  "docsets": {
    "alice-home": { "paths": ["/user/alice"] },
    "alice-claude": {
      "paths": ["/user/alice/claude"],
      "projection": { "store": "claude/alice/personal" }
    }
  },
  "identities": [
    { "name": "alice", "roles": ["alice"], "home": "alice-home" }
  ]
}
```

Run `lore config reload` in an OpenLore session with `lore:config:edit`; no server restart is needed. `/user/alice/claude` appears in your existing home listing and in nobody else's; a teammate with the same roles cannot read that path. Every transport treats it as read-only, so the copy on your machine stays the one you edit.

> **Note:** To share the same store with a team later, add a second projection at a path outside your home with an `access.allow` block, as in [Import a Claude project](import-claude-project.md).

---

## 4. Commit the definition

`home/<identity>/sources.yaml` in the [knowledge repository](../start/create-knowledge-repository.md) on your machine, not on the server, holds one source with no machine-specific path:

```yaml
version: "1"
sources:
  - name: claude-personal
    type: claude
    group: personal
```

Commit it with `lore.json`. On another machine, `lore login` as the same identity followed by `lore sync` refreshes the same store from that machine's `~/.claude`.

---

## Verify

Read the synced instructions from your home over SSH. Set `OPENLORE_SERVER` to your server's hostname first, for example `lore.example.com`, or `localhost` for a local server:

```shell
ssh -p 2222 "$OPENLORE_SERVER" "cat ~/claude/CLAUDE.md"
```

You should see:

```text
# Personal instructions
- Prefer table-driven tests.
- Ask before adding a dependency.
```

The content matches `~/.claude/CLAUDE.md` on your machine, and the same command as any other identity fails with a permission error.

---

## Related

- [Import a Claude project](import-claude-project.md)
- [Auth](../concepts/auth.md)
- [lore CLI reference](../reference/lore-cli.md)
