# Plugins and Knowledge Formats

OpenLore plugins extend policy, validation, metadata, and processing while core
commands continue to own the user-facing shell surface.

## Provider interfaces

| Interface | Contribution |
|---|---|
| `WriteMiddlewareProvider` | Admission middleware before commit |
| `ReadMiddlewareProvider` | Middleware before reads |
| `PostCommitProvider` | Middleware after successful commits |
| `GrantTypeProvider` | Named grant types such as `publish` |
| `ValidatorProvider` | Checks run by `lore validate` |
| `MetaExtenderProvider` | Fields added to `lore meta` records |
| `PluginInfoProvider` | Plugin name and semantic version logged at boot |

Built-in plugins include `shellexec`, `inbox`, and `okf`. Go consumers register
additional plugins through `Server.RegisterPlugin`.

Admission middleware receives an immutable `WriteOp`. It **must** use
`WriteOp.Leaves()` and inspect every leaf; batches are one policy decision and
the first leaf is not representative. Construct operations with `NewWriteOp`.
Middleware that defers an operation uses `op.Pending(ref)`, which captures the
complete immutable batch for persistence and later replay.

```text
INFO plugin registered name=shellexec version=1.0.0
INFO plugin registered name=okf version=0.1.0
INFO plugin registered name=inbox version=1.0.0
```

## Open Knowledge Format validation

The built-in OKF plugin validates knowledge against
[Open Knowledge Format v0.1](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md).
Enable it per docset:

```json
{
  "docsets": {
    "wiki": {
      "paths": ["/wiki"],
      "okf": {
        "patterns": ["*.md"],
        "enforce": true
      }
    }
  }
}
```

Validation runs before content reaches disk for every write verb. `patterns`
defaults to `["*.md"]`; `enforce` defaults to `true`, while `false` logs and
allows findings.

The owning docset's configuration governs each target. Use nested docsets to
scope validation more narrowly or to exempt a subtree from its parent's policy.

Downstream Go code can apply the same rules directly:

```go
import "github.com/aakarim/go-openlore/pkg/okf"

if err := okf.Validate(path, content); err != nil {
	// Content is not OKF-conformant.
}
```

## Bundle linting with `lore validate`

`lore validate [bundle]` scans the current or selected bundle, invokes enabled
plugin validators, and adds OpenLore's local-link and alias-portability checks:

```text
tables/orders.md:1:1: error [okf/concept] frontmatter is missing the required non-empty 'type' field
metrics/revenue.md:12:19: error [openlore/broken-link] local link "../tables/missing.md" does not resolve
metrics/revenue.md:15:8: warning [openlore/alias-target] link targets aliased docset path /wiki; it may resolve differently on another machine
2 errors, 1 warning
```

- `okf/*` findings come from OKF conformance rules.
- `openlore/broken-link` and `openlore/link-outside-bundle` are operational
  errors; OKF itself permits consumers to tolerate broken links.
- `openlore/alias-referrer` and `openlore/alias-target` warn that links may not
  be portable to a server with different aliases.

The command does not fetch URLs. Errors produce a non-zero exit status; warnings
alone do not.

## Metadata queries with `lore meta`

`lore meta [path]` walks readable Markdown documents under the current directory
or path and emits parseable YAML frontmatter as one JSON object per line. Bodies
remain out of the response, keeping discovery cheap:

```bash
cd backend
lore meta
lore meta | jq -r .type | sort -u
lore meta | jq -r 'select(.type=="Metric").path' | xargs cat
```

The walk uses the session filesystem, so it cannot reveal documents outside the
identity's read scope.

When OKF applies to a document, the plugin enriches its metadata record:

```json
{"path":"orders.md","type":"Table","okf":{"valid":true}}
{"path":"draft.md","title":"No type","okf":{"valid":false,"error":"frontmatter is missing the required non-empty 'type' field"}}
```

```bash
lore meta | jq -r 'select(.okf.valid == false) | .path'
```

## Agent Skills collections

Mark a docset as an [Agent Skills](https://agentskills.io/) collection:

```json
{
  "docsets": {
    "skills": {
      "paths": ["/skills"],
      "aliases": ["/agent-skills"],
      "agent_skills": true
    }
  }
}
```

Each immediate child directory is then a skill and must contain an exactly named
`SKILL.md` with valid frontmatter. Writes are checked at admission and again
before serialized commit. Set `metadata.agent_skill: disable` to treat a
parseable `SKILL.md` as ordinary documentation.

Discover enabled, accessible collections without scanning unrelated docs:

```bash
lore meta --filter agent_skills
lore meta --filter skills /skills/deploy
```

Accepted filter names are `agent_skills`, `agent_skill`, `skills`, and `skill`.
Results use absolute canonical mounted paths.
