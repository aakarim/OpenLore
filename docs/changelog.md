# Changelog

**Current release: [v0.7.3](https://github.com/aakarim/OpenLore/releases/tag/v0.7.3).**

## Which version do these docs describe?

The documentation on `main` tracks development, with **v0.7.3** as the current
release baseline. It can include changes not yet released; those changes belong
under **Unreleased** below. Version-specific behavior should be labelled with the
release that introduced it, rather than assumed to work on older installations.

Check your installed binary:

```bash
openlore version
```

For v0.7.3, the output is `openlore 0.7.3`. If you connect to a remote server, ask
its operator for the server version too: your local binary may be a different
version.

For a fixed documentation snapshot, use the
[v0.7.3 docs](https://github.com/aakarim/OpenLore/tree/v0.7.3/docs).
For an older installation, choose its release tag in GitHub's branch/tag selector
instead of reading `main`. Release notes below highlight changes that may explain
differences between the docs and your installation.

The full published release notes are preserved below, newest first. Examples,
limitations, and verification statements describe their respective releases, not
necessarily the current version. Dates are the GitHub publication dates in UTC.

## Unreleased

- Added this changelog and guidance for matching documentation to an installed
  version.
- The analytics dashboard shows knowledge and recent activity without waiting
  for all activity history. A new **Status** panel shows how far history has
  been processed.
- Knowledge analytics no longer reprocess the whole workspace after a restart or
  upgrade.

## v0.7.3 — 2026-10-01

[Release notes and downloads](https://github.com/aakarim/OpenLore/releases/tag/v0.7.3)

### Reliability across analytics, shell, and dashboard

OpenLore v0.7.3 publishes the verified v0.7 reliability work after v0.7.2 stopped before producing release artifacts.

#### Highlights

- Harden bounded, authorized analytics materialization and correct synthetic-session, exclusion, ownership-generation, and large-workspace behavior.
- Fix restricted-shell edge cases across `sed`, `grep`, `find`, `jq`, `xargs`, glob expansion, and `tail`.
- Preserve dashboard state across tab switches, restore desktop Markdown scrolling, handle incomplete usage data, and improve table rendering.
- Apply file policy to embedded files and correlate MCP shell activity with server sessions.
- Refresh configuration documentation, hook examples, and onboarding records.
- Add GoReleaser archives, Homebrew distribution, verified checksums, and durable `source-sha.txt` provenance.

#### Compatibility

No configuration migration is required. Unsupported shell options now fail clearly instead of being silently ignored. The v0.7.2 tag produced no GitHub release, Homebrew update, or GHCR image; install v0.7.3 instead.

#### Install or upgrade

```bash
go install github.com/aakarim/go-openlore/cmd/openlore@v0.7.3
openlore version
```

Expected output: `openlore 0.7.3`.

**Full changelog:** https://github.com/aakarim/OpenLore/compare/v0.7.1...v0.7.3

## v0.7.1 — 2026-09-19

[Release notes and downloads](https://github.com/aakarim/OpenLore/releases/tag/v0.7.1)

### What's Changed
* web: separate replaceable site from app assets by @aakarim in https://github.com/aakarim/OpenLore/pull/104
* Fix OPE-20: explain guest write restrictions by @aakarim in https://github.com/aakarim/OpenLore/pull/105
* Fix root analytics for workspaces over 64 MiB by @aakarim in https://github.com/aakarim/OpenLore/pull/106
* Cache current analytics facts per file (OPE-56) by @aakarim in https://github.com/aakarim/OpenLore/pull/108
* Fix MCP classification of non-zero shell output (OPE-7) by @aakarim in https://github.com/aakarim/OpenLore/pull/107
* chore: release v0.7.1 by @aakarim in https://github.com/aakarim/OpenLore/pull/109


**Full Changelog**: https://github.com/aakarim/OpenLore/compare/v0.7.0...v0.7.1

## v0.7.0 — 2026-09-17

[Release notes and downloads](https://github.com/aakarim/OpenLore/releases/tag/v0.7.0)

### What's Changed
* Revise README for clarity on OpenLore's functionality by @alfielambert in https://github.com/aakarim/OpenLore/pull/83
* fix(shell): support head and tail byte counts by @aakarim in https://github.com/aakarim/OpenLore/pull/84
* fix(oauth): preserve sessions on stale refresh retries by @aakarim in https://github.com/aakarim/OpenLore/pull/85
* fix(shell): support grep basic and fixed-string patterns by @aakarim in https://github.com/aakarim/OpenLore/pull/86
* Fix MCP shell structured output (OPE-17) by @aakarim in https://github.com/aakarim/OpenLore/pull/87
* Fix sed replacement semicolon truncation by @aakarim in https://github.com/aakarim/OpenLore/pull/88
* fix(sed): honor basic regular expressions by @aakarim in https://github.com/aakarim/OpenLore/pull/89
* fix(patch): respect unified diff hunk counts by @aakarim in https://github.com/aakarim/OpenLore/pull/91
* Fix mobile edit history opening automatically by @aakarim in https://github.com/aakarim/OpenLore/pull/93
* Fix missing docset roots in lore meta by @aakarim in https://github.com/aakarim/OpenLore/pull/96
* Release v0.7.0: analytics and read-only dashboard by @aakarim in https://github.com/aakarim/OpenLore/pull/98
* Fix dashboard distribution build by @aakarim in https://github.com/aakarim/OpenLore/pull/100
* Fix sed append whitespace handling by @aakarim in https://github.com/aakarim/OpenLore/pull/99
* Fix analytics context at workspace root by @aakarim in https://github.com/aakarim/OpenLore/pull/101
* Fix PWA link and mobile tree navigation by @aakarim in https://github.com/aakarim/OpenLore/pull/102
* Fix analytics actor attribution by @aakarim in https://github.com/aakarim/OpenLore/pull/103


**Full Changelog**: https://github.com/aakarim/OpenLore/compare/v0.6.1...v0.7.0

## v0.6.1 — 2026-09-10

[Release notes and downloads](https://github.com/aakarim/OpenLore/releases/tag/v0.6.1)

### What's Changed
* fix(http): apply mcp.require_auth to the JSON API as well as /mcp by @aakarim in https://github.com/aakarim/OpenLore/pull/77
* fix(mcp): set is_error and exit_code on non-zero shell exit by @aakarim in https://github.com/aakarim/OpenLore/pull/74
* fix(config): let an explicit config file take precedence over the embedded openlore.yml by @aakarim in https://github.com/aakarim/OpenLore/pull/76
* fix(shell): print rule rejections verbatim instead of redirect: <path>: rules: <path> by @aakarim in https://github.com/aakarim/OpenLore/pull/75
* docs(deploy): verify authenticated connectivity by @aakarim in https://github.com/aakarim/OpenLore/pull/81
* fix(history): migrate legacy commit journals by @aakarim in https://github.com/aakarim/OpenLore/pull/79
* fix(shell): support attached cut option values by @aakarim in https://github.com/aakarim/OpenLore/pull/80
* Support direct file editing over SFTP by @aakarim in https://github.com/aakarim/OpenLore/pull/78
* chore: release v0.6.1 by @aakarim in https://github.com/aakarim/OpenLore/pull/82


**Full Changelog**: https://github.com/aakarim/OpenLore/compare/v0.6.0...v0.6.1

## v0.6.0 — 2026-09-04

[Release notes and downloads](https://github.com/aakarim/OpenLore/releases/tag/v0.6.0)

### Introducing context controls

Agents are good at adding detail and bad at knowing when a shared file has enough. A focused engineering plan grows marketing context; an architecture document becomes an implementation dump; every later agent pays to read all of it.

OpenLore v0.6.0 governs that problem at write time. Set a budget for matching files and OpenLore checks every incoming write before it reaches the shared knowledge base.

### What is new

- **Three budgets:** kibibytes, lines, and estimated tokens (`size/kilobytes`, `size/lines`, `size/tokens`).
- **Directory and glob scope:** put `.lore/config.yaml` in a folder and match paths below it. Folder rules layer with `lore.json` rules; a child folder can add or tighten but never loosen a parent's rule unless the parent marks it `default: true`.
- **Fixed limits:** set an absolute `max`.
- **Sticky limits:** set `max: initial` with `growth` to derive a durable cap from the file's first baseline. Use `growth: 1.0` for a strict first-write ceiling; values above 1 allow controlled growth. `lore size baseline <path>` shows the history; `lore size baseline reset <path> [--note]` appends a new baseline and is audited.
- **Actionable rejection:** an over-budget write names the rule and its origin, reports measured versus allowed size, tells the agent to rewrite within budget or split detail into a linked sibling file, and names the override path.
- **One rules engine:** file rules run on every write surface (SSH, MCP, HTTP shell; redirects, append, `patch`, `sed -i`, `tee`, batches) and under `lore validate`. The existing OKF docset block is now shorthand for `okf`, `okf/bundle`, `link/resolves` and `link/alias` rules.
- **Discoverable policy:** `lore package list` and `lore package doc size/lines` show compiled-in members and their parameters.
- **Permissioned:** editing a folder's `.lore/config.yaml` (or resetting a baseline) requires a write grant plus a role in the docset's `config.edit`.

### Example

```yaml
# specs/.lore/config.yaml
version: 1
rules:
  spec-lines:
    match: ["**/*.md"]
    exclude: ["drafts/**"]
    use: size/lines
    with: { max: 200 }

  stable-architecture:
    match: ["architecture/*.md"]
    use: size/tokens
    with: { max: initial, growth: 1.0 }
```

`size/tokens` uses `estimate/v1`: `ceil(bytes / 4)`. It is an approximate content budget, not a provider tokenizer or billing counter.

### When a write exceeds its budget

```text
rules: /docs/adr/a.md: size/lines (adr-sticky @ /docs/adr/.lore/config.yaml)
  11 lines exceeds the limit of 10 (baseline 10 lines × growth 1, set 2026-09-04 on create)
  this file cannot grow past 10 lines under this rule
  suggested: keep a.md under 10 lines; move the new material into a sibling file such as a-details.md and add a link to it from a.md so readers can drill in
  override: a role in config.edit can run `lore size baseline reset /docs/adr/a.md`
  see: lore package doc size/lines
```

OpenLore rejects the whole write or batch. It does not rewrite the content automatically.

### Rules and provenance

Sticky baselines are durable, append-only state stored beside the governed content (`<dir>/.lore/size/<file>.jsonl`, never listed or readable through the VFS). Baseline creation and reset retain attribution, `rm` clears and recreate restarts a baseline, and moving a file carries its baseline.

### Also in this release

- Persistent HTTP shell sessions (#42) and interactive shell tab completion (#63).
- Externalized deployment lifecycle, container images on GHCR, and Fly.io/Railpack deployment (#41, #44, #46).
- Guided setup and onboarding skills (#47, #48, #50, #51, #55–#62).
- OAuth refresh retry tolerance (#38, #43); config tolerates invalid value types at startup (#49).
- Path-sharded file-history index fixing 503s on large histories (#68).
- Race-detector CI (#52).

### Install or upgrade

```bash
go install github.com/aakarim/go-openlore/cmd/openlore@v0.6.0
openlore version   # openlore 0.6.0
```

Container images: `ghcr.io/aakarim/openlore:0.6.0`, `:0.6`, `:0`. Verify downloaded binaries with `checksums.txt` below.

### Upgrade notes

Upgrading from v0.5.0 requires no policy migration. Deployments without rules keep their prior write behaviour; folder rules are opt-in. Back up content and data directories before upgrading; sticky baseline state is additive under hidden `.lore/` package state.

### Known limitations

- Estimated tokens use `ceil(bytes / 4)`, not a model-specific tokenizer. `rules.tokenizer` in `openlore.yml` is reserved and rejected.
- The release rejects or warns; it does not summarize, rewrite, or split content itself.
- The folder configuration path is `.lore/config.yaml`, not `.lore.yaml`.
- User-authored scripts, hooks, operations, and LLM policy workflows are not part of v0.6.0; the `packages`, `hooks` and `operations` sections are reserved.
- Bundle-scoped checks (OKF bundle structure, link resolution) run under `lore validate`, not on each write.

### Docs

- [Folder rules guide](https://github.com/aakarim/OpenLore/blob/v0.6.0/docs/folder-rules.md)
- [Rules standard library reference](https://github.com/aakarim/OpenLore/blob/v0.6.0/docs/rules-stdlib.md)
- [Command reference](https://github.com/aakarim/OpenLore/blob/v0.6.0/docs/commands.md)

### Commit set

**Base:** v0.5.0 (ce05c3a) · **Release head:** 682fc653cd5e38de3d643cf9016ff7877e7f8f84 · **Comparison:** `git log --oneline v0.5.0..v0.6.0` (36 commits)

### Artifacts and checksums (SHA-256)

```text
3474fa0ff448165c176116e67b7902fda62015838a8cf61ece6c153277a430f4  openlore-darwin-amd64
7025259f9d972a03058e444171c2f1b620efa366fe3ff0b0af8be89f790a6744  openlore-darwin-arm64
6452e56413dea2218711cf5ef1d23ab914600bc37e35f7307fde5247e53fcca5  openlore-linux-amd64
8641e7ac1d5fd20eeb49909c83978b031682fc38dc61126e6e7631c986dc135b  openlore-linux-arm64
96e6e1d705e82551a2e701414f94769f02ba61004396058535844ba3aad923a7  openlore-windows-amd64.exe
```

Source SHA recorded in `source-sha.txt`. Built from the tagged checkout by `.github/workflows/release.yml`.


### What's Changed
* fix(oauth): tolerate concurrent refresh retries by @aakarim in https://github.com/aakarim/OpenLore/pull/38
* feat(browser): identity header with permission-settings dropdown by @aakarim in https://github.com/aakarim/OpenLore/pull/39
* UI theme overhaul: Oiya purple/orange palette by @aakarim in https://github.com/aakarim/OpenLore/pull/40
* Add Fly.io deployment with Railpack by @aakarim in https://github.com/aakarim/OpenLore/pull/41
* Add persistent HTTP shell sessions and prevent shell panics by @aakarim in https://github.com/aakarim/OpenLore/pull/42
* Tolerate OAuth refresh retries after client backoff by @aakarim in https://github.com/aakarim/OpenLore/pull/43
* ui(browser): blue links and lighter body weight for legibility by @aakarim in https://github.com/aakarim/OpenLore/pull/45
* Add externalized deployment lifecycle and container images by @aakarim in https://github.com/aakarim/OpenLore/pull/44
* build: copy only binary into container image by @aakarim in https://github.com/aakarim/OpenLore/pull/46
* skills: make setup a guided interview by @aakarim in https://github.com/aakarim/OpenLore/pull/47
* Tolerate invalid config value types at startup by @aakarim in https://github.com/aakarim/OpenLore/pull/49
* skills: seed shared lore context during setup by @aakarim in https://github.com/aakarim/OpenLore/pull/48
* teach: interactive welcome with A/B/C server question by @aakarim in https://github.com/aakarim/OpenLore/pull/51
* Add agent-type skills: shellm support, portable openlore-skill, teach onboarding by @aakarim in https://github.com/aakarim/OpenLore/pull/50
* ci: run Go tests with race detector by @aakarim in https://github.com/aakarim/OpenLore/pull/52
* test: enforce VFS preconditions in test doubles by @aakarim in https://github.com/aakarim/OpenLore/pull/53
* docs: add SSH admin guide to embedded lore by @aakarim in https://github.com/aakarim/OpenLore/pull/54
* teach: short interactive router that fetches guides over SSH by @aakarim in https://github.com/aakarim/OpenLore/pull/55
* setup: prefer local Go, teach the permission model, fix lore.json schema by @aakarim in https://github.com/aakarim/OpenLore/pull/56
* setup: set the blueprint-vs-server mental model before asking for a folder by @aakarim in https://github.com/aakarim/OpenLore/pull/57
* Set up Amp orb development environment by @aakarim in https://github.com/aakarim/OpenLore/pull/58
* docs(setup): default_cwd / instead of /user/onboarding by @aakarim in https://github.com/aakarim/OpenLore/pull/60
* docs(setup): accept a website address for the who-is-this-for question by @aakarim in https://github.com/aakarim/OpenLore/pull/59
* docs(setup): add user role and enroll the setup agent as a delegate by @aakarim in https://github.com/aakarim/OpenLore/pull/61
* docs(deploy): offer a passkey for the web UI at the end of onboarding by @aakarim in https://github.com/aakarim/OpenLore/pull/62
* Add interactive shell tab completion by @aakarim in https://github.com/aakarim/OpenLore/pull/63
* docs: move HTTP inbox uploads into guide by @benturner11 in https://github.com/aakarim/OpenLore/pull/64
* Folder rules system: Phase 1 by @aakarim in https://github.com/aakarim/OpenLore/pull/66
* Rules: add Phase 2 package discoverability by @aakarim in https://github.com/aakarim/OpenLore/pull/67
* Implement Phase 3 folder rule configuration by @aakarim in https://github.com/aakarim/OpenLore/pull/69
* Implement folder rules phase 4 by @aakarim in https://github.com/aakarim/OpenLore/pull/70
* docs: folder rules guide, lore size commands, config keys, skill update by @aakarim in https://github.com/aakarim/OpenLore/pull/72
* Fix file-history 503s with path-sharded index by @aakarim in https://github.com/aakarim/OpenLore/pull/68
* chore: release v0.6.0 by @aakarim in https://github.com/aakarim/OpenLore/pull/73

### New Contributors
* @benturner11 made their first contribution in https://github.com/aakarim/OpenLore/pull/64

**Full Changelog**: https://github.com/aakarim/OpenLore/compare/v0.5.0...v0.6.0

## v0.5.0 — 2026-08-19

[Release notes and downloads](https://github.com/aakarim/OpenLore/releases/tag/v0.5.0)

### Provenance: who changed what, on the record

Every committed write is now recorded durably as a queryable `CommitRecord`: **who** (verified principal and delegate), **how** they authenticated, **what** changed (changeset and content hash), and **when**.

### Verified client identity

- OAuth 2.1 Client ID Metadata Documents (CIMD) provide stable client identities.
- `private_key_jwt` authentication is verified against same-origin JWKS and cannot silently downgrade to a public client when advertised.
- Optional mTLS corroboration records stronger client authentication when OpenLore terminates TLS.
- `openlore oauth keys rotate` supports hot signing-key rotation and compromise revocation.

### Per-agent, per-docset permissions

- Read/write grants per docset with roles, nested carve-outs, and home docsets.
- A permissions dashboard manages docset viewership per delegated identity in real time.
- Custom roles remain extensible through the plugin system.

### Workload Identity Federation

- The `jwt-bearer` grant verifies registered OIDC issuers and exchanges external assertions for short-lived, scope-capped OpenLore tokens.
- Claim-to-identity rules narrow rather than widen access, unmatched claims fail closed, and token lifetime is capped.
- Existing non-WIF tokens retain full scope, so the feature is additive for v0.4.x users.

WIF verification is covered by the injected-verifier test suite. A live-IdP exchange was not available in the release environment and remains an explicit verification exception.

### Also in this release

- MCP tool safety annotations.
- Browser file edit-history sidebar and delegated-identity permissions dashboard.
- Shell support for multiline `sed`, SSH redirection, `/dev/null`, and stderr redirects.
- Improved unsupported-shell diagnostics and agent-facing browser copy-path guidance.
- OAuth auth-state writability fix and updated authentication documentation.

### Install or upgrade

```bash
go install github.com/aakarim/go-openlore/cmd/openlore@v0.5.0
openlore version
```

Upgrading from v0.4.x requires no config changes. WIF and authenticated clients are opt-in.

### Known limitations

- mTLS is best-effort corroboration, not mandatory authentication; reverse-proxy certificate forwarding is unsupported.
- Write provenance is a local durable JSONL log and is not replicated.
- Per-identity permission management is available through the dashboard and configuration; there is not yet a dedicated per-identity CLI.

### Verified release

- Source: `ce05c3a26dcfae3a6ba684f12973cfd15f60d6a4`
- Gates: tests, race tests, vet, build, all five cross-platform builds, downloaded checksums, version smoke test, and downloaded-artifact OKF validation.
- Artifacts were built directly from the immutable tagged checkout. `source-sha.txt` records the source commit.

#### SHA256

```text
56e5ac4a165c64f61c5d4abdc4077bd8ea0ac64fd60f5d2316e12209186d9308  openlore-darwin-amd64
7eb07a06647a255ea1bf225125f8f77d3793929fcd8306a591d06b1be487d335  openlore-darwin-arm64
1207f9c6ac8efb1f5e21ae063101669b7b99337b2acaad434161c15dcc0e0886  openlore-linux-amd64
33083e26e04a411260091e1e4b72f6b1cc8919e899e223f803d712ed3cb37c20  openlore-linux-arm64
a821df94f3013f3fc1c400eeadcdcb330b6fa2a0ebb6aa18746b1dbdf325e505  openlore-windows-amd64.exe
```

**Full changelog:** https://github.com/aakarim/OpenLore/compare/v0.4.1...v0.5.0

## v0.4.1 — 2026-08-08

[Release notes and downloads](https://github.com/aakarim/OpenLore/releases/tag/v0.4.1)

### OKF v0.2 support

The built-in OKF validator now targets [Open Knowledge Format v0.2](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md) while continuing to accept v0.1 bundles.

- **Version detection**: a bundle's spec revision is read from the `okf_version` declaration in its root `index.md` (quoted or unquoted); versionless bundles are linted against the latest revision, and unknown versions warn instead of rejecting, per §12.
- **v0.2 field families**: `lore validate` now shape-checks the optional provenance (`sources`, `usage_window`), trust (`generated`, `verified`), lifecycle (`status`, `stale_after`), and Attested Computation contract fields — as warnings, never errors, per §11's permissive conformance policy.
- **Composable checks**: each family is an exported `okf.ConceptCheck`; consumers can assemble custom check sets or pin a spec version with `okf.ValidateBundleAs`.
- **v0.1 bundles**: declaring `okf_version: "0.1"` skips v0.2 family checks and shape-checks the legacy `timestamp` field instead. Write admission (hard conformance) is identical across both revisions.

Full changes: https://github.com/aakarim/OpenLore/compare/v0.4.0...v0.4.1

## v0.4.0 — 2026-08-07

[Release notes and downloads](https://github.com/aakarim/OpenLore/releases/tag/v0.4.0)

**OpenLore v0.4.0** makes Agent Skills a first-class, governed capability: turn any writable folder into a self-validating Skills collection, import public skills from any major Git forge, keep them synchronized with upstream, and let agents discover them with one metadata query. The web app also becomes an installable PWA, and inboxes gain authenticated HTTP upload routes.

### Agent Skills: collections, remote imports, and discovery

- **Portable Skills collections.** `skills enable [folder]` marks a directory as a recursive Agent Skills collection. The marker travels with the directory, works at runtime without a server restart, and needs no static docset configuration. `skills disable` turns collection behavior off without deleting skills.
- **Import from any public Git forge.** `skills import <spec> [parent-dir]` imports a public skill from GitHub, GitLab, Bitbucket, Codeberg, or self-hosted GitLab/Gitea/Forgejo over HTTPS. Shorthand `owner/repo` means GitHub. Repositories containing multiple skills return a candidate list with names and descriptions; rerun with the selected path, e.g. `skills import owner/repo/path/from/candidate@main`.
- **Tracked remotes.** An omitted ref tracks the repository's default branch; a `@branch` ref tracks upstream and checks for updates when `SKILL.md` is read (throttled by `remote_check_ttl`); a tag or full commit SHA is pinned. `skills update` forces a check-and-apply, `skills remove-remote` keeps the files but stops tracking. Linked skill files are read-only locally, so upstream stays the source of truth.
- **Agent-legible management.** `skills status` and `skills validate` report collection state, linked remotes, and findings as NDJSON. Ordered ChangeSet batches are rollback-safe, so a failed import leaves no partial state.
- **Discovery.** `lore meta --filter skills` scopes metadata to Skills collections and returns only valid `SKILL.md` records — frontmatter plus path:

  ```bash
  lore meta --filter skills | jq -r 'select((.name + " " + .description) | test("pdf"; "i")) | .path'
  ```

- **Configuration.** Enable the plugin in `openlore.yml`; the remote settings are optional and default as shown:

  ```yaml
  plugins:
    skills:
      enabled: true
      remote_check_ttl: 60s
      remote_timeout: 3s
      remote_max_bytes: 10MB
  ```

  Mutating skills commands require writing to be enabled and a named `rw` grant on the destination docset; a home docset is implicitly `rw` for its owner.

See the skills import demo in the [README](https://github.com/aakarim/OpenLore/blob/v0.4.0/README.md).

### Web and PWA

- The web app is now an installable **PWA**.
- File actions in the PWA, including an open action on the file overlay.
- Path aliases are hidden from the browser; canonical paths remain authoritative.

### Inbox HTTP uploads

- New authenticated HTTP upload routes for docset inboxes: identity-bound inbox credentials, bearer and HMAC authentication, an isolated upload policy, multipart metadata batches, token management, and middleware-safe partial commit reporting.

### Documentation

- README restructured with a detailed installation section and demo videos.
- Remote skill imports and skill discovery via `lore meta --filter skills` documented in `docs/plugins.md` and `docs/commands.md`.
- Stale docs removed.

### Limitations

- Skill imports support **public** repositories over HTTPS only; authenticated or private forges are not supported yet.
- Update checks happen on `SKILL.md` reads (subject to `remote_check_ttl`), not as a background sync.

### Upgrading from v0.3.x

No breaking configuration changes. To use Agent Skills management, add `plugins.skills.enabled: true` to `openlore.yml` and run `skills enable` in a writable collection directory.

**Full Changelog**: https://github.com/aakarim/OpenLore/compare/v0.3.0...v0.4.0

## v0.3.0 — 2026-07-14

[Release notes and downloads](https://github.com/aakarim/OpenLore/releases/tag/v0.3.0)

**OpenLore v0.3.0** adds first-class **Open Knowledge Format (OKF) v0.1 support**: validate knowledge as it is written, inspect its metadata efficiently, and lint complete bundles before publishing. This release also introduces role-based access control, Agent Skills collections, path aliases, a root writable overlay, and improved MCP and web experiences.

> Breaking: authorization is now role-based, and writable disk content is configured as one root overlay. Existing v0.2 configurations should be updated using the migration notes below.

### Open Knowledge Format (OKF)
- Built-in, per-docset **OKF v0.1 validator plugin** checks Markdown writes before commit. It can reject invalid documents (`enforce: true`, the default) or warn while allowing them.
- OKF scope follows docset ownership and nested-docset boundaries, keeping validation aligned with authorization. Patterns are configurable and default to `*.md`.
- New dependency-light [`pkg/okf`](https://github.com/aakarim/go-openlore/tree/v0.3.0/pkg/okf) package exposes frontmatter parsing and single-file/bundle validation for downstream Go tooling.
- New `lore validate [bundle]` lints complete bundles with grep-friendly diagnostics for OKF conformance, broken/local links, paths that escape a bundle, and alias portability.
- New `lore meta` emits frontmatter as NDJSON and lets plugins enrich metadata. When OKF applies, records include validation status.
- Built-in plugins now report their names and semantic versions in startup logs.

### Role-based access control
- Roles now group docset grants and capabilities; docsets define resource-centric allow/deny ACLs, with deny precedence and additive grants.
- Authorization is resolved dynamically for current identities, while writes and capabilities are reauthorized at invocation time.
- Nested docsets are hard access boundaries: ancestor grants cannot expose or mutate an ungranted child docset.
- Ungranted namespaces and nested docset names are hidden while navigation ancestors remain visible only when needed.
- Home directories remain implicitly read/write for their owners; `guest` is reserved for keyless access.
- New CLI management commands cover roles, identity assignments, docset ACLs, and role capabilities.

### Knowledge organization and agent tooling
- Docsets support durable **path aliases** while authorization, writes, hooks, CAS state, and events use canonical paths. `lore docsets` reports canonical and alias mounts explicitly.
- A single `writable_dir` can overlay embedded content at the virtual root, replacing separate named folder mounts.
- New opt-in **Agent Skills docset plugin** validates Agent Skills collections during admission and commit, and exposes skill discovery through `lore meta --filter`.
- The writable shell now supports atomic, file-only `mv` operations.

### MCP, OAuth, and web
- `mcp.require_auth` can force OAuth for MCP independently of keyless SSH, or explicitly allow anonymous MCP while SSH requires a key.
- OAuth resource matching accepts equivalent canonical root URIs with or without a trailing slash, improving compatibility with OAuth-native MCP clients.
- The authenticated browser now renders Markdown with breadcrumbs and navigation while keeping raw HTML disabled.
- YAML frontmatter is displayed separately and preserves nested formatting instead of rendering as Markdown.

### Migration from v0.2.x
- Define top-level `roles`, assign identities with `roles`, and move docset permissions to `docsets.<name>.access.allow` / `deny`. Legacy per-identity docset grants remain parseable but are non-authoritative when RBAC is enabled.
- Replace `folders` mounts with one `writable_dir` in `openlore.yml`; filesystem paths define content layout while `lore.json` independently defines docsets and access.
- Review nested docsets: the most-specific docset now governs access, so roles need an explicit grant on each nested boundary they should enter.
- To enable OKF, add an `okf` object to each governing docset in `lore.json`; no global OKF configuration is required.
- To require browser authentication for hosted MCP while preserving keyless SSH, set `mcp.require_auth: true`.

### Maintenance
- Relocated the metadata scanner to `pkg/openlore/meta` so the reusable read-side package lives under the OpenLore namespace.
- Removed stale internal design documents after their behavior was incorporated into current documentation.

See [`lore.json.example`](https://github.com/aakarim/go-openlore/blob/v0.3.0/lore.json.example), [`openlore.yml.example`](https://github.com/aakarim/go-openlore/blob/v0.3.0/openlore.yml.example), and the [README](https://github.com/aakarim/go-openlore/blob/v0.3.0/README.md) for complete configuration and usage.

**Full Changelog**: https://github.com/aakarim/go-openlore/compare/v0.2.0...v0.3.0

## v0.2.0 — 2026-07-09

[Release notes and downloads](https://github.com/aakarim/OpenLore/releases/tag/v0.2.0)

**OpenLore v0.2.0** turns the read-only lore server from v0.1.0 into a **writable, multi-identity knowledge substrate** with per-docset access control, agent write capabilities, and token-based auth for MCP/HTTP.

> Breaking: the `lore` view + per-identity `publish` list + docset `publish_dir` model is replaced by a per-docset **grant** model. See Migration below.

### Writable substrate
- Experimental writable filesystem (`readonly: false`) with atomic writes and a single ordered **write log** as the sole substrate writer.
- Shell write surface: `>`, `>>`, `tee`, `patch`, `sed -i`, and `publish`.
- **Session compare-and-swap**: last-read hashes are tracked so a blind overwrite fails if the file changed since it was read (`hash` / `last_write_wins`, per-docset overridable).
- Async `spawn` jobs (capability-gated) with output written back through the scoped FS; surfaced read-only at `/jobs`.

### Access control: grant model
- Each identity holds a named **grant** per docset: `ro` (read whole docset) or `rw` (read + write anywhere in it).
- **Inbox plugin** adds a `publish` grant: read the whole docset, never delete, create/edit **only** within the docset's configured `inbox` folder.
- Reads are **path-subtree scoped** — a session only sees the docsets it holds a grant on (sibling docsets are hidden).
- Writes run through a **per-operation authorizer** (grant ∩ token scope ∩ readonly locks), fail-closed.
- Unknown grant names are a **hard startup error**.
- `public_key` is now optional — passkey/token-only identities are allowed.
- Per-identity **home docset** (`$HOME`, `cd` with no args).

### Auth for MCP + HTTP
- Bearer-token auth with OAuth login (`/authorize`, `/oauth/token`), Dynamic Client Registration, and discovery endpoints.
- **Workload Identity Federation**: exchange external IdP JWTs for OpenLore tokens (jwt-bearer grant), with per-identity `match` rules that can narrow `scope`/`ttl`.
- SSH/SFTP sessions are identity-scoped through the same layered FS — **SFTP no longer bypasses read scoping**.

### MCP
- Always-on **MCP-over-HTTP** mounted on a path of the existing HTTP server (no separate port).

### Introspection
- `lore docsets` shows the per-session docset views (grant, inbox, writability, home).

### Migration from v0.1.0 `lore.json`
- Remove `docsets.<name>.publish_dir`; add `inbox: "<folder>"` where an inbox is wanted.
- Replace the top-level `lore` map with per-identity `docsets: { "<docset>": "<grant>" }`.
- Replace per-identity `lore` (read) + `publish` (write) with grants: `ro` for read-only docsets, `rw` for writable, `publish` for inbox-only.
- Add a top-level `default: { "<docset>": "ro" }` for keyless/anonymous access.
- Bearer-token config (`tokens:`) lives in `openlore.yml`, not `lore.json`.

See `lore.json.example` for the full new schema.

## v0.1.0 — 2026-06-30

[Release notes and downloads](https://github.com/aakarim/OpenLore/releases/tag/v0.1.0)

The original **OpenLore**: an SSH-accessible, read-only virtual filesystem that serves developer documentation ("lore") to humans and coding agents over plain SSH.

### Highlights
- **Browse docs over SSH** with familiar shell commands — `ls`, `cat`, `grep`, `find`, `tree`, `head`, `tail`, `sed`, `awk`, `jq`, and many more — all operating on a read-only virtual filesystem.
- **Docsets & lore** — organize documentation into docsets and compose them into named "lore" views per identity.
- **Auth** — SSH public keys, `allow_keyless` anonymous access, and passkeys (WebAuthn).
- **MCP-over-HTTP** — expose the same content to MCP clients on a path of the existing HTTP server.
- **Embeddable** — ship your own docs as an embedded filesystem with the `teach` workflow.

> This release marks the last version **before** the writable substrate and agent write capabilities were introduced. Write support (atomic writes, scoped per-agent writes, human-gated approvals, and async write-back) lands in later releases.
