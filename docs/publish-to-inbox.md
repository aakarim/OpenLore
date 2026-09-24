# Let an agent publish into an inbox

Give an agent a place to contribute notes without letting it edit anything else.

A `publish` grant lets an identity read a whole docset but write only inside one folder of it, the inbox. The agent cannot overwrite existing documents or delete anything, so you can accept contributions from an agent you do not fully trust.

> **Tip:** Use `rw` instead of `publish` when the agent should maintain existing documents. Grants are per role and per docset, so one agent can have `rw` on its own docset and `publish` on a shared one.

---

## 1. Enable writes

OpenLore is read-only until you say otherwise. In `openlore.yml`, turn writes on and point at your `lore.json`.

```yaml
readonly: false
auth_file: ./lore.json
```

---

## 2. Add an inbox to the docset

In `lore.json`, name the inbox folder and grant the agent's role `publish`.

```json
{
  "roles": { "researcher": {} },
  "docsets": {
    "backend": {
      "paths": ["/backend"],
      "inbox": "inbox",
      "access": { "allow": { "researcher": "publish" } }
    }
  },
  "identities": [
    { "name": "research-agent", "public_key": "ssh-ed25519 AAAA…", "roles": ["researcher"] }
  ]
}
```

`inbox` is relative to the docset, so contributions land under `/backend/inbox/`. Restart the server.

---

## 3. Publish a file

Connect as the agent and pipe content into `publish`. The path is the docset name followed by the file name; the server routes it into the inbox.

```bash
echo "# Retry policy findings" | ssh -p 2222 -i ~/.ssh/research_agent localhost publish /backend/retry-findings.md
```

The command prints the path it wrote. Run `publish` with no arguments to list the docsets you can publish to.

```bash
ssh -p 2222 -i ~/.ssh/research_agent localhost publish
```

---

## 4. Check what the agent cannot do

The same identity is refused outside the inbox.

```bash
echo "edit" | ssh -p 2222 -i ~/.ssh/research_agent localhost "tee /backend/api.md"
```

The command reports `read-only filesystem` and `api.md` is unchanged. `rm` inside the inbox is refused too; only `rw` can delete.

---

## Verify

Read the file back as any identity that can see the docset.

```bash
ssh -p 2222 -i ~/.ssh/research_agent localhost "cat /backend/inbox/retry-findings.md"
```

---

## Next steps

- Learn how [writing and publishing](writing.md) handle conflicts when two agents change one file.
- [Cap the size of contributions](folder-rules.md) with a folder rule on the inbox.
- [Accept uploads over HTTP](inbox.md) from systems that cannot run `ssh`.
