# Prune a knowledge repository from its analytics

Give a coding agent the read analytics an operator sees, and have it recommend what to delete, merge or trim, with evidence a person can check before anything is removed.

In this recipe you can:

- Grant one identity the `lore:analytics:admin` capability so the agent can run `analytics` in the restricted shell.
- Rank folders and files by when they were last read, and tell files nobody reads apart from files that only matched a `grep`.
- Combine read counts with file age, size and byte-identical copies, so "unused" means "old and unread", not just "not read this month".
- Find the cold sections inside files that are otherwise read often.
- Hand the agent one prompt that writes a dated opportunities report, with one table per kind of cleanup and the context each row frees, then apply the rows you approve.
- Save the prompt as a skill in the repository, so any agent can find it with `lore meta --filter skills` and run the review on request.

The end result is a `reports` docset with one opportunities report per review, such as `/reports/cleanup/2026-10-13.md`. The report has one section each for duplicates, merge, delete, consolidate, trim and archive; each section states the context it frees and lists the files or folders in a table. Every row says why it is there (last read, age, size, reads, the copy or line range it refers to) and what it frees in bytes and tokens. The agent never deletes anything on its own.

---

## Before you begin

- A running server with analytics on. [`analytics.enabled`](../reference/openlore-yml.md#analyticsenabled) defaults to `true`, and the server must have been recording for long enough to judge use; a month is a reasonable minimum.
- An identity for the agent in `lore.json`, with a role you can grant the admin capability to. See [Identities, roles and docsets](../concepts/auth.md#manage-identities-and-roles).
- Writes enabled on the folders you will clean, so step 8 can `rm` and `mv`. Keep a backup of the repository, or an `archive/` folder in each docset, for anything you are not sure about: the journal records each delete, but there is no restore command.
- The [Agent Skills plugin](../concepts/plugins.md#install-the-agent-skills-plugin) enabled (`plugins.skills.enabled: true`), so step 7 can save the prompt as a skill.
- Understand what the [dashboard](../concepts/dashboard.md#facts-versus-activity) counts as a read. The shell `analytics` command reports the same events, instance-wide instead of per docset.

---

## 1. Grant the capability and create the docsets

The `analytics` command is operator-only: every subcommand needs the `lore:analytics:admin` capability and a full-scope token. Grant it through the role's `allow.capabilities`, and give the same role `rw` on a `reports` docset for the output and a `skills` docset for the saved prompt.

```json
{
  "roles": {
    "curator": { "allow": { "capabilities": ["lore:analytics:admin"] } },
    "team": {}
  },
  "docsets": {
    "reports": {
      "paths": ["/reports"],
      "access": { "allow": { "curator": "rw" } }
    },
    "skills": {
      "paths": ["/skills"],
      "access": { "allow": { "curator": "rw", "team": "ro" } }
    }
  },
  "identities": [
    { "name": "adil", "public_key": "ssh-ed25519 AAAA…", "roles": ["curator", "team"] }
  ]
}
```

Restart the server, then check the grant took.

```shell
analytics list
```

The output lists the aggregations and their status; `least-used-files`, `least-used-folders`, `least-used-lines`, `largest-docs` and `tree-size` are the ones this recipe uses. A denial names what is missing: the capability, a deny rule or the token scope.

> **Caution:** The capability sees reads across every docset on the instance, including ones the role cannot read. Grant it to an operator's identity, not to a shared agent key.

---

## 2. Find the horizon and the noise

"Never read" only means "not read since analytics started recording", so find that date first. The first bucket of `sessions-over-time` is the earliest event.

```shell
analytics show sessions-over-time --limit 1 --json | jq -r '.table.rows[0][0]'
```

The output is a date such as `2026-09-17`. A file modified after it and never read is new, not dead; a file modified a year before it and never read is the real candidate.

Then see who is reading. A crawler, backup job or a dashboard preview that walks the tree with `stat` and `cat` inflates read counts without anyone using the content.

```shell
analytics show commands-by-principal --limit 20
```

A principal with hundreds of thousands of `stat`, `cat` and `find` calls and one session per command is a crawler. Note its name: the prompt in step 6 tells the agent to discount reads from it, and `most-used-files` will show which files it touched.

> **Tip:** Every `analytics show` with a new combination of window, limit and parameters rescans the stored events. Keep the default 30-day window and scope queries with `--param path=<folder>`; a tree-wide `least-used-files --limit 0` over a custom `--since` window can take minutes on a large instance.

---

## 3. Rank folders

`least-used-folders` puts the folders nobody reads at the top. From the root with `depth=2` it gives one row per top-level folder and its children; the `NEVER_READ` column counts files in the folder with no read since the horizon.

```shell
analytics show least-used-folders --param path=/ --param depth=2 --json | jq -r '.table.rows[] | select(.[2] > 0) | [.[0], .[2], .[3], ((.[3] / .[2] * 100) | floor)] | @tsv'
```

The columns are path, files, never-read files and the never-read percentage.

```
/user/alfie/Bookmarks   83   80   96
/agent/jared/wiki-research/site   50   42   84
/agent/miles/logs   12   12   100
/runbooks   48   3   6
```

Folders near 100% are deletion candidates as a unit; folders in the middle need the file-level pass in the next step. Run the same query with `--param path=<folder>` and `depth=1` to drill into one.

---

## 4. List the files in a folder, with age and size

For each folder you want to review, three queries give the evidence. All three run in the restricted shell, so the same lines work over `ssh` or through the MCP `shell` tool.

Files never read since the horizon, with their modification time. `least-used-files` prints path, last read, reads, grep hits, tokens and reads per thousand tokens; a null last read is a file nobody has opened. `stat` adds the age.

```shell
analytics show least-used-files --param path=/agent/claw --limit 0 --json | jq -r '.table.rows[] | select(.[1] == null) | .[0]' | xargs -I{} stat {} | grep -E '^  File|^Modify' | paste - -
```

```
  File: /agent/claw/bin/x.txt	Modify: 2026-07-05 22:36:35
  File: /agent/claw/inbox/archive/oxted-health-centre.md	Modify: 2026-07-27 15:20:49
```

Files that only ever matched a `grep` and were never read in full. These show up in search results but nobody opens them, which is the signature of a file whose title or headings are more useful than its body.

```shell
analytics show least-used-files --param path=/agent/claw --limit 0 --json | jq -r '.table.rows[] | select(.[1] != null and .[2] == 0) | [.[0], .[3]] | @tsv'
```

The columns are path and grep hits.

The largest files, so the agent weighs an unread 400-token note differently from an unread 400,000-token dump.

```shell
analytics show largest-docs --param path=/agent/claw --limit 20 --json | jq -r '.table.rows[] | [.[0], .[1], .[3]] | @tsv'
```

The columns are path, bytes and tokens. `tree-size --param path=<folder> --param depth=1` gives the same per subfolder.

---

## 5. Find copies and cold sections

Two things analytics alone does not show. Byte-identical copies are the cheapest consolidation there is: keep one, link the rest. `md5sum` runs in the restricted shell.

```shell
find /agent/zeb -name '*.md' | xargs md5sum | sort | awk '{print $1}' | uniq -d
```

Each line is a hash that appears more than once. List the paths behind one with `find /agent/zeb -name '*.md' | xargs md5sum | grep <hash>`, and pair it with the `least-used-files` output to see which copy is the one agents actually read. Case-only duplicates (`Reports/` beside `reports/`) show in `least-used-folders` as two rows with similar names.

Cold sections inside a file that is read often. `least-used-lines` prints one row per line range with its last read and read count; a null last read is a range no read has covered.

```shell
analytics show least-used-lines --param path=/runbooks/gateway-oom-session-overflow.md --json | jq -r '.table.rows[] | select(.[3] == null) | [.[1], .[2]] | map(tostring) | join("-")'
wc -l /runbooks/gateway-oom-session-overflow.md
```

```
62-68
70-70
72-81
86-106
223 /runbooks/gateway-oom-session-overflow.md
```

Here lines 1 to 60 are read repeatedly and almost everything after line 62 never is: the file's first section earns its place and the rest is a candidate to move into a linked appendix or drop. Run this only for files that `most-used-files` ranks highly; an unread file has no hot section to compare against.

> **Tip:** The shell's `jq` is a restricted build. Use `floor`, `@tsv`, `map(tostring)` and `join` freely, but compute differences such as "lines in file minus cold lines" in your head or with `wc -l`; a `-` between two fields can be dropped from the filter.

---

## 6. Run the prompt

Give a coding agent the prompt below, connected to the server through the MCP `shell` tool or `ssh` with the identity from step 1. Replace `/agent/claw` with the folders you want reviewed, and `crawler` with the principal from step 2, or remove that sentence.

```
Review the OpenLore knowledge repository for files to prune and write a
cleanup report. Use the openlore MCP shell for every command. Do not delete,
move or edit anything outside /reports. Let TODAY be today's ISO date.

Scope: /agent/claw and /runbooks. Keep every analytics query scoped with
--param path=<folder> and the default window. Never run least-used-files
over / with --limit 0 or a custom --since.

1. Horizon. Run `analytics show sessions-over-time --limit 1 --json` and
   take the first date as HORIZON. Run `analytics show commands-by-principal
   --limit 20`. Reads by the principal `crawler` are not use; say so in the
   report and discount them.

2. Folders. For each folder in scope run `analytics show least-used-folders
   --param path=<folder> --param depth=2 --json` and note every subfolder
   where more than half the files were never read.

3. Files. For each folder in scope run `analytics show least-used-files
   --param path=<folder> --limit 0 --json`. Split the rows into:
   never read (last_read_at null), grep-only (last_read_at set, reads 0),
   and read. For every never-read and grep-only file, run `stat` and record
   the Modify time and Size. Run `analytics show tree-size --param
   path=<folder> --param depth=0 --json` for bytes and tokens per file, and
   sum them per subfolder with awk.

4. Copies. Run `find <folder> -name '*.md' | xargs md5sum | sort | awk
   '{print $1}' | uniq -d` for each folder, then `grep <hash>` on the same
   md5sum output to list the paths behind each hash. For each group, the
   copy with the most reads in step 3 is the one to keep.

5. Sections. For the five most-read files across the scope (`analytics show
   most-used-files --param path=<folder> --limit 5`), run `analytics show
   least-used-lines --param path=<file> --json` and `wc -l <file>`. For any
   contiguous never-read range longer than 40 lines, run `sed -n
   <start>,<end>p <file> | wc -c` for its bytes.

6. Classify. Put every finding into exactly one opportunity:
   - duplicates: byte-identical copies from step 4. Keep the most-read copy;
     the saving is the size of every other copy.
   - merge: two files or folders whose names differ only by case, a date
     suffix or a parent folder, with content that differs. The saving is the
     size of the smaller or less-read side, reported as an upper bound.
   - delete: never read, modified before HORIZON, no inbound links
     (`grep -rl "<basename>" <folder>` returns nothing else). Group by
     folder when a whole folder qualifies. The saving is its size.
   - consolidate: a folder tree that is mirrored in another place (the same
     subfolder names under two parents). List the tree once and point at the
     duplicates, merge and delete rows it produces; do not count its saving
     twice.
   - trim: a read file with a never-read range longer than 40 lines. The
     saving is the range's bytes per read, not once.
   - archive: never read but modified after HORIZON, or grep-only. Move
     under <folder>/archive/ so search still finds it. Saving is zero.
   - keep: everything else. Do not list these.
   Treat a file newer than HORIZON as unproven, never as unused. Tokens are
   bytes divided by four unless tree-size reports them.

7. Write /reports/cleanup/TODAY.md with tee. Frontmatter: title, type:
   Report, generated.at, horizon, scope. Body, in this order:
   - A paragraph with the counts: files in scope, never read, grep-only,
     read; HORIZON; the crawler principal and how you discounted it; and
     the total bytes and tokens the one-off sections free.
   - One "## <Opportunity>" section per opportunity, in the order
     duplicates, merge, delete, consolidate, trim, archive. Each section
     starts with a bold "Context freed: <bytes> bytes, ~<tokens> tokens"
     line (merge says "up to", trim says "per read", consolidate says it is
     counted in the other sections), then a one-sentence rule, then a table
     with one row per file or folder and a subtotal row. Columns:
     duplicates: keep, remove, reads kept / removed, bytes, tokens.
     merge: fold into, fold from, evidence, bytes (upper bound), tokens.
     delete: path, files (never read), last modified, bytes, tokens, note.
     consolidate: tree, shape, files, realised through (row references).
     trim: path, lines, cold range, cold bytes / total, reads, grep hits,
     tokens per read. archive: path, modified, reads, grep hits.
     A section with nothing in it keeps its heading and says "Nothing".
   - A "## Commands to apply" section with one `rm`, `rm -r`, `mv` or edit
     per row, grouped by opportunity, largest saving first. State that none
     has been run.
   - A "## How these numbers were produced" section listing the commands.

8. Run `cat /reports/cleanup/TODAY.md | head -30` and reply with the path,
   the "Context freed" line of each section, and its row count.
```

The agent writes one report and reports its path. Narrow the rules in step 6 to match your tolerance: raise the 40-line threshold, require a file to be older than the horizon by a margin, or drop the archive opportunity if you would rather see delete or keep.

> **Tip:** Run the prompt by hand once and read the report before giving it a schedule. A monthly run is enough; the window is 30 days and reads before it are already in the aggregation.

---

## 7. Save the prompt as a skill

Once the prompt produces a report you trust, keep it in the repository as an [Agent Skill](../concepts/plugins.md#install-the-agent-skills-plugin), so any agent connected to the server can discover and run it without you pasting the prompt. Enable the `skills` docset from step 1 as a collection, then write the skill. Each immediate child directory of the collection is one skill and must contain a `SKILL.md` with `name` and `description` frontmatter.

```shell
skills enable /skills
```

Save the prompt as the body of `/skills/knowledge-cleanup/SKILL.md`. The `description` is what an agent matches against when it searches for a skill, so say when to use it.

```shell
tee /skills/knowledge-cleanup/SKILL.md <<'EOF'
---
name: knowledge-cleanup
description: Review OpenLore read analytics and write a cleanup opportunities report that lists duplicates, merges, deletes, consolidations, trims and archives with the context each frees. Use when asked to prune, tidy or find unused files in the knowledge repository. Requires the lore:analytics:admin capability.
---

# Knowledge cleanup

<the prompt from step 6, with the scope and crawler lines left for the
caller to fill in>
EOF
```

Check that the skill validates and is discoverable.

```shell
skills validate /skills
lore meta --filter skills | jq -r 'select(.name == "knowledge-cleanup") | .path'
```

The second command prints `/skills/knowledge-cleanup/SKILL.md`. An agent then runs the review with "use the knowledge-cleanup skill on /agent/claw and /runbooks", and a Claude Code routine or Amp schedule can name the skill instead of carrying the prompt.

> **Tip:** Keep the scope out of the skill. The caller names the folders each time, so one skill serves every docset and the report never silently covers more than was asked.

---

## 8. Apply what you approve

The report's "Commands to apply" section lists one command per row. Review the tables, delete the rows you disagree with, and run the rest in the shell as an identity with `rw` on the folders.

```shell
rm /agent/claw/bin/x.txt
mv /agent/claw/inbox/archive/projects/doodad-20260727162741.md /agent/claw/archive/doodad-20260727162741.md
```

Each `rm` and `mv` is one journaled write. [`history`](../reference/commands.md#history---principal-name---actor-name) lists them with your attribution, and the dashboard timeline shows them under the folder.

> **Caution:** Deleting a file that other files link to breaks those links. The prompt's `grep -rl` check catches links by basename; run [`lore validate <folder>`](../reference/commands.md#lore-validate-dir) afterwards if the folder is an OKF bundle, and it reports any `openlore/broken-link` left behind.

---

## Verify

After the first run, read each section's "Context freed" line and spot-check one row from each table against the queries in steps 4 and 5.

```shell
cat /reports/cleanup/2026-10-13.md | sed -n 1,30p
```

You should see the frontmatter with `horizon` and `scope`, then the summary:

```
---
title: Knowledge cleanup, 2026-10-13
type: Report
generated:
  at: 2026-10-13T09:12:00Z
horizon: 2026-09-17
scope: [/agent/claw, /runbooks]
---

90 files in scope: 32 never read since 2026-09-17, 11 grep-only, 47 read.
Reads by `crawler` on 2026-10-10 are a tree walk and are discounted. The
duplicates, merge and delete sections free 1,630,260 bytes, ~407,000 tokens.

## Duplicates

**Context freed: 1,146,020 bytes, ~286,000 tokens** (4 files removed, 3 groups).

Byte-identical copies; the most-read copy stays.

| Keep | Remove | Reads kept / removed | Bytes | Tokens |
|---|---|---|---|---|
| /agent/claw/knowledge/local/team-governance-mvp.md | /agent/claw/inbox/archive/projects/team-governance-mvp.md | 3 / 0 | 8,940 | 2,235 |
```

Each section's first line is its saving; the table under it is the evidence. After applying the commands, rerun the folder query from step 3 for the same folder. The `FILES` count drops by the number of deletes, and the next month's report should list none of the same paths.

---

## Next steps

- [Cap folder growth](../concepts/folder-rules.md) with a `size/lines` or file-count rule on the folders that filled up, so the next review is smaller.
- [Let the agent publish into an inbox](../how-to/publish-to-inbox.md) if the report should go through review before it lands in `reports`.
- Pair the review with the [weekly briefing](weekly-briefing.md), which reads the same journal: the briefing says what was added, the cleanup report says what was never used.
