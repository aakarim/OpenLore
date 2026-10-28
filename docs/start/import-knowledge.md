# Import knowledge

Import your agent's memory, a Git repository or any folder on your machine and keep it synced with your lore server.

The `lore` client copies the folder into the server's [docstore](../concepts/knowledge-gateway.md) and refreshes the copy each time you run `lore sync`. The original stays where it is and stays yours to edit. This page walks through a plain folder; the same flow connects a [Git repository](../how-to/import-git-repository.md), a [Claude Code project](../how-to/import-claude-project.md) or [your own Claude memory](../how-to/import-claude-memory.md).

`lore` installs beside `openlore`; the Homebrew cask and the release archives ship both binaries. Run `lore` with no arguments to confirm it is on your path; it prints its usage line. You also need a [knowledge repository](create-knowledge-repository.md), the folder that holds the server's `openlore.yml`.

---

### 1. Set up your lore server

In the knowledge repository's `openlore.yml`, set the address `lore` connects to and enable the HTTP API it talks to:

```yaml
external_url: https://lore.example.com
api:
  enabled: true
  path: /api
```

Restart the server. For a local server use `http://localhost:8080`. Minting tokens needs the `tokens` block described in [Auth](../concepts/auth.md).

---

### 2. Log in

Run `lore` from your knowledge repository. `lore` finds it by walking up the directory tree, like Git.

```shell
lore login
```

Your browser opens for a passkey login. The token is stored under `~/.config/lore/` and never in the repository. Every later `lore` command runs as this identity, and `lore` remembers this repository as the default for commands you run from other folders.

> **Tip:** On a machine without a browser, mint a token on the server with `openlore token mint --identity <identity>` and run `lore login <server-url> --token "$TOKEN"`. Replace `<identity>` with your identity name from `lore.json` and `<server-url>` with the server's `external_url`.

---

### 3. Add a source

Point `lore` at a folder of notes, keeping only the Markdown files:

```shell
lore source add ~/notes --name notes --include '**/*.md' --exclude 'drafts/**'
```

`lore` prints the source, its file count and the store path it will sync to, and writes the definition to `home/<identity>/sources.yaml` in the knowledge repository:

```yaml
version: "1"
sources:
  - name: notes
    type: folder
    path: ~/notes
    include: ["**/*.md"]
    exclude: ["drafts/**"]
```

Commit `home/<identity>/sources.yaml` to the [knowledge repository](create-knowledge-repository.md). It describes what you sync; it holds no credentials and no file contents. Each person's sources live under their own identity, so a teammate's file never lists yours.

---

### 4. Sync

Push the folder to the server:

```shell
lore sync
```

You should see:

```text
Syncing as alice to https://lore.example.com. Identity-scoped sources:
  notes → folder/alice/notes
┃ Push these sources under this identity?
┃
┃                      Yes     No
←/→ toggle • enter submit • y Yes • n No
```

Press `y`. The question appears once per server and identity on this machine, and the source then reports what it uploaded:

```text
notes: 14 files · 14 new blobs · +14 ~0 -0 · snapshot 3f9c1a2e…
```

Run `lore sync` again after you edit the notes; only changed content is uploaded, and files you deleted disappear from the new snapshot but stay in history.

> **Tip:** `lore status` shows what a sync would change without uploading anything.

---

### 5. Let agents see it

Synced content is held in the docstore until a [docset](../concepts/docsets.md) projects it. Add a projection to `lore.json` and grant the roles that can read it:

```json
{
  "docsets": {
    "notes": {
      "projection": { "store": "folder/alice/notes" },
      "access": { "allow": { "engineers": "ro" } }
    }
  }
}
```

Restart the server. The docset mounts read-only at `/notes`, and everyone who holds the `engineers` [role](../concepts/auth.md) can read it. Leave out `access` and project the store under your home instead to keep it to yourself; [Import your Claude memory](../how-to/import-claude-memory.md) shows that layout.

---

### 6. Read it from an agent

Ask the server for something the notes answer. Set `OPENLORE_SERVER` to your server's hostname first, for example `lore.example.com`, or `localhost` for a local server:

```shell
ssh -p 2222 "$OPENLORE_SERVER" "grep -r 'retry policy' /notes"
```

The matching lines come back from the synced copy. Agents connected over SSH or MCP read `/notes` like any other docset.

Congrats! Your agents can now read knowledge you keep outside OpenLore, and one command keeps it current.

---

## Next steps

- [Import a Claude project](../how-to/import-claude-project.md) to share what Claude Code has learned about a codebase.
- [Import your Claude memory](../how-to/import-claude-memory.md) to carry your personal instructions into OpenLore without sharing them.
- [Import a Git repository](../how-to/import-git-repository.md) to give agents the docs that live beside the code.
- Learn how the [knowledge gateway](../concepts/knowledge-gateway.md) keeps sources, the docstore and docsets apart.
