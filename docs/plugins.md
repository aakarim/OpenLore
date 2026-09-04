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
INFO plugin registered name=okf version=0.2.0
INFO plugin registered name=inbox version=1.0.0
```

## Open Knowledge Format validation

The built-in OKF plugin validates knowledge against the
[Open Knowledge Format](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md).
It targets spec v0.2 and also accepts v0.1 bundles: a bundle declaring
`okf_version: "0.1"` in its root `index.md` is linted against v0.1 rules,
anything else against v0.2. Hard conformance violations are errors; shape
problems in a version's optional field families (e.g. v0.2 `sources`,
`generated`, `verified`, `status`, Attested Computation contracts) are
warnings from `lore validate`. Enable it per docset:

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

The `okf` block is shorthand for four [folder rules](folder-rules.md): `okf`
(checked on write), plus `okf/bundle`, `link/resolves` and `link/alias`
(checked by `lore validate` only). Declare them under `docsets.<name>.rules`
instead when you want to combine them with other members such as `size/lines`,
or scope them with `match`/`exclude` globs.

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
alone do not. If no validator plugin is enabled, the command emits
`lore validate: warning: no validators enabled` rather than reporting an empty
scan as valid.

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

## Install the Agent Skills plugin

Enable Skills management in `openlore.yml`:

```yaml
plugins:
  skills:
    enabled: true
    remote_check_ttl: 60s
    remote_timeout: 3s
    remote_max_bytes: 10MB
```

The defaults shown above are used when the optional remote settings are
omitted. The server must also have writing enabled, and each identity that
manages skills needs the named `rw` grant on its destination docset. A home
docset is implicitly `rw` for its owner.

Docsets do not need a static `agent_skills` setting. After installation, an
authorized user enables a directory as a collection with `skills enable`; the
marker is portable with the directory and can be changed without restarting the
server.

Each immediate child directory is then a skill and must contain an exactly named
`SKILL.md` with valid frontmatter. Writes are checked at admission and again
before serialized commit. Set `metadata.agent_skill: disable` to treat a
parseable `SKILL.md` as ordinary documentation.

Agents discover skills the same way they query OKF metadata. The `skills`
filter scopes `lore meta` to Agent Skills collections and returns only valid
`SKILL.md` records — frontmatter plus the skill's path:

```bash
lore meta --filter skills
lore meta --filter skills | jq -r 'select((.name + " " + .description) | test("pdf"; "i")) | .path'
```

Run bare `skills` for collection management, importing and tracking remote
skills, status checks, updates, and unlinking.
