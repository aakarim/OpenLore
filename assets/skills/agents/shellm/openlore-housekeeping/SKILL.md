---
name: openlore-housekeeping
description: Audit and maintain a shared OpenLore knowledge base. Use on a schedule or on request to find stale docs, broken links, unreviewed inbox items, and missing skill coverage, then publish an audit report.
metadata:
  shelllm:
    requires:
      bins: ["ssh"]
---

# OpenLore Housekeeping

Keep a shared knowledge base healthy. Run each check below, collect findings,
and publish one report. Requires the `openlore` skill (server access via
`$OPENLORE_SSH`).

## 1. Find stale documents

Documents carry frontmatter. Use `lore meta` to list paths with dates, then
flag old ones:

```bash
ssh $OPENLORE_SSH "lore meta /docs | jq -r 'select(.updated != null) | [.updated, .path] | @tsv' | sort" 
```

Flag anything older than the agreed threshold (default: 90 days). Also flag
documents with no frontmatter date at all:

```bash
ssh $OPENLORE_SSH "lore meta /docs | jq -r 'select(.updated == null) | .path'"
```

## 2. Find broken internal links

Extract relative Markdown links and check each target exists:

```bash
ssh $OPENLORE_SSH "grep -rho ']([^)h][^)]*)' /docs | tr -d ']()' | sort -u" 
```

For each path, test it: `ssh $OPENLORE_SSH "test -e /docs/<target> || echo missing: <target>"`.

## 3. Check the publish inboxes

Items published into an inbox wait for a human to move them into the docset.
List what is waiting and how old it is:

```bash
ssh $OPENLORE_SSH "find / -type f | grep '/inbox/' | xargs -I{} stat {}"
```

Flag inbox items older than 7 days: they are stuck and need a human decision.

## 4. Check trajectory freshness

If the server has a `/trajectories` docset, confirm recent agent runs are
being synced:

```bash
ssh $OPENLORE_SSH "ls -t /trajectories | head -5"
```

Compare with local `~/.headlong/trajectories/`. Sync any completed run that is
missing (see the `openlore` skill for the sync procedure).

## 5. Check skill coverage

If the server hosts a skills collection, verify every skill directory has a
`SKILL.md`:

```bash
ssh $OPENLORE_SSH "ls -1 /skills | while read -r d; do test -e /skills/\$d/SKILL.md || echo missing: \$d; done"
```

## 6. Publish the report

Write one Markdown report with a section per check and only actionable
findings. Publish it; the inbox keeps reports out of the docset until a human
accepts them:

```bash
cat report.md | ssh $OPENLORE_SSH "publish /reports/housekeeping/$(date +%Y-%m-%d).md"
```

If nothing is wrong, publish a one-line all-clear instead. Never edit other
documents directly during housekeeping; propose changes in the report.
