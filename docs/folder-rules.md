# Folder Rules

Folder rules are declarative checks that OpenLore runs against files before
they reach disk and again under `lore validate`. A rule names a set of files
with globs and a *member* of the rules standard library to check them with:
`size/lines` caps a file's length, `okf` enforces Open Knowledge Format
conformance, `link/resolves` reports broken links. Rules live in three places
that layer together: `lore.json` (server-wide and per docset) and
`.lore/config.yaml` files inside the content tree.

This guide explains how the layers combine, the configuration schema, what runs
when, who may edit rules, how to read a rejection, and how growth limits
(`max: initial`) work. For each member's parameters see the generated
[Rules standard library](rules-stdlib.md), or run `lore package doc <member>`
inside a session.

## Three layers, one effective rule set

For any target path the engine unifies every layer whose scope contains it:

```diagram
lore.json  rules:            ─┐  applies to every docset
lore.json  docsets.X.rules:   │  applies under docset X's display roots
/docs/backend/.lore/config.yaml   applies to /docs/backend and below
/docs/backend/decisions/.lore/config.yaml   applies to decisions/ and below
                             ─┘
              ▼
   effective rule set for /docs/backend/decisions/adr-001.md
```

- Rules with **different names** are all evaluated. A violation of any one of
  them rejects the write. Layers only ever add checks.
- The **same name** at two layers is the same rule. If the outer layer marked
  it `default: true`, the inner spec replaces it. Otherwise the two specs must
  be identical, or the configuration is invalid and is rejected when the
  `.lore/config.yaml` is written (or at boot for `lore.json`, or by
  `lore validate`):

  ```text
  .lore/config.yaml: rules.doc-size: conflicts with doc-size @ lore.json (max: 400 vs 60); use a new rule name to tighten
  ```

- To tighten a rule from a child folder, add a rule with a **new name**. A
  child can never loosen a rule it did not author, except where the parent said
  `default: true`.
- To exempt files, add `exclude:` to the rule at the layer that owns it, or use
  docset shadowing: a nested docset that does not declare a rule exempts its
  subtree from the parent docset's rules, exactly as OKF scoping already works.

There is no `inherit` key. A `.lore/config.yaml` applies to its directory and
everything beneath it.

## `.lore/config.yaml`

```yaml
version: 1

rules:
  adr-length:                         # rule name — unique within this file
    match: ["decisions/*.md"]         # globs, relative to this folder; `**` supported
    exclude: ["decisions/draft-*.md"] # optional
    use: size/lines                   # standard library member
    with: { max: initial, growth: 1.1 }
    enforce: true                     # default true; false = warn and allow
    default: false                    # true = a same-named rule in a child folder replaces this one

# Reserved for future releases. Present but non-empty is an error: "not supported yet".
packages: {}
hooks: {}
operations: {}
```

Rule-level keys are exactly `match`, `exclude`, `use`, `with`, `enforce` and
`default`. Everything a member needs goes inside `with` and is validated
against the member's declared parameters. Unknown keys, unknown members and
unknown parameters are rejected with a suggestion:

```text
.lore/config.yaml: rules.adr-length.with.maxx: unknown parameter for size/lines (did you mean "max"?)
```

The file is written like any other file (`cat config.yaml | ssh … "cat >
/docs/backend/.lore/config.yaml"`, `patch`, `sed -i`), subject to the
permission below. Rules are never evaluated against the config file itself.
`tree` and `ls` show `.lore/config.yaml` when it exists and nothing else under
`.lore/`.

### Globs

`match` and `exclude` are matched against the target path **relative to the
layer's scope directory**: the containing folder for `.lore/config.yaml`, the
docset display root for `lore.json` layers. `*` matches within one path
segment; `**` matches zero or more segments. A rule applies when any `match`
pattern matches and no `exclude` pattern does.

## `lore.json`

```json
{
  "rules": {
    "doc-size": { "match": ["**/*.md"], "use": "size/kilobytes", "with": { "max": 60 }, "default": true }
  },
  "docsets": {
    "backend": {
      "paths": ["/docs/backend"],
      "access": { "allow": { "engineer": "rw" } },
      "rules": {
        "format": { "match": ["**/*.md"], "use": "okf" }
      },
      "config": { "edit": ["engineer"] }
    }
  }
}
```

- Top-level `rules` apply to every docset. `docsets.<name>.rules` apply under
  that docset's display roots. Both use the same rule shape as
  `.lore/config.yaml`.
- `config.edit` lists the roles that may create, edit or delete
  `.lore/config.yaml` files under the docset (see [Permissions](#permissions)).
- The existing per-docset `okf` block keeps working. It desugars to four rules
  so that one switch governs the whole OKF family: `okf` (file scope, the
  block's `patterns` as `**/<pattern>`, its `enforce`), `okf/bundle`,
  `link/resolves` and `link/alias` (bundle scope, `**/*.md`; `link/alias` is
  always a warning). Writes are still checked by `okf` alone and
  `lore validate` still reports the rest, so existing deployments see no
  change.

## `openlore.yml`

```yaml
rules:
  growth: 1.25          # default multiplier for `max: initial`; must be ≥ 1
```

`rules.tokenizer` is reserved. Setting it fails at boot with
`rules.tokenizer is not supported yet`; `size/tokens` always uses the built-in
estimator (see [Growth limits](#growth-limits)).

## What runs when

Every member declares a scope, shown by `lore package list` and in
[rules-stdlib.md](rules-stdlib.md).

| Scope | Members | On write | Under `lore validate` |
|---|---|---|---|
| `file` | `okf`, `size/kilobytes`, `size/lines`, `size/tokens` | evaluated; an enforcing finding rejects the write | evaluated per file |
| `bundle` | `okf/bundle`, `link/resolves`, `link/alias` | never | evaluated once at the bundle root |

Bundle rules need to see related files. A write that adds a new concept
legitimately breaks `okf/bundle` until `index.md` is updated in a following
write, and a link target may be written next. Rejecting or even warning on the
first write would be noise, so bundle rules leave no trace on the write path
and are the job of `lore validate`.

On write, the engine resolves the effective rule set for each file in the
operation and evaluates its file-scope rules. A finding from an `enforce: true`
rule rejects the whole operation (a batch is one decision; the first violation
rejects it). A finding from an `enforce: false` rule is logged as a warning and
the write proceeds. A member that errors, rather than finding a violation,
rejects the write unless the rule is non-enforcing: the system fails closed.

`lore validate <dir>` runs both passes over `<dir>` (default: the current
directory) without writing anything. Run it per docset or folder, not from `/`:
if the named directory is *above* docsets that declare bundle rules, those
rules would not be in effect at the root and would silently not run, so the
command refuses instead:

```text
lore validate: / is above docsets docs, wiki with bundle rules; run lore validate per docset
```

Validate output is one line per finding (`path:line:col: severity [rule]
message`), followed by one `see: lore package doc <member>` line per member
that produced findings. Severity is `error` for enforcing rules and `warning`
otherwise. Every `.lore/config.yaml` in the bundle is also decoded and unified;
configuration errors are reported as diagnostics.

## Permissions

Writing `<dir>/.lore/config.yaml` requires both a write grant for `<dir>` under
the owning docset **and** a role listed in that docset's `config.edit`.
Otherwise the write fails with a permission error. A docset without
`config.edit` has no one who can write its folder configs. Anyone who can read
the folder can read its config.

The same gate applies to deletion. `rm` of a config file needs the same two
conditions. `rm -r` of a directory tree that contains one or more
`.lore/config.yaml` files needs them for every config in the tree, otherwise
the whole removal fails and nothing is deleted — without this, plain `rw` could
drop a folder's governance by deleting and recreating the folder.

`lore size baseline reset` (below) is authorised the same way, because it
loosens a rule for one file.

## Reading a rejection

A rejected write exits non-zero and prints a block written for the agent that
just had its write refused. It answers, in order: what was measured and what
the limit is, what cannot be done, what to do instead, and how a privileged
human can override.

```text
rules: /docs/backend/decisions/adr-001.md: size/lines (adr-length @ /docs/backend/.lore/config.yaml)
  812 lines exceeds the limit of 800 (baseline 640 lines × growth 1.25, set 2026-08-30 on create)
  this file cannot grow past 800 lines under this rule
  suggested: keep adr-001.md under 800 lines; move the new material into a sibling file such as adr-001-details.md and add a link to it from adr-001.md so readers can drill in
  override: a role in config.edit can run `lore size baseline reset /docs/backend/decisions/adr-001.md`
  see: lore package doc size/lines
```

The header names the member, the rule and the layer that declared it
(`/docs/backend/.lore/config.yaml`, `lore.json#docsets.backend`, or `lore.json`
for a top-level rule), so you know which file to edit. For a fixed `max` the first line reads
`812 lines exceeds the limit of 800 (max: 800)` and the `override:` line
becomes `to raise the limit, edit adr-length in /docs/backend/.lore/config.yaml`.

The `suggested:` remedy for `size/*` is progressive disclosure: keep the file
within its limit, move the new material into a sibling file, and link to it
from the original so readers can drill in. Retrying with small trims is
pointless when the overrun is large.

`okf` rejections keep their existing one-line diagnostic (it already names the
fix) and add only the `see:` line:

```text
okf: /docs/bad.md: missing YAML frontmatter block (a concept must open with a '---' delimited block)
  see: lore package doc okf
```

## Growth limits

`max: initial` caps a file relative to its own size instead of a fixed number.
The first admitted write of a file records a **baseline** (kilobytes, lines and
tokens); afterwards the file may not exceed `baseline × growth`. `growth`
defaults to `openlore.yml rules.growth` (1.25 unless configured) and can be set
per rule.

```yaml
rules:
  adr-length:
    match: ["decisions/*.md"]
    use: size/lines
    with: { max: initial, growth: 1.1 }
```

How the baseline is taken:

- **Create.** The baseline is the content of the first admitted write. If that
  write is rejected by another rule, its baseline never governs anything: the
  next write of the file is treated as a create again.
- **Rule added to existing files.** The first write to a file that already
  exists records the baseline from the *existing* content, before the proposed
  write is evaluated. Adding `max: initial` to a folder never rejects the next
  edit merely because the file was already large; it freezes the file at the
  size it had.
- **`rm`** clears the baseline; recreating the file starts a new one.
- **`mv`** carries the baseline to the new path.
- **`lore validate`** reads baselines but never records one.

State lives in the content tree beside the file, in
`<dir>/.lore/size/<basename>.jsonl`, so it travels with the content when the
tree is copied or checked in. It is never listed by `tree` or `ls` and cannot be
read or written through the VFS; `lore size baseline <path>` is the view onto
it. Records are append-only: a new baseline is added, older ones are kept, and
every record names the actor who caused it, so the history answers "who set
this cap".

`size/tokens` counts with the built-in estimator `estimate/v1`
(`ceil(bytes / 4)`). It is an approximation, so leave a margin when a token cap
matters. Each token baseline records the estimator that produced it; if the
estimator changes in a later release, token baselines from the old one are
re-taken on the next write while kilobyte and line baselines are untouched.

### Inspecting and resetting a baseline

```text
$ lore size baseline /docs/backend/decisions/adr-001.md
/docs/backend/decisions/adr-001.md  (size/lines via adr-length @ /docs/backend/.lore/config.yaml, growth 1.1)
  2026-08-30T09:12:04Z  create      agent:claude@acme.com   412 lines  12 KiB  3900 tokens (estimate/v1)
  2026-09-02T14:01:37Z  reset       adil                    812 lines  21 KiB  6400 tokens (estimate/v1)  "ADR grew after review"
current cap: 893 lines
```

`lore size baseline reset <path> [--note <text>]` re-measures the current
content and appends a new baseline with reason `reset`, so the file may grow
again by `growth` from where it is now. It requires a write grant on the path
and a role in the owning docset's `config.edit`, the same as editing
`.lore/config.yaml`. Earlier records are kept, and the reset is also written to
the server audit log as `rules.baseline.reset` with the previous and new
baseline and the note.

```text
$ lore size baseline reset /docs/backend/decisions/adr-001.md --note "ADR grew after review"
baseline reset: previous 412, new 812, new cap 893 lines
```

An agent that hits a growth limit should follow the `suggested:` line, or ask
a human for the exact `override:` command, rather than retry.

## Worked example: tightening a docset rule in one folder

The docset allows Markdown files up to 60 KiB and marks that rule as a default:

```json
{
  "docsets": {
    "backend": {
      "paths": ["/docs/backend"],
      "access": { "allow": { "engineer": "rw" } },
      "config": { "edit": ["engineer"] },
      "rules": {
        "doc-size": { "match": ["**/*.md"], "use": "size/kilobytes", "with": { "max": 60 }, "default": true }
      }
    }
  }
}
```

The team wants decision records kept short and frozen once written, and a
tighter size for everything in `decisions/`. An `engineer` writes
`/docs/backend/decisions/.lore/config.yaml`:

```yaml
version: 1
rules:
  doc-size:                              # same name + parent said default → replaces 60 KiB
    match: ["**/*.md"]
    use: size/kilobytes
    with: { max: 20 }
  adr-length:                            # new name → adds a check
    match: ["*.md"]
    exclude: ["index.md"]
    use: size/lines
    with: { max: initial, growth: 1.1 }
```

Under `decisions/`, a Markdown write must now pass `doc-size` at 20 KiB and
`adr-length`, and any `okf` rule the docset declares. Elsewhere in the docset
the 60 KiB default still applies. Had the docset's `doc-size` not been marked
`default: true`, writing this config would have been rejected with the
`conflicts with doc-size @ lore.json#docsets.backend (max: 60 vs 20)` error,
and the folder would have had to
add its tighter cap under a new name instead.

Check the result without writing anything:

```bash
lore validate /docs/backend/decisions
```
