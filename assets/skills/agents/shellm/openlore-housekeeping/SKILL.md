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
`$OPENLORE_SSH`).{{if .Mounts}} Documentation is mounted at {{range $i, $m := .Mounts}}{{if $i}}, {{end}}`{{$m}}`{{end}};
the checks below target `{{.Mount}}` — repeat them for each mount.{{end}}

## Find stale documents

Documents carry frontmatter. Use `lore meta` to list paths with dates, then
flag old ones:

```bash
ssh $OPENLORE_SSH "lore meta {{.Mount}} | jq -r 'select(.updated != null) | [.updated, .path] | @tsv' | sort"
```

Flag anything older than the agreed threshold (default: 90 days). Also flag
documents with no frontmatter date at all:

```bash
ssh $OPENLORE_SSH "lore meta {{.Mount}} | jq -r 'select(.updated == null) | .path'"
```

## Find broken internal links

Extract relative Markdown links and check each target exists:

```bash
ssh $OPENLORE_SSH "grep -rho ']([^)h][^)]*)' {{.Mount}} | tr -d ']()' | sort -u"
```

For each path, test it: `ssh $OPENLORE_SSH "test -e {{sub .Mount "<target>"}} || echo missing: <target>"`.

## Check the publish inboxes

Items published into an inbox wait for a human to move them into the docset.
List what is waiting and how old it is:

```bash
ssh $OPENLORE_SSH "find / -type f | grep '/inbox/' | xargs -I{} stat {}"
```

Flag inbox items older than 7 days: they are stuck and need a human decision.

{{if exists "/trajectories"}}## Check trajectory freshness

This server has a `/trajectories` docset. Confirm recent agent runs are being
synced (`ls -l` shows modification times):

```bash
ssh $OPENLORE_SSH "ls -l /trajectories"
```

Compare with local `~/.headlong/trajectories/`. Sync any completed run that is
missing (see the `openlore` skill for the sync procedure).

{{end}}{{if exists "/skills"}}## Check skill coverage

This server hosts a `/skills` collection. Verify every skill directory has a
`SKILL.md`:

```bash
ssh $OPENLORE_SSH "ls /skills | while read -r d; do test -e /skills/\$d/SKILL.md || echo missing: \$d; done"
```

{{end}}## Publish the report

Write one Markdown report with a section per check and only actionable
findings. Publish it; the inbox keeps reports out of the docset until a human
accepts them:
{{if .Publish}}
```bash
cat report.md | ssh $OPENLORE_SSH "publish /{{.PublishDocset}}/housekeeping/$(date +%Y-%m-%d).md"
```
{{else}}
This identity currently has no publish access, so deliver the report to the
requester instead. Ask the server operator for a `publish` grant on a reports
docset; `ssh $OPENLORE_SSH "publish"` then lists the docsets you can publish
to, and `publish /<docset>/housekeeping/<date>.md` files the report into that
docset's inbox.
{{end}}
If nothing is wrong, publish a one-line all-clear instead. Never edit other
documents directly during housekeeping; propose changes in the report.
