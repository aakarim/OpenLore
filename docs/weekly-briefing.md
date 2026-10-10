# Build a weekly briefing from docset changes

Schedule a coding agent to turn a week of synced Claude Code sessions and docset writes into one briefing that links back to its sources.

In this recipe you can:

- Sync every Claude Code transcript into a `sessions` docset with a `SessionEnd` hook.
- Read a week of change from two sources: the write timeline that `history` prints, and the Open Knowledge Format (OKF) `log.md` that your wiki keeps.
- Hand a coding agent one prompt that it runs on a weekly schedule, so the briefing writes itself.
- Query the briefings afterwards with `lore meta`, because each one carries frontmatter naming its sources.

The end result is a `briefings` docset with one file per week, such as `/briefings/2026-W41.md`. Each briefing says what was worked on, how Claude was used (repos, models, tools, prompt counts per session), and which documents changed, and every bullet links to the transcript or page it came from.

---

## Before you begin

- A running server with writes enabled and Claude Code connected over MCP. See [Claude Code with OpenLore](start-claude-code.md).
- An SSH identity for yourself in `lore.json`, so the hook can write as you. See [Identities, roles and docsets](auth.md#manage-identities-and-roles).
- `jq` on the machine that runs Claude Code. The restricted shell has its own `jq`, so the agent needs nothing extra.
- Understand what a [write](writing.md) records, and what the [dashboard](dashboard.md) shows as the timeline. The recipe reads the same journal.

---

## 1. Create the three docsets

The briefing reads from two docsets and writes to a third. In `lore.json`, mount `wiki` as an OKF bundle, `sessions` for transcripts and `briefings` for the output, and give your role `rw` on all three.

```json
{
  "roles": { "team": {} },
  "docsets": {
    "wiki": {
      "paths": ["/wiki"],
      "okf": { "patterns": ["*.md"], "enforce": true },
      "access": { "allow": { "team": "rw" } }
    },
    "sessions": {
      "paths": ["/sessions"],
      "access": { "allow": { "team": "rw" } }
    },
    "briefings": {
      "paths": ["/briefings"],
      "access": { "allow": { "team": "rw" } }
    }
  },
  "identities": [
    { "name": "adil", "public_key": "ssh-ed25519 AAAA…", "roles": ["team"] }
  ]
}
```

Restart the server. `lore docsets` now lists all three with `rw`.

Only `wiki` carries the `okf` block. Transcripts are JSON Lines, and briefings link outside their own folder, which `lore validate` would report as `openlore/link-outside-bundle` if `briefings` were a bundle too. Keep the journal on: `analytics.history.blobs` defaults to `true`, and [`analytics.history.retention`](openlore-yml.md#analyticshistoryretention) must cover at least a week.

---

## 2. Sync Claude Code sessions

Claude Code runs a `SessionEnd` hook when a session closes and passes `session_id` and `transcript_path` on stdin. Save this script as `~/.claude/hooks/sync-session.sh` and make it executable. `<server>` is your OpenLore host, and `~/.ssh/openlore` is the key for the identity from step 1.

```bash
#!/bin/bash
input=$(cat)
id=$(jq -r .session_id <<<"$input")
transcript=$(jq -r .transcript_path <<<"$input")
ssh -p 2222 -i ~/.ssh/openlore <server> "tee /sessions/claude/$id.jsonl" <"$transcript" >/dev/null
```

`tee` creates `/sessions/claude/` on first use, and the write is attributed to your identity in the timeline. Register the hook in `~/.claude/settings.json`. The `timeout` matters: the default budget for `SessionEnd` is 1.5 seconds, which is too short to upload a long transcript.

```json
{
  "hooks": {
    "SessionEnd": [
      { "hooks": [ { "type": "command", "command": "~/.claude/hooks/sync-session.sh", "timeout": 30 } ] }
    ]
  }
}
```

Close a Claude Code session, then list the docset.

```bash
ssh -p 2222 -i ~/.ssh/openlore <server> "ls /sessions/claude"
```

One file appears per closed session, named `<session-id>.jsonl` after the Claude Code session it came from.

> **Caution:** A transcript contains everything Claude read, including file contents from your repository. Grant `sessions` only to roles that may see that material.

---

## 3. Log changes in the wiki

The timeline records that `/wiki/retry-policy.md` changed; it does not say why. The OKF log does. Keep a `log.md` at the root of the wiki with one `## <date>` heading per day, newest first, and ask every agent that edits the wiki to add a line.

```markdown
## 2026-10-08
* **Update**: Raised the default backoff ceiling in the [Retry policy](/retry-policy.md) to 60s.

## 2026-10-06
* **Creation**: Added the [Deploy playbook](/playbooks/deploy.md).
```

The `okf` block from step 1 rejects a `log.md` that drifts from this shape, so the briefing can rely on it. Links inside the log are bundle-relative, as the [OKF plugin](plugins.md#open-knowledge-format-validation) requires; the briefing rewrites them when it links out.

---

## 4. Run the queries by hand

The prompt in the next step asks the agent to run three queries. Run them once yourself so you know what it will see. All three run in the restricted shell, so the same lines work over `ssh` or through the MCP `shell` tool.

The timeline, filtered to this week and reduced to the fields that matter. [`history`](commands.md#history---principal-name---actor-name) prints one JSON object per committed write, newest first, scoped to the docsets you can read.

```bash
history | jq -c 'select(.time >= "2026-10-06") | {time, attribution, action, target}'
```

Each line names a target and who wrote it. Writes made through Claude Code over OAuth show `attribution` as `<person>/claude-code@claude.ai`; plain names are people on SSH, and that pairing is how the briefing tells agent work from human work.

The wiki log, cut to the headings dated this week.

```bash
cat /wiki/log.md | awk '/^## /{keep = ($2 >= "2026-10-06")} keep'
```

The output is the top of `log.md` down to the first heading older than the cutoff.

One synced transcript, reduced to the tools Claude called. Transcript lines are JSON objects with a `type` of `user` or `assistant`; assistant content is a list of blocks, and `tool_use` blocks carry the tool `name`.

```bash
cat /sessions/claude/<session-id>.jsonl | jq -r 'select(.type == "assistant") | .message.content[]? | select(.type == "tool_use") | .name' | sort | uniq -c | sort -rn
```

The output is a count per tool, with `mcp__openlore__shell` listed when Claude used the knowledge repository. Prompts are `user` lines whose `message.content` is a string and whose `isMeta` is not `true`; `cwd` and `gitBranch` give the repository, and `message.model` the model.

---

## 5. Schedule the prompt

Give a coding agent the prompt below on a weekly schedule. In Claude Code Desktop, open **Routines**, create a local routine, set it to weekly, and paste the prompt; the routine connects to the same MCP server as your sessions. Scheduled tasks created inside a session with `/loop` expire after seven days, so use a routine or a tool that keeps its own schedule, such as an Amp thread schedule.

```text
Write the weekly briefing for the OpenLore knowledge repository. Use the
openlore MCP shell for every command. Let WEEK_START be the Monday seven days
ago as an ISO date, and WEEK the ISO week number of that Monday.

1. Timeline. Run `history` and keep entries whose time is on or after
   WEEK_START. Group them by target. An attribution of the form
   <person>/claude-code@claude.ai is a write Claude Code made for that person;
   a plain name is a person writing directly.

2. Logs. Run `find / -name log.md`. For each result, keep the `## <date>`
   sections dated on or after WEEK_START.

3. Sessions. For each /sessions/claude/*.jsonl target in this week's timeline,
   read the file with cat and jq, and extract: the person's prompts (type
   user, isMeta not true, message.content a string); the tool names used
   (assistant tool_use blocks); cwd and gitBranch; message.model; the first
   and last timestamp. Summarise each session in one sentence: what was asked
   and what changed.

4. Write /briefings/<year>-W<WEEK>.md with tee. Frontmatter: title, type:
   Report, generated.by, generated.at, and sources with one `resource:` per
   file you read, as a path relative to the briefing. Body sections: "What was
   worked on" (bullets, one per theme, each linking its session and any wiki
   page it touched); "How Claude was used" (a table with session, repository
   and branch, model, tools with counts, prompt count); "Docset changes" (one
   bullet per log entry and per timeline target not covered by a log entry,
   with the author). Every link is relative, such as
   ../sessions/claude/<id>.jsonl or ../wiki/retry-policy.md. Rewrite
   bundle-relative log links such as /retry-policy.md to ../wiki/retry-policy.md.

5. Run `lore meta /briefings` and confirm the new file is listed. Reply with
   the path and the three section headings.

Write only inside /briefings. If there were no sessions and no writes this
week, write a briefing that says so.
```

The agent writes one file per week and reports its path. Change the sections or the table columns in step 4 of the prompt to match what your team wants to read.

> **Tip:** Run the prompt once by hand in a Claude Code session before scheduling it, and read the briefing it produces. A routine inherits the same MCP server and identity, so what works interactively works on the schedule.

---

## Verify

After the first scheduled run, list the briefings with their sources.

```bash
lore meta /briefings | jq -c '{path, title, sources: [.sources[].resource]}'
```

You should see one line per week:

```text
{"path":"2026-W41.md","title":"Weekly briefing, 2026-W41","sources":["../sessions/claude/3c90a1e2-7b4f-4d8e-9a21-5f6e7d8c9b0a.jsonl","../wiki/log.md"]}
```

The briefing itself appears in the timeline as a write to `/briefings/`, attributed to your identity through the agent, and opens in the dashboard with every source link resolving.

---

## Next steps

- [Cap the size of each briefing](folder-rules.md) with a `size/lines` rule on `/briefings`, so a busy week produces a summary rather than a transcript.
- [Let the agent publish into an inbox](publish-to-inbox.md) instead of granting `rw` on `briefings`, if a person should review each briefing before it is final.
- Query briefings over time with [`lore meta`](commands.md#lore-meta-path) to find every week that touched one wiki page.
