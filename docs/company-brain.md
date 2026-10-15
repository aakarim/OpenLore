# Build your company brain on OpenLore

Make OpenLore the knowledge layer your agents share: connect the sources your company already has, give every agent a scoped view of them, and capture what people and agents learn back into the same tree.

In this recipe you can:

- Decide what to build yourself and what OpenLore already solves: transports, identity, permissions, governed writes, provenance and observability.
- Lay the brain out as docsets, so a new source or a new team is a folder and a grant, not a new schema.
- Connect data sources with tools you already have: `tee` over SSH from a script or CI job, an HTTP upload from a webhook, or a directory something else keeps in sync.
- Give agents the brain over MCP or SSH, scoped per identity, with context budgets you can see.
- Capture knowledge from across the organisation through inboxes, home folders, Obsidian and agent session hooks, with every write attributed.

The end result is one server, one tree and one `lore.json`. Sources land under `/sources`, teams work under `/teams/<name>`, shared material lives in `/handbook`, and every agent and person sees exactly the part they were granted.

---

## Why build on OpenLore rather than from scratch

Most company brains start as a script that pulls a few systems into a store an agent can query. The first version ships in a week. What takes the months is everything after: a second team wants in, a second agent starts writing, someone asks who changed a document and why, and security asks which agent can see the sales data. Each of those answers is infrastructure, and none of it is specific to your company.

OpenLore ships that infrastructure as a single Go binary. You keep the part that is yours: which sources matter, how the tree is laid out, and what each agent is for.

**The plumbing is done.** One server speaks SSH, SFTP, MCP over HTTP and stdio, and a JSON API, all against the same virtual filesystem ([Transports](transports.md)). Agents use `cat`, `grep`, `find` and pipes, in a restricted shell that is Go functions over the tree rather than a process on the host ([security evaluation](https://github.com/aakarim/OpenLore/blob/main/SECURITY.md)). Writes are whole-file atomic swaps ([Writing and publishing](writing.md)), a journal records who wrote what ([`history`](commands.md#history---principal-name---actor-name)), and the [dashboard](dashboard.md) shows what is read, what nobody touches, and how many tokens each file costs. There is no vector database, ingestion pipeline or SDK to run or keep up to date.

**The hard parts of growing it are solved.** A brain that works for one agent breaks in predictable ways when it grows. Two agents edit the same file and one overwrites the other: compare-and-swap rejects the stale write instead ([Conflict handling](writing.md#conflict-handling)). Documents swell until they no longer fit in a context window: [folder rules](folder-rules.md) cap files by lines, kilobytes or tokens, and `max: initial` growth limits freeze a file near the size it had. A third audience wants different access: [docsets](docsets.md) let one server and one tree serve every audience instead of a server per team or copies that drift. You find out which files cost the most context from the dashboard, not from a surprise bill.

**The structure is not tied to one kind of data.** A hand-built brain usually ends up shaped like its first source: the schema, the index and the retrieval all assume it, and the second source means a migration. OpenLore's unit is a file in a folder. Markdown, JSON Lines transcripts, YAML, CSV and images are all served ([`files.allowed`](openlore-yml.md#filesallowed)), policy is decided by path, and adding a source is adding a folder and a grant. Where you want structure, add it per docset: [Open Knowledge Format](plugins.md#open-knowledge-format-validation) bundles with an index and a log, frontmatter you can query with [`lore meta`](commands.md#lore-meta-path), and validators or metadata extenders as [plugins](plugins.md).

**Security, permissions and identity are handled.** One identity model covers SSH keys, SSH certificates, passkeys, OAuth logins and [workload identity federation](auth.md#workload-identity-federation) for CI jobs that should never hold a long-lived secret. Roles map to docsets with `ro`, `publish` or `rw`, and a docset an identity has no grant on is not listed, not searchable and not reachable by path. Grants only ever narrow, the same policy answers on every transport, and the server refuses to start on an ambiguous configuration rather than guess ([Auth](auth.md)). Every write is attributed to the identity, or to `person/agent` when an agent acts through an OAuth client, so provenance is on the record from the first day.

---

## Before you begin

- A running server with writes enabled (`readonly: false`) and `auth_file` pointing at a `lore.json`. [Any agent over SSH](start-ssh.md) and [Configure the server](configure-the-server.md) cover both.
- An SSH key for yourself and one for each sync job you plan to run. Jobs get their own identity so the journal can tell them apart.
- Pick the first two or three sources and the first team. The layout below grows from there without being rebuilt.
- Optional: Claude Code or another MCP client to connect in step 3. See [Claude Code with OpenLore](start-claude-code.md).

---

## 1. Lay out the brain as docsets

Three kinds of folder cover most companies: sources that machines write and everyone reads, team areas that a team and its agents own, and shared material such as a handbook. Each is a docset in `lore.json`, and each docset carries its own access, inbox and rules.

```diagram
/
├── handbook/              docset: handbook   staff ro
├── sources/               docset: sources    ingest rw, everyone else ro
│   ├── crm/
│   └── support/inbox/     HTTP uploads land here
└── teams/
    └── platform/          docset: platform   platform rw, staff publish
        ├── index.md
        ├── runbooks/
        └── inbox/         contributions from outside the team
```

```json
{
  "roles": { "staff": {}, "platform": {}, "ingest": {} },
  "docsets": {
    "handbook": {
      "paths": ["/handbook"],
      "access": { "allow": { "staff": "ro", "platform": "ro" } }
    },
    "sources": {
      "paths": ["/sources"],
      "inbox": "support/inbox",
      "access": { "allow": { "ingest": "rw", "staff": "ro", "platform": "ro" } }
    },
    "platform": {
      "paths": ["/teams/platform"],
      "inbox": "inbox",
      "access": { "allow": { "platform": "rw", "staff": "publish" } },
      "rules": {
        "doc-size": { "match": ["**/*.md"], "use": "size/tokens", "with": { "max": 4000 } }
      }
    }
  },
  "identities": [
    { "name": "adil", "public_key": "ssh-ed25519 AAAA…", "roles": ["staff", "platform"] },
    { "name": "crm-sync", "public_key": "ssh-ed25519 AAAA…", "roles": ["ingest"] },
    { "name": "platform-agent", "public_key": "ssh-ed25519 AAAA…", "roles": ["platform"] },
    { "name": "research-agent", "public_key": "ssh-ed25519 AAAA…", "roles": ["staff"] }
  ]
}
```

Restart the server and check the view from each identity.

```bash
ssh -p 2222 -i ~/.ssh/crm-sync <server> "lore docsets"
```

```text
DOCSET   GRANTS  ATTRIBUTES  PATH      TARGET
sources  rw      inbox       /sources  -
```

`crm-sync` sees `sources` and nothing else: `ls /teams` answers `No such file or directory`, because the docset is absent from its view rather than merely forbidden. `platform-agent` sees all three docsets and can write only under `/teams/platform`. `research-agent`, on `staff` alone, reads the handbook and the sources and can contribute to the platform team's inbox without being able to edit the team's runbooks.

The `doc-size` rule caps every Markdown file in the team area at an estimated 4,000 tokens. An agent that keeps appending to a runbook gets a rejection that names the rule and suggests a split, rather than a document that no longer fits in a prompt:

```text
rules: /teams/platform/runbooks/big.md: size/tokens (doc-size @ lore.json#docsets.platform)
  5000 tokens exceeds the limit of 4000 (max: 4000)
```

Rules are per docset, so the raw exports under `/sources` are not capped unless you say so.

> **Tip:** Add a team by adding a docset and a role. Add a source by adding a folder under `/sources`. Neither changes anything the existing agents see.

---

## 2. Connect your data sources

Anything that can produce a file can be a source. Choose the route by what the source can do, not by what OpenLore needs: there is no connector framework to fit into.

**A script or scheduled job, over SSH.** Export in whatever format the system gives you and pipe it to `tee`. `tee` creates the folder on first use, the write is attributed to `crm-sync` in the journal, and any folder rules on the path apply.

```bash
./export-accounts.sh | ssh -p 2222 -i ~/.ssh/crm-sync <server> "tee /sources/crm/accounts.md" >/dev/null
```

Run it from cron, a CI schedule or the system's own export hook. A CI job can skip the SSH key entirely and exchange its platform OIDC token for a short-lived OpenLore token with [workload identity federation](auth.md#workload-identity-federation), then run the same command through the [JSON API](transports.md#sessions-and-shell-state).

**A webhook or a service that only speaks HTTP.** Create an inbox credential for the `crm-sync` identity and point the source at `POST /inbox/sources`.

```bash
openlore inbox token create --identity crm-sync --label support-webhook --config openlore.yml
curl -H 'Authorization: Bearer olin_ID_SECRET' -H 'Content-Type: text/markdown' \
  --data-binary @ticket-4821.md 'https://<server>/inbox/sources?name=ticket-4821.md'
```

Each upload lands in the docset's inbox under a timestamped name such as `/sources/support/inbox/2026-10-15T115806-63622ca2bd7628d7-ticket-4821.md`, so a feed of tickets, meeting notes or alerts accumulates without one upload overwriting another. The credential is separate from any SSH key or OAuth token and can be revoked on its own. See [HTTP inbox uploads](inbox.md).

**A directory something else already keeps current.** The served tree is a directory on disk, so a git checkout, a synced folder or a wiki export that already lands there is already a source. Files that arrive this way are served and scoped like any other, but they bypass the write path: no attribution, no rules on the way in. Run `lore validate /sources` after a sync so rules are checked, and prefer one of the routes above when you want the journal to say who delivered a file.

Keep raw exports raw. Agents can read a CSV or a JSON Lines file directly with `cat`, `jq` and `awk`, and a summary written beside it by an agent in step 4 is easier to keep honest than a transform in the sync script.

---

## 3. Give the brain to agents

Every agent that can run a shell command or speak MCP can use the brain, and each one gets the view its identity was granted. For Claude Code, add the server over MCP; for an agent with a shell, give it an SSH identity and the server's own instructions.

```bash
claude mcp add --transport http openlore https://<server>/mcp
ssh -p 2222 -i ~/.ssh/platform-agent <server> agents >> AGENTS.md
```

The `agents` command prints instructions for the server as configured: the address, the commands, and how to publish. Pair it with a short `index.md` at the root of each docset that says what lives where. That index is what makes `grep` enough: an agent reads `/teams/platform/index.md`, learns that incident history is under `runbooks/incidents/` and raw alerts under `/sources/support/inbox`, and goes straight there.

Scoping is the part people usually build last and OpenLore makes first. A support agent with only the `staff` role answers from the handbook and the sources; it cannot stumble into a team's draft runbooks. A platform agent sees its own area in full. A contractor's agent granted a single docset sees one folder and does not know the others exist. When an agent connects through an OAuth client on someone's behalf, its writes are attributed as `person/client`, and `deny_docsets` on the client caps it below the person ([Auth](auth.md#roles-docsets-and-identities)).

Watch what the agents read. The dashboard's Analytics tab shows the most-read files, the files nobody has opened in the selected range, and the estimated context cost of each, so the brain's structure can follow how it is actually used rather than how it was planned.

---

## 4. Capture knowledge from across the organisation

Reading is half of a company brain. The other half is getting what people and agents learn back into it without opening the whole tree to everyone. Each route below writes through the same governed path: scoped, attributed, checked against folder rules, and rejected if it would overwrite a newer version.

**Agents contribute through inboxes.** A `publish` grant lets an agent read a whole docset and write only inside its inbox, never delete. `research-agent` from step 1 can hand findings to the platform team without touching a runbook.

```bash
echo "# Findings: retry storms on the queue" | publish /platform/retry-storms.md
```

```text
Published /teams/platform/inbox/retry-storms.md (38 bytes)
```

The path names the docset, not the folder; the server routes the file into the inbox, and the team moves it into place once it has been read. See [Let an agent publish into an inbox](publish-to-inbox.md).

**People and their agents get a home.** Declare a docset per person, such as `adil-home` at `/home/adil`, and name it on the identity. The owner has `rw` on it implicitly, it answers to `~` in the shell, and the docset can be granted read-only to the teams that should see it ([Identity home directories](auth.md#identity-home-directories)).

```json
{ "name": "adil", "public_key": "ssh-ed25519 AAAA…", "roles": ["staff", "platform"], "home": "adil-home" }
```

With the [Obsidian plugin](https://github.com/aakarim/obsidian-openlore), that home syncs two ways with a folder in a vault, read-only docsets pull in beside it, and **Send to agent** drops a pointer to a note into an agent's inbox. Notes people write for themselves become part of the brain without a separate publishing step.

**Agent sessions become a source.** A Claude Code `SessionEnd` hook can `tee` every transcript into a `sessions` docset, which turns the work agents do into material other agents can search. The [weekly briefing](weekly-briefing.md) recipe sets up the hook and shows what to do with the transcripts once they are there.

**Editors write through the same path.** SFTP lets VS Code and other editors open the tree directly and save individual files back through the governed write path ([Browse and edit from an editor](editors.md)), so a person tidying a runbook gets the same conflict check and rules as an agent.

Keep the shape of what comes in. The `doc-size` rule from step 1 already bounds the team area; add an `okf` block to docsets that should be a proper bundle with an index and a log, and ask agents to run `lore validate` before they finish ([Write → validate → finish](writing.md#write--validate--finish)). The rules are enforced by the server, so "keep it under 4,000 tokens" is no longer advice in a prompt.

---

## Verify

After the first sync, the first agent and the first contribution, read the journal as yourself.

```bash
ssh -p 2222 -i ~/.ssh/adil <server> "history | jq -c '{time, attribution, action, target}' | head"
```

You should see one line per write, newest first, each naming who made it and where it landed:

```text
{"action":"write","attribution":"crm-sync","target":"/sources/support/inbox/2026-10-15T115806-63622ca2bd7628d7-ticket-4821.md","time":"2026-10-15T11:58:06Z"}
{"action":"write","attribution":"research-agent","target":"/teams/platform/inbox/retry-storms.md","time":"2026-10-15T11:57:47Z"}
{"action":"write","attribution":"crm-sync","target":"/sources/crm/accounts.md","time":"2026-10-15T11:57:22Z"}
```

Then check the boundaries from the identities you trust least.

```bash
ssh -p 2222 -i ~/.ssh/crm-sync <server> "ls /teams"
echo x | ssh -p 2222 -i ~/.ssh/research-agent <server> "tee /teams/platform/runbooks/x.md"
```

```text
ls: /teams: No such file or directory
tee: /teams/platform/runbooks/x.md: read-only filesystem
```

The sync identity cannot see the team docset, and the research agent can read the runbooks but not change them. Its `publish /platform/x.md` from step 4 is the only write it has, and the journal shows it landing in the inbox.

---

## What OpenLore does not do

- **Rank or embed.** Agents find things with `grep` and `find`, and the structure you give the tree (an `index.md` per area, frontmatter, folder names that mean something) does the work a retrieval layer would otherwise do. If you need semantic search, build it beside the server against the [JSON API](transports.md#sessions-and-shell-state) or as a [plugin](plugins.md); the files stay where they are.
- **Host it for you.** The [Render](render.md) guide and the Fly, Railway and Nix files in the repository are starting points, not a managed service. Deployment, upgrades and backups are yours.
- **Roll back.** The journal records who changed what and when; there is no command that restores a file to an earlier revision. Keep the served directory in git or on a snapshotted volume if you need rollback.

---

## Next steps

- [Schedule a weekly briefing](weekly-briefing.md) that reads the journal and the sources you just connected and writes a summary with links back to them.
- [Tighten rules per folder](folder-rules.md) with `.lore/config.yaml`, so each team owns the shape of its own area.
- [Let CI jobs authenticate without secrets](auth.md#workload-identity-federation) once more than one pipeline writes into `/sources`.
- Run `ssh <server> skills` to see the instruction commands your server offers, and save the ones your agents need as `AGENTS.md` or a `SKILL.md`.
