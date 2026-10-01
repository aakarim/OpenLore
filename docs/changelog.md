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

## Unreleased

- Added this changelog and guidance for matching documentation to an installed
  version.

## v0.7.3 — 2026-10-01

Reliability improvements across analytics, the restricted shell, and the dashboard.

### Changed and fixed

- Hardened analytics materialization and fixed synthetic-session, exclusion,
  ownership-generation, and large-workspace behavior.
- Fixed shell edge cases in `sed`, `grep`, `find`, `jq`, `xargs`, glob expansion,
  and `tail`.
- Preserved dashboard state across tab switches, restored desktop Markdown
  scrolling, and improved handling of incomplete usage data and table rendering.
- Applied file policy to embedded files and correlated MCP shell activity with
  server sessions.
- Refreshed configuration documentation, hook examples, and onboarding records.
- Added GoReleaser archives, Homebrew distribution, verified checksums, and
  `source-sha.txt` release provenance.

### Upgrade notes

No configuration migration is required. Unsupported shell options now fail
clearly instead of being silently ignored.

The v0.7.2 tag did not produce a GitHub release, Homebrew update, or GHCR image.
Install v0.7.3 instead.

[Release notes and downloads](https://github.com/aakarim/OpenLore/releases/tag/v0.7.3)
· [Full diff from v0.7.1](https://github.com/aakarim/OpenLore/compare/v0.7.1...v0.7.3)

## v0.7.1 — 2026-09-19

- Separated the replaceable website from application assets.
- Clarified guest write restrictions.
- Fixed root analytics for workspaces larger than 64 MiB and cached current
  analytics facts per file.
- Fixed MCP classification of non-zero shell output.

[Release notes and downloads](https://github.com/aakarim/OpenLore/releases/tag/v0.7.1)
· [Full diff from v0.7.0](https://github.com/aakarim/OpenLore/compare/v0.7.0...v0.7.1)

## v0.7.0 — 2026-09-17

- Introduced analytics and a read-only dashboard, with fixes for workspace-root
  context and actor attribution.
- Improved shell compatibility: `head` and `tail` byte counts, `grep` basic and
  fixed-string patterns, `sed` expressions and whitespace, and unified diff
  hunk handling in `patch`.
- Preserved OAuth sessions on stale refresh retries and fixed MCP shell
  structured output.
- Fixed missing docset roots in `lore meta`, mobile navigation, and dashboard
  distribution builds.

[Release notes and downloads](https://github.com/aakarim/OpenLore/releases/tag/v0.7.0)
· [Full diff from v0.6.1](https://github.com/aakarim/OpenLore/compare/v0.6.1...v0.7.0)

## Earlier releases

See [all GitHub releases](https://github.com/aakarim/OpenLore/releases) for older
release notes and downloads. This page summarises user-facing changes; the linked
release notes and diffs provide the complete history.

## Keeping this page current

Record user-facing changes under **Unreleased**, including compatibility or
migration notes. When publishing a release, move those entries into a dated
version section and update the current release and documentation baseline here,
in the [introduction](introduction.md), and in the README's documentation section.
Keep those versions aligned with `assets/config/VERSION`, and link to the release
notes and tagged docs snapshot. Mark features that are still unreleased explicitly
on the pages that describe them.
