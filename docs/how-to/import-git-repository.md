# Import a Git repository

Connect a Git repository so its documentation, decision records or coding rules are readable by agents through OpenLore, and refresh the copy with one command.

## Before you begin

- A `lore login` completed against your server. See [Import knowledge](../start/import-knowledge.md).
- `git` on your machine can already clone the repository. `lore` uses your credential helper or SSH keys and never prompts for credentials.
- Understand how the [knowledge gateway](../concepts/knowledge-gateway.md) turns a synced store into a docset.

---

## 1. Add the repository as a source

From your knowledge repository, add the remote and keep the sync to the files agents need:

```shell
lore source add https://github.com/acme/payments --name payments --branch main --include 'docs/**' --include '*.md'
```

`lore` prints the store path and writes the definition to `home/<identity>/sources.yaml`, where `<identity>` is the name you logged in with:

```text
payments · git · https://github.com/acme/payments → git/github.com/acme/payments
```

HTTPS, `ssh://` and `git@host:org/repo.git` URLs all work. Omit `--branch` to track the remote's default branch. Patterns match repository-relative paths; `**` matches zero or more directories, so `*.md` matches only the root and `**/*.md` matches everywhere, and exclusions win over inclusions. Add `--dry-run` to preview without writing.

> **Caution:** `lore` rejects a URL that contains a password, because `sources.yaml` is committed. Use a credential helper or SSH instead.

---

## 2. Sync

Push the checkout to the server:

```shell
lore sync payments
```

You should see:

```text
Syncing as alice to https://lore.example.com. Identity-scoped sources:
  payments → git/github.com/acme/payments
┃ Push these sources under this identity?
┃
┃                      Yes     No
←/→ toggle • enter submit • y Yes • n No
```

Press `y`. The question appears once per server and identity on this machine, and the source then reports what it uploaded:

```text
payments: 62 files · 62 new blobs · +62 ~0 -0 · snapshot 5e2a9f31…
```

Each sync fetches the branch, checks out its tip in `.local/sources/git/github.com/acme/payments/repo` and uploads only blobs the server has not seen. Run it again after the branch moves and the output reports the delta, such as `+1 ~1 -0`.

> **Note:** The first identity to sync a repository owns its store, and only that identity can push new snapshots to it. Syncs of the same store from two machines build on each other; a snapshot whose parent is stale is rejected, and the next `lore sync` succeeds.

---

## 3. Project it for agents

Synced content is invisible until a docset projects it. Add a projection to `lore.json`, using `subpath` to expose one directory of the repository as the docset root and `access` to name the roles that can read it:

```json
{
  "docsets": {
    "payments-docs": {
      "projection": {
        "store": "git/github.com/acme/payments",
        "subpath": "docs"
      },
      "access": { "allow": { "engineers": "ro" } }
    }
  }
}
```

Run `lore config reload` in an OpenLore session with `lore:config:edit`; no server restart is needed. The docset mounts read-only at `/payments-docs` with the `docs/` prefix stripped, so `docs/adr/0007.md` is served as `/payments-docs/adr/0007.md`. Everyone who holds the `engineers` [role](../concepts/auth.md) can read it, including in existing sessions; a projection takes `access` grants like any other docset.

---

## 4. Commit the definition

`home/<identity>/sources.yaml` now holds the repository:

```yaml
version: "1"
sources:
  - name: payments
    type: git
    url: https://github.com/acme/payments
    branch: main
    include: ["docs/**", "*.md"]
```

Commit it with `lore.json` to the [knowledge repository](../start/create-knowledge-repository.md) and keep `.local/` ignored; it holds the clone.

---

## Verify

List the projected directory through the server. Set `OPENLORE_SERVER` to your server's hostname first, for example `lore.example.com`, or `localhost` for a local server:

```shell
ssh -p 2222 "$OPENLORE_SERVER" "ls /payments-docs/adr"
```

You should see:

```text
0001-record-architecture-decisions.md
0007-webhook-retries.md
```

The listing matches `docs/adr/` on the `main` branch of the repository.

---

## Related

- [Knowledge gateway](../concepts/knowledge-gateway.md)
- [Import a Claude project](import-claude-project.md)
- [lore CLI reference](../reference/lore-cli.md)
