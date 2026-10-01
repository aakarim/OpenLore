---
name: automating-changelogs
description: Automates changelog generation from commits, PRs, and releases following Keep a Changelog format. Use when maintaining OpenLore's changelog, setting up release workflows, generating release notes, or standardizing commit conventions.
license: MIT
metadata:
  source: https://github.com/wshobson/agents/tree/156b7a5e7a8b93642628a339ee4039c925b34c7f/plugins/documentation-generation/skills/changelog-automation
---

# Changelog Automation

Patterns and tools for automating changelog generation, release notes, and version management following industry standards.

## OpenLore integration

These repository-specific notes take precedence over the generic examples below.
The upstream skill and its reference material are retained under the MIT license
in `LICENSE`; local changes add this integration guidance, rename the skill to
match repository conventions, and repair code fences split across upstream files.

- Maintain `docs/changelog.md`, not a new root `CHANGELOG.md`. Keep maintenance
  instructions here, outside the public documentation.
- Preserve the full notes for every published GitHub release, newest first,
  including migration notes, limitations, examples, and attribution. Retrieve all
  pages with `gh api --paginate repos/aakarim/OpenLore/releases`; exclude drafts,
  label prereleases, and use publication dates. A tag alone is not a release.
- Record upcoming user-facing changes under **Unreleased**. Do not rewrite the
  historical release notes into summaries or impose a new format retroactively.
- At release time, move applicable Unreleased entries into the dated release
  section and update the current release, docs baseline, version-command example,
  and tagged-docs links in `docs/changelog.md`. Align the baseline with
  `assets/config/VERSION`; mark unreleased behavior on the pages that describe it.
  Do not add version-baseline boilerplate to the README or `docs/introduction.md`.
- OpenLore already uses GoReleaser and a tag-triggered release workflow. The
  Node/Python tooling and CI snippets in this skill are examples, not instructions
  to install dependencies or replace the existing release pipeline.
- Verify complete release coverage and notes against GitHub, version consistency,
  Markdown links and fences, and `git diff --check`; run applicable project checks.
- Public docs live in `lix-it/openlore.sh`, which pins OpenLore as `go-openlore`.
  After publishing the canonical docs commit, update that submodule pin (use the
  merged commit after a squash/rebase). Check navigation in `site/src/docs.ts`
  and the shared layout's version notice. Run `npm --prefix site run build` and
  `npm --prefix site test` in the website repo. Do not push, merge, or deploy
  without user authorization.

## When to Use This Skill

- Setting up automated changelog generation
- Implementing Conventional Commits
- Creating release note workflows
- Standardizing commit message formats
- Generating GitHub/GitLab release notes
- Managing semantic versioning

## Core Concepts

### 1. Keep a Changelog Format

```markdown
# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
```

## Detailed patterns and worked examples

Detailed pattern documentation lives in `references/details.md`. Read that file when the navigation tier above is insufficient.

### Internal release notes example (continued from the reference)

```markdown
## Summary

This release introduces dark mode support and improves checkout performance
by 40%. It also includes important security updates.

## Highlights

### 🌙 Dark Mode

Users can now switch to dark mode from settings. The preference is
automatically saved and synced across devices.

### ⚡ Performance

- Checkout flow is 40% faster
- Reduced bundle size by 15%

## Breaking Changes

None in this release.

## Upgrade Guide

No special steps required. Standard deployment process applies.

## Known Issues

- Dark mode may flicker on initial load (fix scheduled for v2.1.1)

## Dependencies Updated

| Package | From    | To      | Reason                   |
| ------- | ------- | ------- | ------------------------ |
| react   | 18.2.0  | 18.3.0  | Performance improvements |
| lodash  | 4.17.20 | 4.17.21 | Security patch           |
```

## Commit Message Examples

```bash
# Feature with scope
feat(auth): add OAuth2 support for Google login

# Bug fix with issue reference
fix(checkout): resolve race condition in payment processing

Closes #123

# Breaking change
feat(api)!: change user endpoint response format

BREAKING CHANGE: The user endpoint now returns `userId` instead of `id`.
Migration guide: Update all API consumers to use the new field name.

# Multiple paragraphs
fix(database): handle connection timeouts gracefully

Previously, connection timeouts would cause the entire request to fail
without retry. This change implements exponential backoff with up to
3 retries before failing.

The timeout threshold has been increased from 5s to 10s based on p99
latency analysis.

Fixes #456
Reviewed-by: @alice
```

## Best Practices

### Do's

- **Follow Conventional Commits** - Enables automation
- **Write clear messages** - Future you will thank you
- **Reference issues** - Link commits to tickets
- **Use scopes consistently** - Define team conventions
- **Automate releases** - Reduce manual errors

### Don'ts

- **Don't mix changes** - One logical change per commit
- **Don't skip validation** - Use commitlint
- **Don't manual edit** - Generated changelogs only
- **Don't forget breaking changes** - Mark with `!` or footer
- **Don't ignore CI** - Validate commits in pipeline
